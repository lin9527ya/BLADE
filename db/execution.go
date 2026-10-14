package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

// P13 工具执行台账：长任务（nmap/nuclei/ffuf/字典跑批）由 host 侧的执行服务托管，
// 进程独立进程组存活、输出落盘、状态进本表。意图收尾门（有活跃执行不许判完）与
// 租约看门狗（有活跃执行的意图不算僵死）都以 ExecutionActive 为准。
//
// 状态机：queued → running → completed | failed | cancelled | hard_timeout；
// orphaned = 服务重启后遗留的 running（内存里没有对应进程），有意不计入 failed。

// Execution statuses.
const (
	ExecQueued      = "queued"
	ExecRunning     = "running"
	ExecCompleted   = "completed"
	ExecFailed      = "failed"
	ExecCancelled   = "cancelled"
	ExecHardTimeout = "hard_timeout"
	ExecOrphaned    = "orphaned"
)

// ExecutionActive reports whether a status is still in flight.
func ExecutionActive(status string) bool {
	return status == ExecQueued || status == ExecRunning
}

// ExecutionTerminal reports whether a status is final.
func ExecutionTerminal(status string) bool {
	switch status {
	case ExecCompleted, ExecFailed, ExecCancelled, ExecHardTimeout, ExecOrphaned:
		return true
	}
	return false
}

