package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// Pending events, not max(entity/event ID), are the consumption frontier. Even
// sequence IDs allocated by a transaction that commits late must remain visible.
type TaskTimeoutEvent struct {
	ID, TaskID                    int64
	Description, Goal, ClaimToken string
	At                            time.Time
	DeliveredTriggerIDs           []int64
	Live                          bool
}

func (d *DB) ClaimTaskTimeoutEvents(ctx context.Context, limit int, taskIDs ...int64) ([]TaskTimeoutEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	owner := hex.EncodeToString(token[:])
	rows, err := d.QueryContext(ctx, `WITH pending AS (
    SELECT id FROM task_timeout_events WHERE consumed_at IS NULL AND (lease_until IS NULL OR lease_until<=now())
    AND (COALESCE(cardinality($3::bigint[]),0)=0 OR task_id=ANY($3))
    ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1
  ) UPDATE task_timeout_events e SET claim_token=$2,lease_until=now()+interval '2 minutes'
  FROM pending p,tasks t WHERE e.id=p.id AND t.id=e.task_id
  RETURNING e.id,e.task_id,e.description,e.goal,e.occurred_at,to_jsonb(e.delivered_trigger_ids),(t.deleted_at IS NULL AND t.archived_at IS NULL)`, limit, owner, taskIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskTimeoutEvent
	for rows.Next() {
		var e TaskTimeoutEvent
		var delivered []byte
		e.ClaimToken = owner
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Description, &e.Goal, &e.At, &delivered, &e.Live); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(delivered, &e.DeliveredTriggerIDs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, rows.Err()
}

func (d *DB) RenewTaskTimeoutEvent(ctx context.Context, e TaskTimeoutEvent) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE task_timeout_events SET lease_until=now()+interval '2 minutes' WHERE id=$1 AND claim_token=$2 AND consumed_at IS NULL AND lease_until>now()`, e.ID, e.ClaimToken)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (d *DB) AckTaskTimeoutTrigger(ctx context.Context, e TaskTimeoutEvent, triggerID int64) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE task_timeout_events SET delivered_trigger_ids=array_append(delivered_trigger_ids,$3::bigint)
    WHERE id=$1 AND claim_token=$2 AND consumed_at IS NULL AND NOT ($3=ANY(delivered_trigger_ids))`, e.ID, e.ClaimToken, triggerID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (d *DB) FinishTaskTimeoutEvent(ctx context.Context, e TaskTimeoutEvent, consumed bool) error {
	_, err := d.ExecContext(ctx, `UPDATE task_timeout_events SET consumed_at=CASE WHEN $3 THEN now() ELSE consumed_at END,lease_until=NULL,claim_token=''
    WHERE id=$1 AND claim_token=$2 AND consumed_at IS NULL`, e.ID, e.ClaimToken, consumed)
	return err
}
