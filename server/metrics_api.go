package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"

	"github.com/lin9527ya/blade/llmrec"
)

// 运行指标与调试面。这不是业务 API，而是让操作者能回答「为什么慢/为什么停/钱花在哪」：
//   - /api/metrics：每任务队列深度、worker 槽位、在跑 run、在飞 LLM 调用；近 24h 的
//     LLM 延迟 p50/p95（按 profile 与 worker）；执行台账状态计数；DB 连接池与内存。
//   - /api/debug/pprof/*：同一套 JWT 鉴权下的性能剖析入口。
//
// 只读、无副作用；指标查询有 24h 窗口与 LIMIT，不会随历史无限变慢。

var processStarted = time.Now()

type metricsLLMRow struct {
	Key          string `json:"key"`
	Calls        int64  `json:"calls"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheRead    int64  `json:"cache_read"`
	MissTokens   int64  `json:"miss_tokens"`
	ZeroCache    int64  `json:"zero_cache_calls"`
	P50Ms        int64  `json:"p50_ms"`
	P95Ms        int64  `json:"p95_ms"`
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"now":            time.Now().UTC(),
		"uptime_seconds": int64(time.Since(processStarted).Seconds()),
		"llm_in_flight":  llmrec.InFlightCalls(),
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out["runtime"] = map[string]any{
		"goroutines":       runtime.NumGoroutine(),
		"heap_alloc_bytes": ms.HeapAlloc,
		"heap_sys_bytes":   ms.HeapSys,
		"gc_cycles":        ms.NumGC,
	}

	if pg := s.m.PG(); pg != nil {
		st := pg.Stats()
		out["db"] = map[string]any{
			"open":     st.OpenConnections,
			"in_use":   st.InUse,
			"idle":     st.Idle,
			"wait":     st.WaitCount,
			"max_open": st.MaxOpenConnections,
		}
	}

	all := s.m.List()
	expIDs := make([]int64, 0, len(all))
	for _, t := range all {
		expIDs = append(expIDs, t.ExpID)
	}
	// One GROUP BY per metric instead of two queries per task; the metrics page
	// must not become the load it is trying to measure.
	var statesByExp map[int64]map[string]int
	var claimableByExp map[int64]int
	var statesErr, claimableErr error
	hasDB := false
	if pg := s.m.PG(); pg != nil {
		hasDB = true
		statesByExp, statesErr = pg.IntentStateCountsForContext(r.Context(), expIDs)
		claimableByExp, claimableErr = pg.ClaimableIntentCountsForContext(r.Context(), expIDs)
	}
	tasks := make([]map[string]any, 0, len(all))
	for _, t := range all {
		item := map[string]any{
			"id":               t.ID,
			"status":           s.resolvedTaskStatus(t),
			"workers":          s.engine.TaskWorkers(t.ID),
			"inflight":         s.engine.inflightCount(t.ID),
			"active_llm_calls": s.engine.ActiveLLMCalls(t.ID),
		}
		if hasDB && statesErr == nil {
			states := statesByExp[t.ExpID]
			if states == nil {
				states = map[string]int{}
			}
			item["intent_states"] = states
		}
		if hasDB && claimableErr == nil {
			item["claimable_intents"] = claimableByExp[t.ExpID]
		}
		tasks = append(tasks, item)
	}
	out["tasks"] = tasks
	// 被丢弃的活动行：不可恢复的审计丢失，此前只存在于一行日志里（重启即失）。
	// 既有进程级总数，也有按任务的明细（谁在丢）。
	dropped := make(map[string]int64, len(all))
	for _, t := range all {
		if n := s.engine.DroppedActivity(t.ID); n > 0 {
			dropped[t.ID] = n
		}
	}
	out["dropped_activity"] = map[string]any{
		"total":   s.engine.DroppedActivityTotal(),
		"by_task": dropped,
		"meaning": "写不进库的活动行（不可恢复）；持续增长说明库/连接池有问题",
	}
	// HTTP 请求：QPS 与错误率此前完全不可见（只有慢请求写日志）。
	out["http"] = httpStats.snapshot()
	out["logs"] = logSink.DBStats()
	if s.engine != nil {
		if bc := s.engine.Broadcaster(); bc != nil {
			out["sse"] = map[string]int64{"overflow_events": bc.OverflowCount()}
		}
	}

	if pg := s.m.PG(); pg != nil {
		out["llm"] = map[string]any{
			"window_hours": 24,
			"by_profile":   s.metricsLLMBy("profile_name"),
			"by_worker":    s.metricsLLMBy("worker"),
			// 每个 worker 的「冷启动会话数」：按 session_id 取第一条调用，命中=0 即冷启动。
			// 与 zero_cache_calls 的区别：后者包含中途掉命中的调用，前者只看会话首调。
			"cold_start_sessions": s.metricsColdStartSessions(),
		}
		execs := map[string]int{}
		if rows, err := pg.Query(`SELECT status, count(*) FROM tool_executions GROUP BY 1`); err == nil {
			for rows.Next() {
				var status string
				var n int
				if rows.Scan(&status, &n) == nil {
					execs[status] = n
				}
			}
			rows.Close()
		}
		out["executions"] = execs
		// 归档队列深度：归档 worker 是单线程 FIFO，积压意味着"点了归档但很久没动静"。
		out["archives"] = s.metricsArchiveQueue(r.Context())
	}

	writeJSON(w, 200, out)
}

// metricsLLMBy 聚合 24h 内每条 lane 的调用数、token 与延迟分位数。
// column 只允许代码内常量（profile_name / worker），不来自请求。
func (s *Server) metricsLLMBy(column string) []metricsLLMRow {
	pg := s.m.PG()
	if pg == nil {
		return []metricsLLMRow{}
	}
	q := fmt.Sprintf(`SELECT COALESCE(%s,'(unattributed)'), count(*),
       COALESCE(sum(input_tokens),0), COALESCE(sum(output_tokens),0),
       COALESCE(sum(cache_read),0), COALESCE(sum(input_tokens-cache_read),0),
       count(*) FILTER (WHERE cache_read=0),
       COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms),0)::bigint,
       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms),0)::bigint
