package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

type BlackboardVersion struct {
	ExplorationID int64  `json:"exploration_id"`
	Epoch         string `json:"epoch"`
	Revision      int64  `json:"-"`
}

func (v BlackboardVersion) Cursor() string {
	return fmt.Sprintf("ev1:%d:%s:%d", v.ExplorationID, v.Epoch, v.Revision)
}

type BlackboardChange struct {
	Sequence       string   `json:"seq"`
	Entity         string   `json:"entity"`
	Kind           string   `json:"kind,omitempty"`
	ObjectKey      string   `json:"object_key"`
	Operation      string   `json:"operation"`
	NodeID         *int64   `json:"node_id,omitempty"`
	SourceID       *int64   `json:"src_id,omitempty"`
	TargetID       *int64   `json:"dst_id,omitempty"`
	Relation       string   `json:"relation,omitempty"`
	Before         string   `json:"old_state,omitempty"`
	After          string   `json:"new_state,omitempty"`
	ContentVersion *int64   `json:"content_version,omitempty"`
	Fields         []string `json:"changed_fields"`
	OperationID    string   `json:"operation_id,omitempty"`
	ReasonCode     string   `json:"reason_code,omitempty"`
	// Semantic patch for node changes: a short summary and the current polarity /
	// confidence let the planner understand an update without a detail round-trip.
	// DetailRequired marks changes whose content matters enough to read node_detail.
	Summary        string `json:"summary,omitempty"`
	Polarity       string `json:"polarity,omitempty"`
	Confidence     string `json:"confidence,omitempty"`
	Status         string `json:"status,omitempty"`
	DetailRequired bool   `json:"detail_required,omitempty"`
}

type BlackboardChanges struct {
	Revision string             `json:"revision"`
	Cursor   string             `json:"change_cursor"`
	Changes  []BlackboardChange `json:"changes"`
	Status   ReadStatus         `json:"read_status"`
	Resync   bool               `json:"resync_required"`
}

func (s *ExplorationStore) BlackboardVersion(ctx context.Context) (BlackboardVersion, error) {
	v := BlackboardVersion{ExplorationID: s.expID}
	err := s.db.QueryRowContext(ctx, `SELECT epoch,revision FROM blackboard_versions WHERE exploration_id=$1`, s.expID).Scan(&v.Epoch, &v.Revision)
	if err == sql.ErrNoRows {
		// Creation installs the row; a missing row means nothing was ever published,
		// so an explicit empty baseline is accurate and read-only-safe. A stored
		// synthetic cursor will resync as soon as the first real event appears.
		return BlackboardVersion{ExplorationID: s.expID, Epoch: "uninitialized"}, nil
	}
	return v, err
}

