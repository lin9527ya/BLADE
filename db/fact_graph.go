package db

import (
	"encoding/json"
	"fmt"
	"sort"
)

// AddFactGraph keeps fact, anchors and provenance in one guarded transaction.
func (s *ExplorationStore) AddFactGraph(payload map[string]any, origin string, anchors []int64, intent int64, relations []IntentEdge) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	sort.Slice(relations, func(i, j int) bool { return relations[i].To < relations[j].To })
	for _, edge := range relations {
		if edge.Rel != RelContradicts && edge.Rel != RelSupersedes {
			return 0, fmt.Errorf("unsupported fact relation")
		}
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 FOR UPDATE`, edge.To, s.expID).Scan(&kind); err != nil {
			return 0, err
		}
		if kind != KindFact {
			return 0, fmt.Errorf("fact relation requires a fact in this exploration")
		}
	}
	if intent > 0 {
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM exploration_nodes WHERE id=$1 AND exploration_id=$2`, intent, s.expID).Scan(&kind); err != nil {
			return 0, err
		}
		if kind != KindIntent {
			return 0, fmt.Errorf("fact origin must be an intent")
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin) VALUES($1,'fact',$2,5,'confirmed',$3) RETURNING id`, s.expID, string(raw), origin).Scan(&id); err != nil {
		return 0, err
	}
	for _, asset := range anchors {
		if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, asset); err != nil {
			return 0, err
		}
	}
	if intent > 0 {
		relations = append(relations, IntentEdge{From: intent, Rel: RelYields})
	}
	for _, edge := range relations {
		from, to := edge.From, edge.To
		if from == 0 {
			from = id
		}
		if to == 0 {
			to = id
		}
		if _, err := tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, s.expID, from, edge.Rel, to); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// AddLinkedNode keeps a knowledge node, its asset anchors and its single lineage
// edge atomic. A best-effort Link after AddNode could leave loot/detection that
// looks unattributed and cannot be traced back to the intent that produced it.
func (s *ExplorationStore) AddLinkedNode(kind string, payload map[string]any, priority int, state, origin string, anchors []int64, from int64, rel string) (int64, error) {
	if from > 0 {
		if rel != RelYields && rel != RelLeaves {
			return 0, fmt.Errorf("unsupported lineage relation")
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if from > 0 {
		var src, dstExp int64
		if err := tx.QueryRow(`SELECT id,exploration_id FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 FOR SHARE`, from, s.expID).Scan(&src, &dstExp); err != nil {
			return 0, err
		}
	}
	var id int64
	if err := tx.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin)VALUES($1,$2,$3,$4,$5,$6)RETURNING id`, s.expID, kind, string(raw), priority, state, origin).Scan(&id); err != nil {
		return 0, err
	}
	for _, asset := range anchors {
		if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id,asset_id)VALUES($1,$2)ON CONFLICT DO NOTHING`, id, asset); err != nil {
			return 0, err
		}
	}
	if from > 0 {
		if _, err := tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id)VALUES($1,$2,$3,$4)ON CONFLICT DO NOTHING`, s.expID, from, rel, id); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}
