package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// TaskSchedule 是一条计划任务（P22）：到点按 task_spec 建一个任务。
type TaskSchedule struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Enabled   bool            `json:"enabled"`
	TaskSpec  json.RawMessage `json:"task_spec"`
	Frequency json.RawMessage `json:"frequency"`
	NextRunAt *time.Time      `json:"next_run_at,omitempty"`
	LastRunAt *time.Time      `json:"last_run_at,omitempty"`
	// LastTaskID 是最近一次触发建出来的任务 id（链接用）；空 = 还没跑过或建失败。
	LastTaskID string    `json:"last_task_id,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const scheduleCols = `id, name, enabled, task_spec, frequency, next_run_at, last_run_at,
COALESCE(last_task_id,''), COALESCE(last_error,''), created_at, updated_at`

func scanSchedule(sc interface{ Scan(...any) error }) (*TaskSchedule, error) {
	var s TaskSchedule
	if err := sc.Scan(&s.ID, &s.Name, &s.Enabled, &s.TaskSpec, &s.Frequency, &s.NextRunAt,
		&s.LastRunAt, &s.LastTaskID, &s.LastError, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateTaskSchedule 落一条计划任务。next 由调用方用 sched.Frequency.Next 算好
// （禁用时传 nil：不排期）。
func (d *DB) CreateTaskSchedule(name string, enabled bool, spec, freq json.RawMessage, next *time.Time) (*TaskSchedule, error) {
	s, err := scanSchedule(d.QueryRow(`
INSERT INTO task_schedules(name, enabled, task_spec, frequency, next_run_at)
VALUES ($1,$2,$3,$4,$5) RETURNING `+scheduleCols, name, enabled, spec, freq, next))
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ListTaskSchedules 列出全部计划任务（新的在前）。
func (d *DB) ListTaskSchedules() ([]*TaskSchedule, error) {
	rows, err := d.Query(`SELECT ` + scheduleCols + ` FROM task_schedules ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TaskSchedule{}
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetTaskSchedule 按 id 取一条（不存在 → nil, nil）。
func (d *DB) GetTaskSchedule(id int64) (*TaskSchedule, error) {
	s, err := scanSchedule(d.QueryRow(`SELECT `+scheduleCols+` FROM task_schedules WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

// UpdateTaskSchedule 改一条计划任务：nil 字段 = 不改。next 非 nil 时一并写 next_run_at
// （改频率/重新启用时由调用方算好；禁用传 nil 表示清空排期）。
func (d *DB) UpdateTaskSchedule(id int64, name *string, enabled *bool, spec, freq json.RawMessage, next *time.Time, clearNext bool) (*TaskSchedule, error) {
	s, err := scanSchedule(d.QueryRow(`
UPDATE task_schedules SET
  name        = COALESCE($2, name),
  enabled     = COALESCE($3, enabled),
  task_spec   = COALESCE($4, task_spec),
  frequency   = COALESCE($5, frequency),
  next_run_at = CASE WHEN $6 THEN NULL ELSE COALESCE($7, next_run_at) END,
  updated_at  = now()
WHERE id=$1 RETURNING `+scheduleCols, id, name, enabled, spec, freq, clearNext, next))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

// DeleteTaskSchedule 删一条（幂等：不存在返回 false）。
func (d *DB) DeleteTaskSchedule(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM task_schedules WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DueTaskSchedules 取到点该跑的计划任务（只取启用的；next_run_at 为空的跳过——
// 那是"禁用着"或"还没排期"的状态）。
func (d *DB) DueTaskSchedules(now time.Time, limit int) ([]*TaskSchedule, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := d.Query(`SELECT `+scheduleCols+`
FROM task_schedules WHERE enabled AND next_run_at IS NOT NULL AND next_run_at <= $1
ORDER BY next_run_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TaskSchedule{}
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AdvanceTaskSchedule 原子把 next_run_at 从 from 推到 to：**这是"这一次归我跑"的唯一
// 凭据**（CAS）。返回 false = 别的 tick/实例已经推过了，本次不要建任务。
func (d *DB) AdvanceTaskSchedule(id int64, from, to time.Time) (bool, error) {
	res, err := d.Exec(`UPDATE task_schedules SET next_run_at=$3, updated_at=now()
WHERE id=$1 AND next_run_at=$2`, id, from, to)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FinishScheduleRun 记录一次触发的结果：建成功的任务 id，或失败原因（人话）。
// 失败也要落 last_run_at——"跑过但没建成"与"根本没跑"是两种不同的状态。
func (d *DB) FinishScheduleRun(id int64, runAt time.Time, taskID, errMsg string) error {
	_, err := d.Exec(`UPDATE task_schedules SET last_run_at=$2, last_task_id=$3, last_error=$4, updated_at=now()
WHERE id=$1`, id, runAt, taskID, utf8Clean(errMsg))
	return err
}

// SetScheduleNextRun 只改排期（启停与"立即运行"后重排用）。
func (d *DB) SetScheduleNextRun(id int64, next *time.Time) error {
	_, err := d.Exec(`UPDATE task_schedules SET next_run_at=$2, updated_at=now() WHERE id=$1`, id, next)
	return err
}

// ErrTaskScheduleNotFound 供 HTTP 层判断 404。
var ErrTaskScheduleNotFound = fmt.Errorf("task schedule not found")