FROM llm_usage
WHERE ts >= now() - interval '24 hours' AND latency_ms IS NOT NULL
GROUP BY 1 ORDER BY 2 DESC LIMIT 30`, column)
	rows, err := pg.Query(q)
	if err != nil {
		return []metricsLLMRow{}
	}
	defer rows.Close()
	out := make([]metricsLLMRow, 0, 8)
	for rows.Next() {
		var row metricsLLMRow
		if err := rows.Scan(&row.Key, &row.Calls, &row.InputTokens, &row.OutputTokens,
			&row.CacheRead, &row.MissTokens, &row.ZeroCache, &row.P50Ms, &row.P95Ms); err != nil {
			return out
		}
		out = append(out, row)
	}
	return out
}

// metricsColdStartSessions 统计近 24h 每个 worker 的「首调零命中」会话数。
// session_id 为空的历史行无法分组，跳过——它们只影响旧数据，不影响新运行。
func (s *Server) metricsColdStartSessions() map[string]int {
	pg := s.m.PG()
	if pg == nil {
		return map[string]int{}
	}
	rows, err := pg.Query(`
WITH firsts AS (
  SELECT DISTINCT ON (COALESCE(worker,'(unattributed)'), session_id)
         COALESCE(worker,'(unattributed)') AS worker, session_id, cache_read
  FROM llm_usage
  WHERE ts >= now() - interval '24 hours'
    AND session_id IS NOT NULL AND session_id <> ''
  ORDER BY COALESCE(worker,'(unattributed)'), session_id, ts
)
SELECT worker, count(*) FILTER (WHERE cache_read = 0)
FROM firsts GROUP BY worker`)
	if err != nil {
		return map[string]int{}
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var worker string
		var cold int
		if rows.Scan(&worker, &cold) == nil {
			out[worker] = cold
		}
	}
	return out
}

// registerMetricsRoutes 由 registerRoutes 调用（唯一注册入口）。
func (s *Server) registerMetricsRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/metrics", s.metrics)
	mux.HandleFunc("GET /api/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /api/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /api/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("POST /api/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /api/debug/pprof/trace", pprof.Trace)
	for _, name := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		h := pprof.Handler(name)
		mux.HandleFunc("GET /api/debug/pprof/"+name, h.ServeHTTP)
	}
}

// metricsArchiveQueue 返回归档队列的按状态计数（积压深度）。
//
// 归档 worker 是单线程 FIFO：一次打包可能几十秒到几分钟，积压时"点了归档但很久
// 没动静"是运维最常问的问题，而这个数此前只能去表里查。
func (s *Server) metricsArchiveQueue(ctx context.Context) map[string]int {
	out := map[string]int{}
	pg := s.m.PG()
	if pg == nil {
		return out
	}
	rows, err := pg.QueryContext(ctx, `SELECT state, count(*) FROM task_archives GROUP BY 1`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if rows.Scan(&state, &n) == nil {
			out[state] = n
		}
	}
	return out
}
