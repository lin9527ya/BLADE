package db

import (
	"context"
	"time"
)

// IntelQuery 是一次情报源查询的台账行（P21）。
type IntelQuery struct {
	ID        int64     `json:"id"`
	Provider  string    `json:"provider"`
	Expr      string    `json:"expr"`
	TotalHits int       `json:"total_hits"`
	Returned  int       `json:"returned"`
	TaskID    string    `json:"task_id,omitempty"`
	IntentID  int64     `json:"intent_id,omitempty"`
	Worker    string    `json:"worker,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// RecordIntelQuery 落一条查询台账（配额计数与审计共用）。失败也要记（error 非空），
// 因为"失败的调用同样消耗了额度/次数"，只记成功的账会低估用量。
func (d *DB) RecordIntelQuery(ctx context.Context, q IntelQuery) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO intel_queries(provider, expr, total_hits, returned, task_id, intent_id, worker, error)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		q.Provider, utf8Clean(q.Expr), q.TotalHits, q.Returned, q.TaskID, q.IntentID, q.Worker, utf8Clean(q.Error))
	return err
}

// CountIntelQueriesSince 数某个时间点之后的查询次数（每日配额用；失败调用也计入）。
func (d *DB) CountIntelQueriesSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM intel_queries WHERE created_at >= $1`, since).Scan(&n)
	return n, err
}

// ListIntelQueries 最近的查询台账（新的在前），供设置页展示"今日用量"和排查。
func (d *DB) ListIntelQueries(ctx context.Context, limit int) ([]IntelQuery, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, `
SELECT id, provider, expr, total_hits, returned, COALESCE(task_id,''), intent_id,
       COALESCE(worker,''), COALESCE(error,''), created_at
FROM intel_queries ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IntelQuery{}
	for rows.Next() {
		var q IntelQuery
		if err := rows.Scan(&q.ID, &q.Provider, &q.Expr, &q.TotalHits, &q.Returned, &q.TaskID,
			&q.IntentID, &q.Worker, &q.Error, &q.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
