package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// PerIntentWorkerSession reports whether workers isolate each intent in its
// own logical session rather than sharing one transcript per legacy slot.
// BLADE_WORKER_PER_INTENT_SESSION explicitly forces on ("1"/"true"/"yes") or
// off ("0"/"false"/"no"). Defaults to true.
func PerIntentWorkerSession(_ string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BLADE_WORKER_PER_INTENT_SESSION"))) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	return true
}

func logicalSessionForClaim(n *Node, exploration int64, owner string) string {
	if n.LogicalSessionID != "" {
		return n.LogicalSessionID
	}
	if PerIntentWorkerSession("") {
		return fmt.Sprintf("exp%d-worker-i%d", exploration, n.ID)
	}
	var p struct {
		Resume   bool `json:"resume_after_restart"`
		Slot     int  `json:"resume_slot"`
		Attempts int  `json:"attempts"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	slot := 0
	if p.Resume && p.Slot > 0 {
		slot = p.Slot
	} else if p.Attempts > 0 && strings.HasPrefix(n.Owner, "work#") {
		slot, _ = strconv.Atoi(strings.TrimPrefix(n.Owner, "work#"))
	}
	if slot <= 0 && strings.HasPrefix(owner, "work#") {
		slot, _ = strconv.Atoi(strings.TrimPrefix(owner, "work#"))
	}
	if slot > 0 {
		return fmt.Sprintf("exp%d-worker-slot%d", exploration, slot)
	}
	return fmt.Sprintf("exp%d-worker-i%d", exploration, n.ID)
}

// ClaimNode returns the post-CAS row. Callers must not use a frontier snapshot as authority.
func (s *ExplorationStore) ClaimNode(ctx context.Context, id int64, owner, expected string, ttl time.Duration, pool []string, slotAffinity bool, snapshots ...*IntentScheduleExplanation) (*Node, error) {
	var snapshot any
	if len(snapshots) > 0 && snapshots[0] != nil {
		if snapshots[0].IntentID != id {
			return nil, fmt.Errorf("dispatch snapshot belongs to another intent")
		}
		raw, err := json.Marshal(snapshots[0])
		if err != nil {
			return nil, err
		}
		snapshot = string(raw)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var task int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE exploration_id=$1 AND deleted_at IS NULL`, s.expID).Scan(&task)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if task > 0 {
		if err := LockTaskEvidenceTx(tx, task); err != nil {
			return nil, err
		}
	}
	n, err := scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`, id, s.expID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if n.State != expected {
		return nil, nil
	}
	session := logicalSessionForClaim(n, s.expID, owner)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "blade.logical-session:"+session); err != nil {
		return nil, err
	}
	next, err := scanNode(tx.QueryRowContext(ctx, `UPDATE exploration_nodes SET state='running',owner=$1,
 logical_session_id=$2,execution_generation=execution_generation+1,content_version=content_version+1,
 heartbeat_at=now(),lease_expires_at=now()+($3*interval '1 second'),blocked_reason=NULL,completed_at=NULL,
 semantic_outcome='',semantic_note='',
 payload=(COALESCE(payload,'{}'::jsonb)-'resume_after_restart'-'resume_slot')||jsonb_build_object('attempts',
 CASE WHEN COALESCE(payload->>'attempts','') ~ '^[0-9]{1,6}$' THEN (payload->>'attempts')::int+1 ELSE 1 END,
 'first_claimed_at',COALESCE(NULLIF(payload->'first_claimed_at','null'::jsonb),to_jsonb(now())),
 'last_claimed_at',now()) || CASE WHEN $7::jsonb IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('dispatch_snapshot',$7::jsonb) END
 WHERE id=$4 AND exploration_id=$5 AND state=$6 RETURNING `+nodeCols, owner, session, int64(ttl/time.Second), id, s.expID, expected, snapshot))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return next, nil
}
