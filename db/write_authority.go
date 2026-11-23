package db

import (
	"context"
	"database/sql"
	"errors"
)

var ErrStaleExecution = errors.New("execution authority is stale; canonical graph was not modified")

// WriteAuthority is trusted runtime data, never decoded from a model/tool payload.
type WriteAuthority struct {
	TaskID           int64
	ExplorationID    int64
	IntentID         int64
	Generation       int64
	Owner            string
	LogicalSessionID string
}

func (n *Node) WriteAuthority(task int64, exploration int64) WriteAuthority {
	return WriteAuthority{TaskID: task, ExplorationID: exploration, IntentID: n.ID, Generation: n.ExecutionGeneration, Owner: n.Owner, LogicalSessionID: n.LogicalSessionID}
}
func CheckWriteAuthorityTx(ctx context.Context, tx *sql.Tx, a WriteAuthority) error {
	if a.IntentID <= 0 || a.Generation <= 0 || a.Owner == "" || a.LogicalSessionID == "" {
		return ErrStaleExecution
	}
	task := a.TaskID
	if task == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE exploration_id=$1 AND deleted_at IS NULL`, a.ExplorationID).Scan(&task); err != nil && err != sql.ErrNoRows {
			return err
		}
	}
	if task > 0 {
		var belongs bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND exploration_id=$2 AND deleted_at IS NULL)`, task, a.ExplorationID).Scan(&belongs); err != nil {
			return err
		}
		if !belongs {
			return ErrStaleExecution
		}
		if err := LockTaskEvidenceSharedTx(tx, task); err != nil {
			return err
		}
	}
	var state, owner, session string
	var generation int64
	err := tx.QueryRowContext(ctx, `SELECT state,COALESCE(owner,''),logical_session_id,execution_generation FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`, a.IntentID, a.ExplorationID).Scan(&state, &owner, &session, &generation)
	if err != nil || state != "running" || owner != a.Owner || session != a.LogicalSessionID || generation != a.Generation {
		return ErrStaleExecution
	}
	return nil
}

// CheckWriteAuthorityReadOnly verifies authority for read-only SELECT queries
// without taking any tasks or exploration_nodes row locks, avoiding cross-worker
// serialization and open-cursor transaction lock contention.
func CheckWriteAuthorityReadOnly(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, a WriteAuthority) error {
	if a.IntentID <= 0 || a.Generation <= 0 || a.Owner == "" || a.LogicalSessionID == "" {
		return ErrStaleExecution
	}
	task := a.TaskID
	if task == 0 {
		if err := q.QueryRowContext(ctx, `SELECT id FROM tasks WHERE exploration_id=$1 AND deleted_at IS NULL`, a.ExplorationID).Scan(&task); err != nil && err != sql.ErrNoRows {
			return err
		}
	}
	if task > 0 {
		var taskAlive bool
		var deleted sql.NullTime
		var archiveState sql.NullString
		err := q.QueryRowContext(ctx, `SELECT t.exploration_id=$2, t.deleted_at, (SELECT ta.state FROM task_archives ta WHERE ta.task_id=t.id) FROM tasks t WHERE t.id=$1`, task, a.ExplorationID).Scan(&taskAlive, &deleted, &archiveState)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrStaleExecution
			}
			return err
		}
		if !taskAlive {
			return ErrStaleExecution
		}
		if deleted.Valid || (archiveState.Valid && archiveState.String != ArchiveFailed) {
			return ErrTaskArchiveState
		}
	}
	var state, owner, session string
	var generation int64
	err := q.QueryRowContext(ctx, `SELECT state,COALESCE(owner,''),logical_session_id,execution_generation FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent'`, a.IntentID, a.ExplorationID).Scan(&state, &owner, &session, &generation)
	if err != nil || state != "running" || owner != a.Owner || session != a.LogicalSessionID || generation != a.Generation {
		return ErrStaleExecution
	}
	return nil
}
func (d *DB) WithAuthority(a WriteAuthority) *DB { return &DB{DB: d.DB, authority: &a} }
func (s *ExplorationStore) WithAuthority(a WriteAuthority) *ExplorationStore {
	return s.db.WithAuthority(a).Exploration(s.expID)
}
func (s *AssetStore) WithAuthority(a WriteAuthority) *AssetStore {
	return s.db.WithAuthority(a).Assets()
}

// Row commits a short authority transaction only after RETURNING/SELECT is consumed.
type Row struct {
	row     *sql.Row
	tx      *sql.Tx
	err     error
	observe func(error)
}

func (r *Row) Scan(values ...any) error {
	if r.err != nil {
		return r.err
	}
	err := r.row.Scan(values...)
	if r.observe != nil {
		r.observe(err)
	}
	if r.tx != nil {
		if err != nil {
			_ = r.tx.Rollback()
		} else {
			err = r.tx.Commit()
		}
		r.tx = nil
	}
	return err
}
func (r *Row) Err() error {
	if r.err != nil {
		return r.err
	}
	err := r.row.Err()
	// Callers that inspect Err() without Scan() must not leave the authority
	// transaction open and holding row locks. There is no result to commit.
	if r.tx != nil {
		_ = r.tx.Rollback()
		r.tx = nil
	}
	return err
}

// txRows adapts an already-owned transaction without taking commit ownership.
type txRows struct{ *sql.Tx }

func (t txRows) QueryRow(query string, args ...any) *Row {
	return &Row{row: t.Tx.QueryRow(query, args...)}
}
func (t txRows) QueryRowContext(ctx context.Context, query string, args ...any) *Row {
	return &Row{row: t.Tx.QueryRowContext(ctx, query, args...)}
}

// RowSet allows shared scanners to read pooled, snapshot and transaction rows.
type RowSet interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}
type Rows struct {
	*sql.Rows
	tx        *sql.Tx
	observe   func(error)
	finishErr error
	finished  bool
}

func (r *Rows) Next() bool {
	ok := r.Rows.Next()
	if !ok {
		_ = r.Close()
	}
	return ok
}
func (r *Rows) Scan(v ...any) error {
	err := r.Rows.Scan(v...)
	if r.observe != nil {
		r.observe(err)
	}
	if err != nil {
		r.finishErr = err
	}
	return err
}
func (r *Rows) Err() error {
	err := r.Rows.Err()
	if err == nil {
		err = r.finishErr
	}
	return err
}
func (r *Rows) Close() error {
	if r.finished {
		return r.finishErr
	}
	r.finished = true
	err := r.Rows.Err()
	closeErr := r.Rows.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = r.finishErr
	}
	if r.tx != nil {
		if err != nil {
			_ = r.tx.Rollback()
		} else {
			err = r.tx.Commit()
		}
	}
	r.finishErr = err
	if r.observe != nil {
		r.observe(err)
	}
	return err
}
func (t txRows) Query(query string, args ...any) (*Rows, error) {
	rows, err := t.Tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	return &Rows{Rows: rows}, nil
}
