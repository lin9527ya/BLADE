package db

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

type GoalProofSummary struct {
	Total          int `json:"total"`
	NeedsReview    int `json:"needs_review"`
	HistoryUnknown int `json:"history_unknown"`
}

type GoalProofDetail struct {
	EvidenceID     int64      `json:"evidence_id,string"`
	ProvedAt       *time.Time `json:"proved_at,omitempty"`
	Kind           string     `json:"kind,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	Changes        []string   `json:"changes"`
	CurrentFlags   []string   `json:"current_flags,omitempty"`
	HistoryUnknown bool       `json:"history_unknown,omitempty"`
}

type GoalProofPage struct {
	Proofs     []GoalProofDetail `json:"proofs"`
	NextBefore string            `json:"next_before,omitempty"`
}

func (s *ExplorationStore) GoalProofSummaries(ctx context.Context, goals []int64) (map[int64]GoalProofSummary, error) {
	out := make(map[int64]GoalProofSummary, len(goals))
	if len(goals) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT goal_id,count(*),
  count(*) FILTER (WHERE cardinality(changes)>0 OR cardinality(current_flags)>0),
  count(*) FILTER (WHERE history_unknown)
FROM goal_proof_review WHERE exploration_id=$1 AND goal_id=ANY($2::bigint[]) GROUP BY goal_id`, s.expID, goals)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var summary GoalProofSummary
		if err := rows.Scan(&id, &summary.Total, &summary.NeedsReview, &summary.HistoryUnknown); err != nil {
			return nil, err
		}
		out[id] = summary
	}
	return out, rows.Err()
}

func (s *ExplorationStore) GoalProofs(ctx context.Context, goalID, before int64, limit int) (GoalProofPage, error) {
	out := GoalProofPage{Proofs: []GoalProofDetail{}}
	if before < 0 || limit < 1 || limit > 100 {
		return out, errors.New("invalid proof page")
	}
	err := s.ReadSnapshot(ctx, func(view *DB, _ *ExplorationStore) error {
		var id int64
		if err := view.QueryRowContext(ctx, `SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND id=$2 AND kind='goal'`, s.expID, goalID).Scan(&id); err != nil {
			return err
		}
		rows, err := view.QueryContext(ctx, `SELECT evidence_id,proved_at,COALESCE(kind,''),COALESCE(reason,''),
    to_json(changes),to_json(current_flags),history_unknown
FROM goal_proof_review WHERE exploration_id=$1 AND goal_id=$2 AND ($3::bigint=0 OR evidence_id<$3)
ORDER BY evidence_id DESC LIMIT $4`, s.expID, goalID, before, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if len(out.Proofs) == limit {
				out.NextBefore = strconv.FormatInt(out.Proofs[limit-1].EvidenceID, 10)
				break
			}
			var proof GoalProofDetail
			var changes, flags []byte
			if err := rows.Scan(&proof.EvidenceID, &proof.ProvedAt, &proof.Kind, &proof.Reason, &changes, &flags, &proof.HistoryUnknown); err != nil {
				return err
			}
			if err := json.Unmarshal(changes, &proof.Changes); err != nil {
				return err
			}
			if err := json.Unmarshal(flags, &proof.CurrentFlags); err != nil {
				return err
			}
			out.Proofs = append(out.Proofs, proof)
		}
		return rows.Err()
	})
	return out, err
}
