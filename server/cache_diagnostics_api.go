package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/agent"
)

func (s *Server) getCacheDiagnostics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	task := strings.TrimSpace(q.Get("task"))
	if task != "" {
		n, err := strconv.ParseInt(task, 10, 64)
		if err != nil || n <= 0 {
			writeErr(w, 400, "任务 ID 无效")
			return
		}
		task = strconv.FormatInt(n, 10)
	}
	worker := q.Get("worker")
	if worker != "" && worker != "planner" && worker != "worker" && worker != "goals" && worker != "mainagent" {
		writeErr(w, 400, "角色无效")
		return
	}
	cache := q.Get("cache")
	if cache != "" && cache != "zero" && cache != "low" {
		writeErr(w, 400, "缓存筛选无效")
		return
	}
	before := int64(0)
	if raw := q.Get("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, 400, "游标无效")
			return
		}
		before = n
	}
	session := q.Get("session")
	if len(session) > 256 {
		writeErr(w, 400, "会话 ID 过长")
		return
	}
	rows, err := s.m.PG().UsageDiagnostics(r.Context(), task, worker, cache, session, before, 100)
	if err != nil {
		writeErr(w, 503, "调用计量读取失败，请查看服务端日志")
		return
	}
	next := int64(0)
	if len(rows) == 100 {
		next = rows[len(rows)-1].ID
	}
	writeJSON(w, 200, map[string]any{"state": agent.CacheTraceState(), "requests": agent.CacheDiagnosticSnapshot(task, session, 100), "calls": rows, "next_before": next, "window_hours": 24})
}

func (s *Server) setCacheDiagnostics(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
		Seconds int   `json:"seconds"`
		Clear   bool  `json:"clear"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if body.Enabled != nil {
		if !*body.Enabled && agent.CacheTraceState().Environment {
			writeErr(w, 409, "诊断由环境变量启用，需调整环境并重启后关闭")
			return
		}
		if *body.Enabled {
			if body.Seconds == 0 {
				body.Seconds = 600
			}
			if body.Seconds < 30 || body.Seconds > 1800 {
				writeErr(w, 400, "诊断窗口应为 30–1800 秒")
				return
			}
			agent.SetCacheTraceWindow(time.Duration(body.Seconds) * time.Second)
		} else {
			agent.SetCacheTraceWindow(0)
		}
	}
	if body.Clear {
		agent.ClearCacheDiagnostics()
	}
	writeJSON(w, 200, agent.CacheTraceState())
}