// ToolExecution is one row of the ledger.
type ToolExecution struct {
	// Limits 是资源限额快照（P24）：请求值 + 实际生效情况（本机不支持某项时如实标注）。
	Limits      json.RawMessage `json:"limits,omitempty"`
	ID          int64           `json:"id"`
	TaskID      string          `json:"task_id"`
	IntentID    int64           `json:"intent_id,omitempty"`
	Worker      string          `json:"worker"`
	Tool        string          `json:"tool"`
	Command     string          `json:"command"`
	Description string          `json:"description,omitempty"`
	Status      string          `json:"status"`
	ExitCode    *int            `json:"exit_code,omitempty"`
	OutputPath  string          `json:"output_path"`
	OutputBytes int64           `json:"output_bytes"`
	PartialTail string          `json:"partial_tail,omitempty"`
	CancelNote  string          `json:"cancel_note,omitempty"`
	Error       string          `json:"error,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	EndedAt     *time.Time      `json:"ended_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

const executionCols = `id, task_id, COALESCE(intent_id,0), worker, tool, command, COALESCE(description,''), status,
	COALESCE(exit_code,-1), output_path, output_bytes, partial_tail, cancel_note, error,
	started_at, ended_at, created_at, limits`

func scanExecution(sc interface{ Scan(...any) error }) (*ToolExecution, error) {
	var e ToolExecution
	var exit int
	if err := sc.Scan(&e.ID, &e.TaskID, &e.IntentID, &e.Worker, &e.Tool, &e.Command, &e.Description, &e.Status,
		&exit, &e.OutputPath, &e.OutputBytes, &e.PartialTail, &e.CancelNote, &e.Error,
		&e.StartedAt, &e.EndedAt, &e.CreatedAt, &e.Limits); err != nil {
		return nil, err
	}
	if exit >= 0 {
		e.ExitCode = &exit
	}
	return &e, nil
}

// CreateExecution inserts a queued row and returns its id.
func (d *DB) CreateExecution(e *ToolExecution) (int64, error) {
	var intentID any
	if e.IntentID > 0 {
		intentID = e.IntentID
	}
	var id int64
	err := d.QueryRow(`INSERT INTO tool_executions(task_id, intent_id, worker, tool, command, description, status, output_path)
VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		e.TaskID, intentID, e.Worker, e.Tool, e.Command, e.Description, ExecQueued, e.OutputPath).Scan(&id)
	return id, err
}

// SetExecutionOutputPath records the absolute output-file path (known only after the
// row id exists, so Submit creates the row first and stamps the path right after).
func (d *DB) SetExecutionOutputPath(id int64, path string) error {
	_, err := d.Exec(`UPDATE tool_executions SET output_path=$1 WHERE id=$2`, path, id)
	return err
}

// MarkExecutionRunning stamps the start time and flips the row to running.
func (d *DB) MarkExecutionRunning(id int64) error {
	_, err := d.Exec(`UPDATE tool_executions SET status=$1, started_at=now() WHERE id=$2 AND status=$3`,
		ExecRunning, id, ExecQueued)
	return err
}

// FinishExecution writes a terminal status. exitCode<0 means "no exit code recorded".
func (d *DB) FinishExecution(id int64, status string, exitCode int, outputBytes int64, tail, errMsg string) error {
	if !ExecutionTerminal(status) {
		return nil
	}
	var exit any
	if exitCode >= 0 {
		exit = exitCode
	}
	_, err := d.Exec(`UPDATE tool_executions
SET status=$1, exit_code=$2, output_bytes=$3, partial_tail=$4, error=$5, ended_at=now()
WHERE id=$6`, status, exit, outputBytes, tail, errMsg, id)
	return err
}

// TouchExecutionTail persists the current partial tail/bytes while still running.
func (d *DB) TouchExecutionTail(id int64, tail string, outputBytes int64) error {
	_, err := d.Exec(`UPDATE tool_executions SET partial_tail=$1, output_bytes=$2 WHERE id=$3 AND status IN ($4,$5)`,
		tail, outputBytes, id, ExecQueued, ExecRunning)
	return err
}

// SetExecutionCancelNote records WHY an execution was cancelled (human/task reason).
func (d *DB) SetExecutionCancelNote(id int64, note string) error {
	_, err := d.Exec(`UPDATE tool_executions SET cancel_note=$1 WHERE id=$2`, note, id)
	return err
}

// SetExecutionLimits 落一次执行的限额快照（提交时写一次，之后不变）。
func (d *DB) SetExecutionLimits(id int64, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	_, err := d.Exec(`UPDATE tool_executions SET limits=$2, updated_at=now() WHERE id=$1`, id, string(raw))
	return err
}

// GetExecution reads one row (nil when absent).
func (d *DB) GetExecution(id int64) (*ToolExecution, error) {
	e, err := scanExecution(d.QueryRow(`SELECT `+executionCols+` FROM tool_executions WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return e, err
}

// ListActiveExecutionsByIntent returns the in-flight executions of one intent.
func (d *DB) ListActiveExecutionsByIntent(intentID int64) ([]*ToolExecution, error) {
	rows, err := d.Query(`SELECT `+executionCols+` FROM tool_executions
WHERE intent_id=$1 AND status IN ($2,$3) ORDER BY id`, intentID, ExecQueued, ExecRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ToolExecution
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListExecutionsByIntent returns every execution of one intent, newest first
// (收尾门/negative 证据门/报告警告共用).
func (d *DB) ListExecutionsByIntent(intentID int64, limit int) ([]*ToolExecution, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.Query(`SELECT `+executionCols+` FROM tool_executions
WHERE intent_id=$1 ORDER BY id DESC LIMIT $2`, intentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ToolExecution
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountRanExecutions counts the intent's executions that actually ran to a
// terminal state (whatever the verdict) — the negative-evidence gate's "有过真实
// 执行记录" criterion. queued/running rows are excluded: 还没跑完的不是证据。
func (d *DB) CountRanExecutions(intentID int64) (int64, error) {
	var n int64
	err := d.QueryRow(`SELECT count(*) FROM tool_executions
WHERE intent_id=$1 AND status NOT IN ($2,$3)`, intentID, ExecQueued, ExecRunning).Scan(&n)
	return n, err
}

// ListExecutionsByTask returns recent executions of one task, newest first.
func (d *DB) ListExecutionsByTask(taskID string, limit int) ([]*ToolExecution, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.Query(`SELECT `+executionCols+` FROM tool_executions
WHERE task_id=$1 ORDER BY id DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ToolExecution
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListActiveExecutions of one task (for停任务时的 CancelAll 与收尾门).
func (d *DB) ListActiveExecutions(taskID string) ([]*ToolExecution, error) {
	rows, err := d.Query(`SELECT `+executionCols+` FROM tool_executions
WHERE task_id=$1 AND status IN ($2,$3) ORDER BY id`, taskID, ExecQueued, ExecRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ToolExecution
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReconcileOrphanedExecutions marks every in-flight row whose id is NOT in live as
// orphaned — those are executions whose hosting process died with a previous
// server instance (or a lost goroutine). Deliberately not "failed": nothing was
// observed about their outcome.
func (d *DB) ReconcileOrphanedExecutions(live map[int64]bool, msg string) (int64, error) {
	rows, err := d.Query(`SELECT id FROM tool_executions WHERE status IN ($1,$2)`, ExecQueued, ExecRunning)
	if err != nil {
		return 0, err
	}
	var orphans []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if !live[id] {
			orphans = append(orphans, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var n int64
	for _, id := range orphans {
		res, err := d.Exec(`UPDATE tool_executions
SET status=$1, error=$5, ended_at=now()
WHERE id=$2 AND status IN ($3,$4)`, ExecOrphaned, id, ExecQueued, ExecRunning, msg)
		if err != nil {
			return n, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			n++
		}
	}
	return n, nil
}
