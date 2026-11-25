package db

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// TaskStorageStat summarizes database rows and payload sizes attributable to
// a single task (and its associated exploration). Bytes reflect row payload
// column sizes (pg_column_size), excluding table/index page bloat.
type TaskStorageStat struct {
	ActivityRows       int64
	ActivityBytes      int64
	SideQuestionRows   int64
	SideQuestionBytes  int64
	LLMRecordRows      int64
	LLMRecordBytes     int64
	LLMRecordBodyRows  int64
	LLMRecordBodyBytes int64
	LLMUsageRows       int64
	LLMUsageBytes      int64
	ToolExecutionRows  int64
	ToolExecutionBytes int64
	BlackboardLogRows  int64
	BlackboardLogBytes int64
}

// TaskCleanScopes controls which task-scoped auxiliary datasets to purge while
// always preserving core task state, exploration graph, findings, and assets.
type TaskCleanScopes struct {
	Activity        bool
	SideQuestions   bool
	LLMRecordBodies bool
	LLMRecords      bool
	LLMUsage        bool
	ToolExecutions  bool
	BlackboardLogs  bool
}

// TaskCleanResult reports rows affected in PostgreSQL per task cleanup call.
type TaskCleanResult struct {
	ActivityDeleted        int64
	SideQuestionsDeleted   int64
	LLMRecordBodiesCleared int64
	LLMRecordsDeleted      int64
	LLMUsageDeleted        int64
	ToolExecutionsDeleted  int64
	BlackboardLogsDeleted  int64
}

