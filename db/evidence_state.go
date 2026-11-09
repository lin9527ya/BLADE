package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

type EvidenceState struct {
	Flags      []string `json:"flags"`
	ObservedAt string   `json:"observed_at,omitempty"`
	ValidUntil string   `json:"valid_until,omitempty"`
}

func (s *ExplorationStore) EvidenceStates(ids []int64) (map[int64]EvidenceState, error) {
	out := map[int64]EvidenceState{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT n.id,COALESCE(n.payload->'evidence_meta'->>'observed_at',''),COALESCE(n.payload->'evidence_meta'->>'valid_until',''),COALESCE(blackboard_time(n.payload->'evidence_meta'->>'valid_until')<=now(),false),EXISTS(SELECT 1 FROM exploration_edges e WHERE e.exploration_id=n.exploration_id AND e.rel='contradicts' AND (e.src_id=n.id OR e.dst_id=n.id)),EXISTS(SELECT 1 FROM exploration_edges e WHERE e.exploration_id=n.exploration_id AND e.rel='supersedes' AND e.dst_id=n.id) FROM exploration_nodes n WHERE n.exploration_id=$1 AND n.id=ANY($2)`, s.expID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var v EvidenceState
		var expired, contested, superseded bool
		if err := rows.Scan(&id, &v.ObservedAt, &v.ValidUntil, &expired, &contested, &superseded); err != nil {
			return nil, err
		}
		v.Flags = []string{}
		if v.ObservedAt == "" {
			v.Flags = append(v.Flags, "observation_time_unknown")
		}
		if expired {
			v.Flags = append(v.Flags, "expired")
		}
		if contested {
			v.Flags = append(v.Flags, "contested")
		}
		if superseded {
			v.Flags = append(v.Flags, "superseded")
		}
		out[id] = v
	}
	return out, rows.Err()
}

// LinkEvidenceRelation retains both originals and prevents new->old replacement cycles.
func (s *ExplorationStore) LinkEvidenceRelation(from int64, rel string, to int64) error {
	if from <= 0 || to <= 0 || from == to || (rel != RelContradicts && rel != RelSupersedes) {
		return fmt.Errorf("invalid evidence relation")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Same ordered row locks as requires and other evidence relation writers.
	rows, err := tx.Query(`SELECT id,kind FROM exploration_nodes WHERE exploration_id=$1 AND id=ANY($2) ORDER BY id FOR UPDATE`, s.expID, []int64{from, to})
	if err != nil {
		return err
	}
	n := 0
	for rows.Next() {
		var id int64
		var kind string
		if err := rows.Scan(&id, &kind); err != nil {
			rows.Close()
			return err
		}
		if kind != KindFact {
			rows.Close()
			return fmt.Errorf("evidence relation endpoints must be facts")
		}
		n++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if n != 2 {
		return sql.ErrNoRows
	}
	if rel == RelSupersedes {
		var cycle bool
		err := tx.QueryRow(`WITH RECURSIVE reachable(id) AS (SELECT $1::bigint UNION SELECT e.dst_id FROM exploration_edges e JOIN reachable r ON e.src_id=r.id WHERE e.exploration_id=$2 AND e.rel='supersedes') SELECT EXISTS(SELECT 1 FROM reachable WHERE id=$3)`, to, s.expID, from).Scan(&cycle)
		if err != nil {
			return err
		}
		if cycle {
			return fmt.Errorf("supersedes would create a cycle")
		}
	}
	_, err = tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id)VALUES($1,$2,$3,$4)ON CONFLICT DO NOTHING`, s.expID, from, rel, to)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ExplorationStore) ReadGraphOperation(id string) (map[string]any, error) {
	var code, detail string
	err := s.db.QueryRow(`SELECT reason_code,detail FROM blackboard_operations WHERE id=$1 AND exploration_id=$2`, id, s.expID).Scan(&code, &detail)
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": id, "reason_code": code, "detail": detail}, nil
}

// ValidateStoredRequires is also used by direct Go admissions, not only tool JSON.
func (s *ExplorationStore) validateStoredRequires(payload map[string]any) error {
	raw, ok := payload["requires"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var r []IntentRequirement
	if string(b) == "null" {
		return fmt.Errorf("requires must be an array")
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	return s.ValidateRequirements(r)
}

func (s *ExplorationStore) PopulateEvidenceReadState(nodes []*Node) error {
	var ids []int64
	for _, n := range nodes {
		if n != nil {
			ids = append(ids, n.ID)
		}
	}
	states, err := s.EvidenceStates(ids)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if n.Kind == KindFact || n.Kind == KindCredential || n.Kind == KindFoothold {
			v := states[n.ID]
			n.EvidenceState = &v
		}
		if n.Kind == KindIntent {
			var p map[string]json.RawMessage
			if err := json.Unmarshal(n.Payload, &p); err != nil {
				return err
			}
			if _, ok := p["requires"]; ok {
				issues, err := s.RequirementIssues(n.ID)
				if err != nil {
					return err
				}
				n.RequiresMissing = issues
			}
		}
	}
	return nil
}
