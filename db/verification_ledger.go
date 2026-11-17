package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// VerificationLedgerFilter selects read-only execution records for the
// verification/exploitation ledger. It intentionally reads the existing
// tool_executions source of truth instead of creating a second attempt table.
type VerificationLedgerFilter struct {
	TaskID string
	Tool   string
	Status string
	Query  string
	Limit  int
	Offset int
}

type VerificationLedgerRow struct {
	ID          int64      `json:"id"`
	TaskID      string     `json:"task_id"`
	TaskName    string     `json:"task_name,omitempty"`
	IntentID    int64      `json:"intent_id,omitempty"`
	Worker      string     `json:"worker,omitempty"`
	Tool        string     `json:"tool"`
	Command     string     `json:"command"`
	Description string     `json:"description,omitempty"`
	Status      string     `json:"status"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	PartialTail string     `json:"partial_tail,omitempty"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
}

func (d *DB) ListVerificationLedger(f VerificationLedgerFilter) ([]*VerificationLedgerRow, int, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := "1=1"
	args := []any{}
	if v := strings.TrimSpace(f.TaskID); v != "" {
		args = append(args, v)
		where += fmt.Sprintf(" AND e.task_id = $%d", len(args))
	}
	if v := strings.TrimSpace(f.Tool); v != "" {
		args = append(args, v)
		where += fmt.Sprintf(" AND e.tool = $%d", len(args))
	}
	if v := strings.TrimSpace(f.Status); v != "" {
		args = append(args, v)
		where += fmt.Sprintf(" AND e.status = $%d", len(args))
	}
	if v := strings.TrimSpace(f.Query); v != "" {
		args = append(args, "%"+v+"%")
		where += fmt.Sprintf(" AND (e.command ILIKE $%d OR COALESCE(e.description,'') ILIKE $%d OR COALESCE(e.partial_tail,'') ILIKE $%d)", len(args), len(args), len(args))
	}
	var total int
	if err := d.QueryRow(`SELECT count(*) FROM tool_executions e WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(`SELECT e.id, e.task_id, COALESCE(NULLIF(t.name,''), NULLIF(t.goal,''), t.description, ''),
       COALESCE(e.intent_id,0), COALESCE(e.worker,''), e.tool, e.command, COALESCE(e.description,''),
       e.status, e.exit_code, COALESCE(e.partial_tail,''), COALESCE(e.error,''),
       e.created_at, e.started_at, e.ended_at
FROM tool_executions e
LEFT JOIN tasks t ON t.id::text = e.task_id
WHERE `+where+`
ORDER BY e.id DESC LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*VerificationLedgerRow{}
	for rows.Next() {
		var row VerificationLedgerRow
		var exit sql.NullInt64
		if err := rows.Scan(&row.ID, &row.TaskID, &row.TaskName, &row.IntentID, &row.Worker,
			&row.Tool, &row.Command, &row.Description, &row.Status, &exit, &row.PartialTail,
			&row.Error, &row.CreatedAt, &row.StartedAt, &row.EndedAt); err != nil {
			return nil, 0, err
		}
		if exit.Valid {
			v := int(exit.Int64)
			row.ExitCode = &v
		}
		out = append(out, &row)
	}
	return out, total, rows.Err()
}