func (s *ExplorationStore) BlackboardChanges(ctx context.Context, cursor string, limit int) (BlackboardChanges, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if !s.InReadSnapshot() {
		var out BlackboardChanges
		err := s.ReadSnapshot(ctx, func(_ *DB, view *ExplorationStore) error {
			var err error
			out, err = view.BlackboardChanges(ctx, cursor, limit)
			return err
		})
		if err != nil {
			out.Status = ReadStatus{State: "unknown"}
			return out, err
		}
		return out, nil
	}
	out := BlackboardChanges{Changes: []BlackboardChange{}}
	version, err := s.BlackboardVersion(ctx)
	if err != nil {
		out.Status = ReadStatus{State: "unknown"}
		return out, err
	}
	out.Revision = strconv.FormatInt(version.Revision, 10)
	after := int64(0)
	if cursor != "" {
		parts := strings.Split(cursor, ":")
		if len(parts) != 4 || parts[0] != "ev1" {
			return out, errors.New("invalid blackboard change cursor")
		}
		board, berr := strconv.ParseInt(parts[1], 10, 64)
		sequence, serr := strconv.ParseInt(parts[3], 10, 64)
		if berr != nil || serr != nil || sequence < 0 || board != s.expID {
			return out, errors.New("cursor does not belong to this blackboard")
		}
		if parts[2] != version.Epoch || sequence > version.Revision {
			out.Resync = true
		} else {
			after = sequence
		}
	}
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT min(seq) FROM blackboard_events WHERE exploration_id=$1 AND epoch=$2`, s.expID, version.Epoch).Scan(&oldest); err != nil {
		return out, err
	}
	if cursor != "" && oldest.Valid && after < oldest.Int64-1 {
		out.Resync = true
	}
	if cursor != "" && !out.Resync {
		var remaining int64
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM blackboard_events WHERE exploration_id=$1 AND epoch=$2 AND seq>$3 AND seq<=$4`, s.expID, version.Epoch, after, version.Revision).Scan(&remaining); err != nil {
			return out, err
		}
		if remaining != version.Revision-after {
			out.Resync = true
		}
	}
	if cursor == "" || out.Resync {
		out.Cursor = version.Cursor()
		rows, err := s.db.QueryContext(ctx, `SELECT seq,entity,object_key,operation,node_id,src_id,dst_id,COALESCE(relation,''),COALESCE(old_state,''),COALESCE(new_state,''),content_version,to_json(changed_fields),COALESCE(operation_id,''),COALESCE(reason_code,'') FROM blackboard_events WHERE exploration_id=$1 AND epoch=$2 ORDER BY seq DESC LIMIT $3`, s.expID, version.Epoch, limit)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		changes, err := scanBlackboardChanges(rows)
		if err != nil {
			return out, err
		}
		for i, j := 0, len(changes)-1; i < j; i, j = i+1, j-1 {
			changes[i], changes[j] = changes[j], changes[i]
		}
		out.Changes = changes
	} else {
		rows, err := s.db.QueryContext(ctx, `SELECT seq,entity,object_key,operation,node_id,src_id,dst_id,COALESCE(relation,''),COALESCE(old_state,''),COALESCE(new_state,''),content_version,to_json(changed_fields),COALESCE(operation_id,''),COALESCE(reason_code,'') FROM blackboard_events WHERE exploration_id=$1 AND epoch=$2 AND seq>$3 AND seq<=$4 ORDER BY seq LIMIT $5`, s.expID, version.Epoch, after, version.Revision, limit)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		out.Changes, err = scanBlackboardChanges(rows)
		if err != nil {
			return out, err
		}
		current := version
		current.Revision = after
		if len(out.Changes) > 0 {
			current.Revision, _ = strconv.ParseInt(out.Changes[len(out.Changes)-1].Sequence, 10, 64)
		}
		out.Cursor = current.Cursor()
	}
	s.enrichChanges(ctx, out.Changes)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM blackboard_events WHERE exploration_id=$1 AND epoch=$2 AND seq>$3 AND seq<=$4`, s.expID, version.Epoch, after, version.Revision).Scan(&total); err != nil {
		return out, err
	}
	out.Status = ReadSetStatus(total, len(out.Changes), "blackboard_view(change_cursor)")
	return out, nil
}

func scanBlackboardChanges(rows RowSet) ([]BlackboardChange, error) {
	out := make([]BlackboardChange, 0)
	for rows.Next() {
		var row BlackboardChange
		var seq int64
		var fields []byte
		if err := rows.Scan(&seq, &row.Entity, &row.ObjectKey, &row.Operation, &row.NodeID, &row.SourceID, &row.TargetID, &row.Relation, &row.Before, &row.After, &row.ContentVersion, &fields, &row.OperationID, &row.ReasonCode); err != nil {
			return nil, err
		}
		row.Sequence = strconv.FormatInt(seq, 10)
		if err := json.Unmarshal(fields, &row.Fields); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func GraphOperationTx(tx *sql.Tx, exploration int64, code, detail string) (string, error) {
	id := uuid.NewString()
	if _, err := tx.Exec(`INSERT INTO blackboard_operations(id,exploration_id,reason_code,detail) VALUES($1,$2,$3,$4)`, id, exploration, code, detail); err != nil {
		return "", err
	}
	_, err := tx.Exec(`SELECT set_config('blade.graph_operation',$1,true),set_config('blade.graph_reason',$2,true)`, id, code)
	return id, err
}

func (s *ExplorationStore) ReadSnapshot(ctx context.Context, fn func(*DB, *ExplorationStore) error) error {
	return s.db.ReadSnapshot(ctx, func(view *DB) error { return fn(view, view.Exploration(s.expID)) })
}

func (s *ExplorationStore) InReadSnapshot() bool   { return s.db.InReadSnapshot() }
func (v BlackboardVersion) RevisionString() string { return strconv.FormatInt(v.Revision, 10) }
func (s *ExplorationStore) CountProjectionNodes(kind, state string, terminal bool) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind=$2 AND ($3='' OR state=$3) AND (NOT $4 OR state IN ('done','negative','no_output','blocked','exhausted','stopped','out-of-scope'))`, s.expID, kind, state, terminal).Scan(&n)
	return n, err
}

