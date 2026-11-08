package db

import (
	"context"
	"time"
)

// UsageDiagnostic is always-on metering metadata, independent of body recording.
type UsageDiagnostic struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	TaskID     string    `json:"task_id"`
	Worker     string    `json:"worker"`
	Session    string    `json:"session"`
	Model      string    `json:"model"`
	Profile    string    `json:"profile"`
	Input      int64     `json:"input"`
	Output     int64     `json:"output"`
	CacheRead  int64     `json:"cache_read"`
	CacheWrite int64     `json:"cache_write"`
	Status     string    `json:"status"`
	LatencyMS  int       `json:"latency_ms"`
	FirstCall  bool      `json:"first_call"`
}

func (d *DB) UsageDiagnostics(ctx context.Context, task, worker, cache, session string, before int64, limit int) ([]UsageDiagnostic, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `SELECT u.id,u.ts,COALESCE(u.task_id,''),COALESCE(u.worker,''),COALESCE(u.session_id,''),
        COALESCE(u.model,''),COALESCE(u.profile_name,''),u.input_tokens,u.output_tokens,u.cache_read,u.cache_write,
        COALESCE(u.status,''),COALESCE(u.latency_ms,0),
        COALESCE(u.session_id,'')<>'' AND NOT EXISTS(SELECT 1 FROM llm_usage p WHERE p.worker IS NOT DISTINCT FROM u.worker AND p.session_id=u.session_id AND p.task_id IS NOT DISTINCT FROM u.task_id AND p.id<u.id)
        FROM llm_usage u WHERE u.ts>=now()-interval '24 hours' AND ($1='' OR u.task_id=$1)
        AND ($2='' OR u.worker=$2) AND ($6='' OR u.session_id=$6) AND ($4::bigint=0 OR u.id<$4)
        AND ($3='' OR ($3='zero' AND u.input_tokens>0 AND u.cache_read=0)
          OR ($3='low' AND u.input_tokens>0 AND u.cache_read::bigint*2<u.input_tokens))
        ORDER BY u.id DESC LIMIT $5`, task, worker, cache, before, limit, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UsageDiagnostic, 0)
	for rows.Next() {
		var row UsageDiagnostic
		if err := rows.Scan(&row.ID, &row.At, &row.TaskID, &row.Worker, &row.Session, &row.Model, &row.Profile, &row.Input, &row.Output, &row.CacheRead, &row.CacheWrite, &row.Status, &row.LatencyMS, &row.FirstCall); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
