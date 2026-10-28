package db

import (
	"context"
	"database/sql"
)

// DashboardTaskTokenProfile is the dashboard's whole-task token aggregate for
// one LLM profile. ProfileID is nil for tasks using the default profile.
type DashboardTaskTokenProfile struct {
	ProfileID    *int64 `json:"profile_id"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheRead    int64  `json:"cache_read_tokens"`
	CacheWrite   int64  `json:"cache_write_tokens"`
	TaskCount    int64  `json:"task_count"`
}

// DashboardTaskTokenDay keeps the legacy dashboard's per-creation-day buckets.
// A task's whole token total is attributed to the day the task was created,
// matching the former frontend calculation.
type DashboardTaskTokenDay struct {
	Date         string `json:"date"`
	ProfileID    *int64 `json:"profile_id"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheRead    int64  `json:"cache_read_tokens"`
	CacheWrite   int64  `json:"cache_write_tokens"`
}

type DashboardTaskTokens struct {
	ByProfile []DashboardTaskTokenProfile `json:"by_profile"`
	Daily     []DashboardTaskTokenDay     `json:"daily"`
}

func nullableInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	id := v.Int64
	return &id
}

// DashboardTaskTokens aggregates whole-task token usage without building a DTO
// for every task. It replaces the dashboard's former full task-list fetch.
func (d *DB) DashboardTaskTokens() (DashboardTaskTokens, error) {
	return d.DashboardTaskTokensContext(context.Background())
}

func (d *DB) DashboardTaskTokensContext(ctx context.Context) (DashboardTaskTokens, error) {
	out := DashboardTaskTokens{
		ByProfile: []DashboardTaskTokenProfile{},
		Daily:     []DashboardTaskTokenDay{},
	}
	rows, err := d.QueryContext(ctx, `
SELECT t.llm_profile_id,
       COALESCE(SUM(a.input_tokens),0)::bigint,
       COALESCE(SUM(a.output_tokens),0)::bigint,
       COALESCE(SUM(a.cache_read_tokens),0)::bigint,
       COALESCE(SUM(a.cache_write_tokens),0)::bigint,
       COUNT(DISTINCT t.id)::bigint
FROM tasks t
LEFT JOIN activity a ON a.exploration_id=t.exploration_id AND a.kind='result'
WHERE t.deleted_at IS NULL
GROUP BY t.llm_profile_id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var profile sql.NullInt64
		var row DashboardTaskTokenProfile
		if err := rows.Scan(&profile, &row.InputTokens, &row.OutputTokens,
			&row.CacheRead, &row.CacheWrite, &row.TaskCount); err != nil {
			rows.Close()
			return out, err
		}
		row.ProfileID = nullableInt64(profile)
		out.ByProfile = append(out.ByProfile, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	rows, err = d.Query(`
SELECT to_char(t.created_at, 'YYYY-MM-DD'), t.llm_profile_id,
       COALESCE(SUM(a.input_tokens),0)::bigint,
       COALESCE(SUM(a.output_tokens),0)::bigint,
       COALESCE(SUM(a.cache_read_tokens),0)::bigint,
       COALESCE(SUM(a.cache_write_tokens),0)::bigint
FROM tasks t
LEFT JOIN activity a ON a.exploration_id=t.exploration_id AND a.kind='result'
WHERE t.deleted_at IS NULL
GROUP BY 1,2
ORDER BY 1,2`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var profile sql.NullInt64
		var row DashboardTaskTokenDay
		if err := rows.Scan(&row.Date, &profile, &row.InputTokens, &row.OutputTokens,
			&row.CacheRead, &row.CacheWrite); err != nil {
			return out, err
		}
		row.ProfileID = nullableInt64(profile)
		out.Daily = append(out.Daily, row)
	}
	return out, rows.Err()
}