// CountAllProjectionNodes computes every read_status projection section total
// in a single scan instead of issuing 10 separate COUNT queries per overview.
func (s *ExplorationStore) CountAllProjectionNodes() (map[string]int64, error) {
	var goals, hints, facts, findings, footholds, credentials, openI, runningI, pausedI, doneI int64
	err := s.db.QueryRow(`SELECT
  COUNT(*) FILTER (WHERE kind=$2),
  COUNT(*) FILTER (WHERE kind=$3 AND state=$4),
  COUNT(*) FILTER (WHERE kind=$5),
  COUNT(*) FILTER (WHERE kind=$6),
  COUNT(*) FILTER (WHERE kind=$7 AND state=$8),
  COUNT(*) FILTER (WHERE kind=$9 AND state=$8),
  COUNT(*) FILTER (WHERE kind=$10 AND state='open'),
  COUNT(*) FILTER (WHERE kind=$10 AND state='running'),
  COUNT(*) FILTER (WHERE kind=$10 AND state='paused'),
  COUNT(*) FILTER (WHERE kind=$10 AND state IN ('done','negative','no_output','blocked','exhausted','stopped','out-of-scope'))
FROM exploration_nodes
WHERE exploration_id=$1`,
		s.expID, KindGoal, KindHint, StateHintActive, KindFact, KindFinding,
		KindFoothold, StateLootActive, KindCredential, KindIntent,
	).Scan(&goals, &hints, &facts, &findings, &footholds, &credentials, &openI, &runningI, &pausedI, &doneI)
	if err != nil {
		return nil, err
	}
	return map[string]int64{
		"goals":               goals,
		"hints":               hints,
		"recent_facts":        facts,
		"finding_list":        findings,
		"footholds":           footholds,
		"credentials":         credentials,
		"open_intents":        openI,
		"running_intents":     runningI,
		"paused_intents":      pausedI,
		"recent_done_intents": doneI,
	}, nil
}
func (s *ExplorationStore) AllByKindState(kind, state string) ([]*Node, error) {
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND kind=$2 AND ($3='' OR state=$3) ORDER BY id`, s.expID, kind, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// enrichChanges attaches a compact semantic patch to node and finding changes.
// It reads current payloads/rows in one query per entity for the change page
// only; it never stores raw payloads in the journal. Deleted nodes have no row,
// so their change keeps only the state transition recorded by the trigger.
func (s *ExplorationStore) enrichChanges(ctx context.Context, changes []BlackboardChange) {
	if len(changes) == 0 {
		return
	}
	nodeIDs := make([]int64, 0, len(changes))
	findingNodeIDs := make([]int64, 0, len(changes))
	seenNodes, seenFindings := map[int64]bool{}, map[int64]bool{}
	for _, c := range changes {
		if c.NodeID == nil {
			continue
		}
		switch c.Entity {
		case "node":
			if !seenNodes[*c.NodeID] {
				seenNodes[*c.NodeID] = true
				nodeIDs = append(nodeIDs, *c.NodeID)
			}
		case "finding":
			if !seenFindings[*c.NodeID] {
				seenFindings[*c.NodeID] = true
				findingNodeIDs = append(findingNodeIDs, *c.NodeID)
			}
		}
	}
	kinds := map[int64]string{}
	payloads := map[int64]map[string]any{}
	if len(nodeIDs) > 0 {
		if rows, err := s.db.QueryContext(ctx, `SELECT id,kind,payload FROM exploration_nodes
WHERE exploration_id=$1 AND id=ANY($2::bigint[])`, s.expID, nodeIDs); err == nil {
			for rows.Next() {
				var id int64
				var kind string
				var raw []byte
				if rows.Scan(&id, &kind, &raw) != nil {
					continue
				}
				kinds[id] = kind
				var p map[string]any
				if json.Unmarshal(raw, &p) == nil {
					payloads[id] = p
				}
			}
			_ = rows.Close()
		}
	}
	type findingPatch struct{ summary, status, confidence string }
	findings := map[int64]findingPatch{}
	if len(findingNodeIDs) > 0 {
		if rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT ON (node_id) node_id,summary,status,confidence
FROM findings WHERE node_id=ANY($1::bigint[]) ORDER BY node_id,id DESC`, findingNodeIDs); err == nil {
			for rows.Next() {
				var nodeID int64
				var p findingPatch
				if rows.Scan(&nodeID, &p.summary, &p.status, &p.confidence) == nil {
					findings[nodeID] = p
				}
			}
			_ = rows.Close()
		}
	}
	for i := range changes {
		c := &changes[i]
		if c.NodeID == nil {
			continue
		}
		switch c.Entity {
		case "node":
			if k := kinds[*c.NodeID]; k != "" {
				c.Kind = k
			}
			p := payloads[*c.NodeID]
			if p == nil {
				continue
			}
			if v, ok := p["summary"].(string); ok {
				c.Summary = truncateUTF8(strings.TrimSpace(v), 240)
			}
			if v, ok := p["polarity"].(string); ok {
				c.Polarity = v
			}
			if v, ok := p["confidence"].(string); ok {
				c.Confidence = v
			}
		case "finding":
			c.Kind = KindFinding
			p, ok := findings[*c.NodeID]
			if !ok {
				continue
			}
			c.Summary = truncateUTF8(strings.TrimSpace(p.summary), 240)
			c.Status = p.status
			c.Confidence = p.confidence
		default:
			continue
		}
		c.DetailRequired = changeNeedsDetail(*c)
	}
}

func changeNeedsDetail(c BlackboardChange) bool {
	for _, field := range c.Fields {
		switch field {
		case "payload", "state", "status", "confidence", "evidence_version",
			"delete_reason", "semantic_outcome", "semantic_note":
			return true
		}
	}
	return false
}
