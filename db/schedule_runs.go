package db

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// A scheduled firing is claimed by (schedule_id, scheduled_at), so a retried
// tick or a second instance can never create the same occurrence twice.
var (
	ErrScheduleRunDuplicate = errors.New("schedule run already claimed")
	ErrScheduleRunAdvanced  = errors.New("schedule occurrence already advanced")
)

type ScheduleRun struct {
	ID          int64     `json:"id"`
	ScheduleID  int64     `json:"schedule_id"`
	ScheduledAt time.Time `json:"scheduled_at"`
	State       string    `json:"state"` // claimed (transient) | created | failed
	TaskID      string    `json:"task_id,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func claimScheduleRun(ctx context.Context, tx *sql.Tx, scheduleID int64, scheduledAt time.Time) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
INSERT INTO schedule_runs(schedule_id, scheduled_at) VALUES ($1,$2)
ON CONFLICT (schedule_id, scheduled_at) DO NOTHING
RETURNING id`, scheduleID, scheduledAt).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, ErrScheduleRunDuplicate
	}
	return id, err
}

func advanceScheduleRunCAS(ctx context.Context, tx *sql.Tx, scheduleID int64, from, to time.Time) error {
	res, err := tx.ExecContext(ctx, `
UPDATE task_schedules SET next_run_at=$3, updated_at=now()
WHERE id=$1 AND next_run_at=$2 AND enabled`, scheduleID, from, to)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrScheduleRunAdvanced
	}
	return nil
}

func finishScheduleRunTx(ctx context.Context, tx *sql.Tx, runID, scheduleID int64, state, taskID, errMsg string) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE schedule_runs SET state=$2, task_id=$3, error=$4, updated_at=now() WHERE id=$1`,
		runID, state, taskID, utf8Clean(errMsg)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE task_schedules SET last_run_at=now(), last_task_id=$2, last_error=$3, updated_at=now() WHERE id=$1`,
		scheduleID, taskID, utf8Clean(errMsg))
	return err
}

// FireTaskScheduleRun claims one occurrence and creates its task atomically with
// next_run_at advancement. A task-creation failure is rolled back to a savepoint,
// recorded as a failed run, and still advances the schedule so a broken spec
// cannot hot-loop every scheduler tick.
func (d *DB) FireTaskScheduleRun(ctx context.Context, scheduleID int64, scheduledAt, nextRunAt time.Time, description, goal string, opts TaskCreateOptions) (*Task, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	runID, err := claimScheduleRun(ctx, tx, scheduleID, scheduledAt)
	if err != nil {
		return nil, err
	}
	if err := advanceScheduleRunCAS(ctx, tx, scheduleID, scheduledAt, nextRunAt); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT schedule_task`); err != nil {
		return nil, err
	}
	t, createErr := createTaskWithOptionsTx(tx, description, goal, opts)
	if createErr != nil {
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT schedule_task`); err != nil {
			return nil, errors.Join(createErr, err)
		}
		if err := finishScheduleRunTx(ctx, tx, runID, scheduleID, "failed", "", createErr.Error()); err != nil {
			return nil, errors.Join(createErr, err)
		}
		if err := tx.Commit(); err != nil {
			return nil, errors.Join(createErr, err)
		}
		return nil, createErr
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT schedule_task`); err != nil {
		return nil, err
	}
	taskID := strconv.FormatInt(t.ID, 10)
	if err := finishScheduleRunTx(ctx, tx, runID, scheduleID, "created", taskID, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return t, nil
}

// FailTaskScheduleRun records a permanently invalid occurrence and advances the
// schedule, without creating a task.
func (d *DB) FailTaskScheduleRun(ctx context.Context, scheduleID int64, scheduledAt, nextRunAt time.Time, errMsg string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	runID, err := claimScheduleRun(ctx, tx, scheduleID, scheduledAt)
	if err != nil {
		return err
	}
	if err := advanceScheduleRunCAS(ctx, tx, scheduleID, scheduledAt, nextRunAt); err != nil {
		return err
	}
	if err := finishScheduleRunTx(ctx, tx, runID, scheduleID, "failed", "", errMsg); err != nil {
		return err
	}
	return tx.Commit()
}

// ListScheduleRuns exposes the firing ledger for diagnostics and UI drill-down.
func (d *DB) ListScheduleRuns(ctx context.Context, scheduleID int64, limit int) ([]ScheduleRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, `
SELECT id, schedule_id, scheduled_at, state, COALESCE(task_id,''), COALESCE(error,''), created_at, updated_at
FROM schedule_runs WHERE schedule_id=$1 ORDER BY scheduled_at DESC, id DESC LIMIT $2`, scheduleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleRun{}
	for rows.Next() {
		var run ScheduleRun
		if err := rows.Scan(&run.ID, &run.ScheduleID, &run.ScheduledAt, &run.State, &run.TaskID,
			&run.Error, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
