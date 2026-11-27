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

// DashboardOpsSummary aggregates cross-task operational posture (loot, artifacts,
// schedules, and persisted mobile devices) in lightweight SQL queries.
type DashboardOpsSummary struct {
	FootholdsTotal     int64  `json:"footholds_total"`
	FootholdsActive    int64  `json:"footholds_active"`
	CredentialsTotal   int64  `json:"credentials_total"`
	ArtifactsUncleaned int64  `json:"artifacts_uncleaned"`
	ArtifactsUnknown   int64  `json:"artifacts_unknown"`
	SchedulesTotal     int64  `json:"schedules_total"`
	SchedulesEnabled   int64  `json:"schedules_enabled"`
	SchedulesError     int64  `json:"schedules_error"`
	NextScheduleName   string `json:"next_schedule_name,omitempty"`
	NextScheduleAction string `json:"next_schedule_action,omitempty"`
	NextScheduleAtUnix int64  `json:"next_schedule_at_unix,omitempty"`
	MobileDevicesTotal int64  `json:"mobile_devices_total"`
	MobileDevicesBound int64  `json:"mobile_devices_bound"`
	MobileDevicesRoot  int64  `json:"mobile_devices_rooted"`
}

// DashboardOpsSummaryContext queries lightweight operational counts for the dashboard.
func (d *DB) DashboardOpsSummaryContext(ctx context.Context) (DashboardOpsSummary, error) {
	var out DashboardOpsSummary
	rows, err := d.QueryContext(ctx, `
SELECT n.kind, n.state, COUNT(*)::bigint
FROM exploration_nodes n
JOIN explorations e ON e.id = n.exploration_id
JOIN tasks t ON t.exploration_id = e.id
WHERE t.deleted_at IS NULL
  AND n.kind IN ($1, $2, $3)
GROUP BY n.kind, n.state`, KindFoothold, KindCredential, KindArtifact)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind, state string
		var cnt int64
		if err := rows.Scan(&kind, &state, &cnt); err != nil {
			rows.Close()
			return out, err
		}
		switch kind {
		case KindFoothold:
			out.FootholdsTotal += cnt
			if state == StateLootActive {
				out.FootholdsActive += cnt
			}
		case KindCredential:
			out.CredentialsTotal += cnt
		case KindArtifact:
			switch state {
			case StateArtifactActive:
				out.ArtifactsUncleaned += cnt
			case StateArtifactUnknown:
				out.ArtifactsUnknown += cnt
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	if err := d.QueryRowContext(ctx, `
SELECT COUNT(*)::bigint,
       COUNT(*) FILTER (WHERE enabled)::bigint,
       COUNT(*) FILTER (WHERE COALESCE(last_error, '') <> '')::bigint
FROM task_schedules`).Scan(&out.SchedulesTotal, &out.SchedulesEnabled, &out.SchedulesError); err != nil {
		return out, err
	}

	var nextName, nextAction string
	var nextUnix sql.NullInt64
	if err := d.QueryRowContext(ctx, `
SELECT name, COALESCE(action, 'create'), EXTRACT(EPOCH FROM next_run_at)::bigint
FROM task_schedules
WHERE enabled AND next_run_at IS NOT NULL
ORDER BY next_run_at ASC
LIMIT 1`).Scan(&nextName, &nextAction, &nextUnix); err != nil && err != sql.ErrNoRows {
		return out, err
	} else if err == nil && nextUnix.Valid {
		out.NextScheduleName = nextName
		out.NextScheduleAction = nextAction
		out.NextScheduleAtUnix = nextUnix.Int64
	}

	if err := d.QueryRowContext(ctx, `
SELECT COUNT(*)::bigint,
       COUNT(*) FILTER (WHERE bound_task_id IS NOT NULL)::bigint,
       COUNT(*) FILTER (WHERE rooted)::bigint
FROM mobile_devices`).Scan(&out.MobileDevicesTotal, &out.MobileDevicesBound, &out.MobileDevicesRoot); err != nil {
		return out, err
	}
	return out, nil
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