// TaskStorageStats aggregates per-task database usage in one scan per table so
// storage reporting stays O(tables) regardless of task count.
func (d *DB) TaskStorageStats(
	ctx context.Context, taskIDs []int64, expByTask map[int64]int64,
) (map[int64]TaskStorageStat, error) {
	out := make(map[int64]TaskStorageStat, len(taskIDs))
	if len(taskIDs) == 0 {
		return out, nil
	}
	taskTexts := make([]string, 0, len(taskIDs))
	seenTask := make(map[int64]bool, len(taskIDs))
	expIDs := make([]int64, 0, len(expByTask))
	tasksByExp := make(map[int64][]int64, len(expByTask))
	seenExp := make(map[int64]bool, len(expByTask))
	for _, tid := range taskIDs {
		if tid <= 0 || seenTask[tid] {
			continue
		}
		seenTask[tid] = true
		out[tid] = TaskStorageStat{}
		taskTexts = append(taskTexts, strconv.FormatInt(tid, 10))
		if eid := expByTask[tid]; eid > 0 {
			tasksByExp[eid] = append(tasksByExp[eid], tid)
			if !seenExp[eid] {
				seenExp[eid] = true
				expIDs = append(expIDs, eid)
			}
		}
	}
	if len(taskTexts) == 0 {
		return out, nil
	}

	if len(expIDs) > 0 {
		rows, err := d.QueryContext(ctx, `
SELECT exploration_id, count(*), COALESCE(sum(pg_column_size(a.*)),0)
FROM activity a
WHERE exploration_id = ANY($1::bigint[])
GROUP BY exploration_id`, expIDs)
		if err != nil {
			return nil, fmt.Errorf("统计任务 activity: %w", err)
		}
		for rows.Next() {
			var eid, cnt, sz int64
			if err := rows.Scan(&eid, &cnt, &sz); err != nil {
				_ = rows.Close()
				return nil, err
			}
			for _, tid := range tasksByExp[eid] {
				st := out[tid]
				st.ActivityRows += cnt
				st.ActivityBytes += sz
				out[tid] = st
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()

		for _, tbl := range []string{"blackboard_events", "blackboard_operations"} {
			rows, err := d.QueryContext(ctx, `
SELECT exploration_id, count(*), COALESCE(sum(pg_column_size(x.*)),0)
FROM `+tbl+` x
WHERE exploration_id = ANY($1::bigint[])
GROUP BY exploration_id`, expIDs)
			if err != nil {
				return nil, fmt.Errorf("统计任务 %s: %w", tbl, err)
			}
			for rows.Next() {
				var eid, cnt, sz int64
				if err := rows.Scan(&eid, &cnt, &sz); err != nil {
					_ = rows.Close()
					return nil, err
				}
				for _, tid := range tasksByExp[eid] {
					st := out[tid]
					st.BlackboardLogRows += cnt
					st.BlackboardLogBytes += sz
					out[tid] = st
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return nil, err
			}
			_ = rows.Close()
		}
	}

	// side_question_sessions enforces task_id NOT NULL for all task-scoped rows.
	rows, err := d.QueryContext(ctx, `
SELECT task_id, count(*), COALESCE(sum(pg_column_size(s.*)),0)
FROM side_question_sessions s
WHERE task_id = ANY($1::bigint[])
GROUP BY task_id`, taskIDs)
	if err != nil {
		return nil, fmt.Errorf("统计任务 side_question_sessions: %w", err)
	}
	for rows.Next() {
		var tid, cnt, sz int64
		if err := rows.Scan(&tid, &cnt, &sz); err != nil {
			_ = rows.Close()
			return nil, err
		}
		st := out[tid]
		st.SideQuestionRows = cnt
		st.SideQuestionBytes = sz
		out[tid] = st
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	rows, err = d.QueryContext(ctx, `
SELECT task_id,
       count(*),
       COALESCE(sum(pg_column_size(x.*)),0),
       count(*) FILTER (WHERE request_body IS NOT NULL OR response_body IS NOT NULL OR raw_request IS NOT NULL OR raw_response IS NOT NULL),
       COALESCE(sum(COALESCE(pg_column_size(x.request_body),0) + COALESCE(pg_column_size(x.response_body),0)
                  + COALESCE(pg_column_size(x.raw_request),0) + COALESCE(pg_column_size(x.raw_response),0))
                FILTER (WHERE request_body IS NOT NULL OR response_body IS NOT NULL OR raw_request IS NOT NULL OR raw_response IS NOT NULL), 0)
FROM llm_records x
WHERE task_id = ANY($1::text[])
GROUP BY task_id`, taskTexts)
	if err != nil {
		return nil, fmt.Errorf("统计任务 llm_records: %w", err)
	}
	for rows.Next() {
		var key string
		var cnt, sz, bcnt, bsz int64
		if err := rows.Scan(&key, &cnt, &sz, &bcnt, &bsz); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if tid, perr := strconv.ParseInt(key, 10, 64); perr == nil {
			st := out[tid]
			st.LLMRecordRows = cnt
			st.LLMRecordBytes = sz
			st.LLMRecordBodyRows = bcnt
			st.LLMRecordBodyBytes = bsz
			out[tid] = st
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	rows, err = d.QueryContext(ctx, `
SELECT task_id, count(*), COALESCE(sum(pg_column_size(x.*)),0)
FROM llm_usage x
WHERE task_id = ANY($1::text[])
GROUP BY task_id`, taskTexts)
	if err != nil {
		return nil, fmt.Errorf("统计任务 llm_usage: %w", err)
	}
	for rows.Next() {
		var key string
		var cnt, sz int64
		if err := rows.Scan(&key, &cnt, &sz); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if tid, perr := strconv.ParseInt(key, 10, 64); perr == nil {
			st := out[tid]
			st.LLMUsageRows = cnt
			st.LLMUsageBytes = sz
			out[tid] = st
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	rows, err = d.QueryContext(ctx, `
SELECT task_id, count(*), COALESCE(sum(pg_column_size(x.*)),0)
FROM tool_executions x
WHERE task_id = ANY($1::text[]) AND status NOT IN ('queued','running')
GROUP BY task_id`, taskTexts)
	if err != nil {
		return nil, fmt.Errorf("统计任务 tool_executions: %w", err)
	}
	for rows.Next() {
		var key string
		var cnt, sz int64
		if err := rows.Scan(&key, &cnt, &sz); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if tid, perr := strconv.ParseInt(key, 10, 64); perr == nil {
			st := out[tid]
			st.ToolExecutionRows = cnt
			st.ToolExecutionBytes = sz
			out[tid] = st
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	return out, nil
}

// PurgeTaskScopedData removes selected auxiliary database datasets belonging to
// one task without deleting the task, exploration graph, findings, or assets.
func (d *DB) PurgeTaskScopedData(ctx context.Context, taskID, expID int64, scopes TaskCleanScopes) (TaskCleanResult, error) {
	var res TaskCleanResult
	taskText := strconv.FormatInt(taskID, 10)
	if scopes.Activity && expID > 0 {
		n, err := d.purgeBatched(ctx, "activity", "id", "created_at", "exploration_id = $1", []any{expID}, 0)
		res.ActivityDeleted = n
		if err != nil {
			return res, err
		}
	}
	if scopes.SideQuestions && taskID > 0 {
		n, err := d.purgeBatched(
			ctx, "side_question_sessions", "session_key", "updated_at",
			"task_id = $1 AND session_key NOT IN (SELECT r.session_key FROM side_question_requests r WHERE r.status='running')",
			[]any{taskID}, 0,
		)
		res.SideQuestionsDeleted = n
		if err != nil {
			return res, err
		}
	}
	if scopes.LLMRecords && taskID > 0 {
		n, err := d.purgeBatched(ctx, "llm_records", "id", "ts", "task_id = $1", []any{taskText}, 0)
		res.LLMRecordsDeleted = n
		if err != nil {
			return res, err
		}
	} else if scopes.LLMRecordBodies && taskID > 0 {
		n, err := d.stripTaskLLMRecordBodies(ctx, taskText)
		res.LLMRecordBodiesCleared = n
		if err != nil {
			return res, err
		}
	}
	if scopes.LLMUsage && taskID > 0 {
		n, err := d.purgeBatched(ctx, "llm_usage", "id", "ts", "task_id = $1", []any{taskText}, 0)
		res.LLMUsageDeleted = n
		if err != nil {
			return res, err
		}
	}
	if scopes.ToolExecutions && taskID > 0 {
		n, err := d.purgeBatched(ctx, "tool_executions", "id", "created_at", "task_id = $1 AND status NOT IN ('queued','running')", []any{taskText}, 0)
		res.ToolExecutionsDeleted = n
		if err != nil {
			return res, err
		}
	}
	if scopes.BlackboardLogs && expID > 0 {
		n1, err := d.purgeBatched(ctx, "blackboard_events", "ctid", "created_at", "exploration_id = $1", []any{expID}, 0)
		res.BlackboardLogsDeleted += n1
		if err != nil {
			return res, err
		}
		n2, err := d.purgeBatched(ctx, "blackboard_operations", "id", "created_at", "exploration_id = $1", []any{expID}, 0)
		res.BlackboardLogsDeleted += n2
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

func (d *DB) stripTaskLLMRecordBodies(ctx context.Context, taskText string) (int64, error) {
	var total int64
	for {
		res, err := d.ExecContext(ctx, `
UPDATE llm_records SET request_body=NULL, response_body=NULL, raw_request=NULL, raw_response=NULL
WHERE id IN (
    SELECT id FROM llm_records
    WHERE task_id=$1
      AND (request_body IS NOT NULL OR response_body IS NOT NULL
           OR raw_request IS NOT NULL OR raw_response IS NOT NULL)
    ORDER BY ts LIMIT $2
)`, taskText, defaultRetentionBatch)
		if err != nil {
			return total, fmt.Errorf("清空任务 llm_records 载荷: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(defaultRetentionBatch) {
			return total, nil
		}
	}
}

var vacuumAllowedTables = []string{
	"server_logs",
	"side_question_sessions",
	"side_question_requests",
	"activity",
	"llm_usage",
	"llm_records",
	"tool_executions",
	"credential_attempts",
	"notification_events",
	"conversation_activities",
	"intercept_pending",
	"audit_events",
	"blackboard_events",
	"blackboard_operations",
	"task_timeout_events",
	"aggregate_deliverable_requests",
}

// VacuumStorageTables runs VACUUM (or VACUUM FULL to return free heap/index
// pages back to the OS) against a strict whitelist of retention tables.
func (d *DB) VacuumStorageTables(ctx context.Context, requested []string, full bool) ([]string, error) {
	allowed := make(map[string]bool, len(vacuumAllowedTables))
	for _, t := range vacuumAllowedTables {
		allowed[t] = true
	}
	var targets []string
	if len(requested) == 0 {
		targets = append([]string(nil), vacuumAllowedTables...)
	} else {
		seen := make(map[string]bool, len(requested))
		for _, raw := range requested {
			name := strings.TrimSpace(raw)
			if !allowed[name] {
				return nil, fmt.Errorf("不允许对表 %q 执行 VACUUM", raw)
			}
			if !seen[name] {
				seen[name] = true
				targets = append(targets, name)
			}
		}
	}
	stmtPrefix := "VACUUM ANALYZE "
	if full {
		stmtPrefix = "VACUUM FULL "
	}
	done := make([]string, 0, len(targets))
	for _, tbl := range targets {
		if _, err := d.ExecContext(ctx, stmtPrefix+tbl); err != nil {
			return done, fmt.Errorf("vacuum %s: %w", tbl, err)
		}
		done = append(done, tbl)
	}
	return done, nil
}
