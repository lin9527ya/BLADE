package server

import (
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

	tasks := make([]map[string]any, 0, 8)
	for _, t := range s.m.List() {
		item := map[string]any{
			"id":               t.ID,
			"status":           s.resolvedTaskStatus(t),
			"workers":          s.engine.TaskWorkers(t.ID),
			"inflight":         s.engine.inflightCount(t.ID),
			"active_llm_calls": s.engine.ActiveLLMCalls(t.ID),
		}
		if t.Store != nil {
			if states, err := t.Store.CountIntentStates(); err == nil {
				item["intent_states"] = states
			}
			if n, err := t.Store.CountClaimableIntents(); err == nil {
				item["claimable_intents"] = n
			}
		}
		tasks = append(tasks, item)
	}
	out["tasks"] = tasks

	if pg := s.m.PG(); pg != nil {
		out["llm"] = map[string]any{
			"window_hours": 24,
			"by_profile":   s.metricsLLMBy("profile_name"),
			"by_worker":    s.metricsLLMBy("worker"),
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
		if err := rows.Scan(&row.Key, &row.Calls, &row.InputTokens, &row.OutputTokens, &row.P50Ms, &row.P95Ms); err != nil {
			return out
		}
		out = append(out, row)
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
