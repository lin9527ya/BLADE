package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type IntentRequirement struct {
	Type          string `json:"type"`
	NodeID        int64  `json:"node_id,omitempty"`
	Key           string `json:"key,omitempty"`
	Target        string `json:"target,omitempty"`
	Polarity      string `json:"polarity,omitempty"`
	MaxAgeSeconds int64  `json:"max_age_seconds,omitempty"`
	Verification  bool   `json:"verification,omitempty"`
}

type EvidenceSource struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}
type EvidenceMetadata struct {
	ClaimKey   string           `json:"claim_key,omitempty"`
	ObservedAt string           `json:"observed_at,omitempty"`
	ValidUntil string           `json:"valid_until,omitempty"`
	Method     string           `json:"method,omitempty"`
	Scope      []int64          `json:"scope,omitempty"`
	Sources    []EvidenceSource `json:"source_refs,omitempty"`
}

func (s *ExplorationStore) ValidateRequirements(requirements []IntentRequirement) error {
	if len(requirements) > 16 {
		return fmt.Errorf("requires最多16项")
	}
	for _, r := range requirements {
		if r.Type != "fact" && r.Type != "capability" && r.Type != "credential" {
			return fmt.Errorf("requires type只能是fact/capability/credential")
		}
		if r.NodeID < 0 || (r.NodeID == 0 && strings.TrimSpace(r.Key) == "") {
			return fmt.Errorf("requires需node_id或明确key")
		}
		if len(r.Key) > 128 || r.MaxAgeSeconds < 0 || r.MaxAgeSeconds > 31536000 {
			return fmt.Errorf("requires key或时效无效")
		}
		if r.Polarity != "" && r.Polarity != "any" && r.Polarity != "positive" && r.Polarity != "negative" {
			return fmt.Errorf("requires polarity无效")
		}
		if r.Verification && (r.Type != "credential" || r.Target == "") {
			return fmt.Errorf("凭据验证条件必须指定target")
		}
		if r.NodeID > 0 {
			n, err := s.GetNode(r.NodeID)
			if err != nil {
				return err
			}
			if n == nil || ((r.Type == "fact" && n.Kind != KindFact) || (r.Type == "credential" && n.Kind != KindCredential) || (r.Type == "capability" && n.Kind != KindFact && n.Kind != KindCredential && n.Kind != KindFoothold)) {
				return fmt.Errorf("requires引用必须是本任务对应类型的节点")
			}
		}
	}
	return nil
}

func (s *ExplorationStore) ValidateEvidenceMetadata(meta *EvidenceMetadata) error {
	if meta == nil {
		return nil
	}
	if len(meta.ClaimKey) > 128 || len(meta.Method) > 256 || len(meta.Sources) > 16 || len(meta.Scope) > 64 {
		return fmt.Errorf("证据元信息超限")
	}
	var observed, valid time.Time
	var err error
	if meta.ObservedAt != "" {
		observed, err = time.Parse(time.RFC3339, meta.ObservedAt)
		if err != nil {
			return fmt.Errorf("observed_at需RFC3339")
		}
	}
	if meta.ValidUntil != "" {
		valid, err = time.Parse(time.RFC3339, meta.ValidUntil)
		if err != nil {
			return fmt.Errorf("valid_until需RFC3339")
		}
	}
	if !observed.IsZero() && !valid.IsZero() && valid.Before(observed) {
		return fmt.Errorf("valid_until早于observed_at")
	}
	for _, id := range meta.Scope {
		var exists bool
		err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM assets a LEFT JOIN tasks t ON t.exploration_id=$2 AND t.deleted_at IS NULL WHERE a.id=$1 AND (t.id=ANY(a.task_ids) OR EXISTS(SELECT 1 FROM exploration_anchors anchor WHERE anchor.asset_id=a.id AND anchor.exploration_id=$2)))`, id, s.expID).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("evidence scope资产不属于本任务")
		}
	}
	for _, ref := range meta.Sources {
		if ref.ID <= 0 {
			return fmt.Errorf("证据引用id无效")
		}
		var exists bool
		switch ref.Type {
		case "activity":
			err = s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM activity WHERE id=$1 AND exploration_id IN (SELECT exploration_id FROM tasks WHERE exploration_id=$2 AND deleted_at IS NULL UNION SELECT source.exploration_id FROM task_relations rel JOIN tasks current ON current.id=rel.task_id AND current.exploration_id=$2 AND current.deleted_at IS NULL JOIN tasks source ON source.id=rel.source_task_id AND source.deleted_at IS NULL))`, ref.ID, s.expID).Scan(&exists)
		case "execution":
			err = s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM tool_executions e JOIN tasks t ON e.task_id=t.id::text WHERE e.id=$1 AND t.exploration_id IN (SELECT exploration_id FROM tasks WHERE exploration_id=$2 AND deleted_at IS NULL UNION SELECT source.exploration_id FROM task_relations rel JOIN tasks current ON current.id=rel.task_id AND current.exploration_id=$2 AND current.deleted_at IS NULL JOIN tasks source ON source.id=rel.source_task_id AND source.deleted_at IS NULL))`, ref.ID, s.expID).Scan(&exists)
		case "finding_binding":
			err = s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM finding_traffic_bindings WHERE id=$1 AND exploration_id IN (SELECT exploration_id FROM tasks WHERE exploration_id=$2 AND deleted_at IS NULL UNION SELECT source.exploration_id FROM task_relations rel JOIN tasks current ON current.id=rel.task_id AND current.exploration_id=$2 AND current.deleted_at IS NULL JOIN tasks source ON source.id=rel.source_task_id AND source.deleted_at IS NULL))`, ref.ID, s.expID).Scan(&exists)
		default:
			return fmt.Errorf("source_refs type无效")
		}
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("证据引用不存在或不属于本任务")
		}
	}
	return nil
}

func intentRequirementsReady(ctx context.Context, tx *sql.Tx, exploration int64, n *Node, lock bool) (bool, []string, error) {
	var p struct {
		Requires []IntentRequirement `json:"requires"`
	}
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return false, nil, err
	}

	if lock {
		for _, r := range p.Requires {
			rows, err := tx.QueryContext(ctx, `SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND (id=$2 OR ($2=0 AND (payload->'evidence_meta'->>'claim_key'=$3 OR COALESCE(payload->'enables','[]'::jsonb)?$3))) ORDER BY id FOR SHARE`, exploration, r.NodeID, r.Key)
			if err != nil {
				return false, nil, err
			}
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return false, nil, err
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return false, nil, err
			}
		}
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT blackboard_requirement_issues($1::jsonb,$2::bigint)`, string(n.Payload), exploration).Scan(&raw); err != nil {
		return false, nil, err
	}
	var issues []json.RawMessage
	if err := json.Unmarshal(raw, &issues); err != nil {
		return false, nil, err
	}
	reasons := make([]string, len(issues))
	for i, v := range issues {
		reasons[i] = string(v)
	}
	return len(issues) == 0, reasons, nil
}
func (s *ExplorationStore) RequirementIssues(id int64) ([]map[string]any, error) {
	var raw []byte
	err := s.db.QueryRow(`SELECT blackboard_requirement_issues(payload,exploration_id) FROM exploration_nodes WHERE id=$1 AND exploration_id=$2`, id, s.expID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}
