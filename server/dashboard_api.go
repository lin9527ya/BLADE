package server

import (
	"log"
	"net/http"
	"runtime"
	"sort"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/llmrec"
)

type dashboardLLMPoolMember struct {
	ProfileID    string `json:"profile_id"`
	Name         string `json:"name"`
	Model        string `json:"model"`
	State        string `json:"state"`
	Active       bool   `json:"active"`
	Excluded     bool   `json:"excluded"`
	CooldownSecs int    `json:"cooldown_secs,omitempty"`
}

type dashboardLLMPoolSummary struct {
	Enabled       bool                     `json:"enabled"`
	BindFallback  bool                     `json:"bind_fallback"`
	OkCount       int                      `json:"ok_count"`
	DegradedCount int                      `json:"degraded_count"`
	TrippedCount  int                      `json:"tripped_count"`
	HalfOpenCount int                      `json:"half_open_count"`
	Members       []dashboardLLMPoolMember `json:"members"`
}

// dashboardSummary returns what the SecOps dashboard renders: task counts, a
// recent-task window, whole-task token aggregates, cached operational counts,
// and in-memory system / pool / mobile runtime telemetry.
func (s *Server) dashboardSummary(w http.ResponseWriter, r *http.Request) {
	all := s.m.List()
	statuses := map[string]int{}
	mobileTasksTotal, mobileTasksRunning := 0, 0
	for _, t := range all {
		st := s.resolvedTaskStatus(t)
		statuses[st]++
		if t.lifecycleSnapshot().AppMode == "mobile" {
			mobileTasksTotal++
			if st == "running" {
				mobileTasksRunning++
			}
		}
	}

	lastActivity := s.cachedLastActivity(r.Context())
	recent := make([]*Task, len(all))
	copy(recent, all)
	recency := make(map[string]int64, len(recent))
	for _, t := range recent {
		recency[t.ID] = s.dashboardRecency(t, lastActivity)
	}
	sort.SliceStable(recent, func(i, j int) bool {
		return recency[recent[i].ID] > recency[recent[j].ID]
	})
	if len(recent) > 10 {
		recent = recent[:10]
	}

	tokens, err := s.cachedDashboardTokens(r.Context())
	if err != nil {
		log.Printf("[dashboard] token aggregate: %v", err)
	}
	ops, opsErr := s.cachedDashboardOps(r.Context())
	if opsErr != nil {
		log.Printf("[dashboard] ops aggregate: %v", opsErr)
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	writeJSONETag(w, r, 200, map[string]any{
		"task_total":       len(all),
		"tasks_by_status":  statuses,
		"recent_tasks":     s.taskDTOsFor(r.Context(), recent),
		"token_by_profile": tokens.ByProfile,
		"daily_tokens":     tokens.Daily,
		"ops":              ops,
		"system":           s.systemMetricsSnapshot(),
		"runtime": map[string]any{
			"uptime_seconds":   int64(time.Since(processStarted).Seconds()),
			"llm_in_flight":    llmrec.InFlightCalls(),
			"goroutines":       runtime.NumGoroutine(),
			"heap_alloc_bytes": ms.HeapAlloc,
		},
		"llm_pool": s.dashboardLLMPoolSummary(),
		"mobile":   s.dashboardMobileSummary(ops, mobileTasksTotal, mobileTasksRunning),
	})
}

func (s *Server) dashboardLLMPoolSummary() dashboardLLMPoolSummary {
	raw := s.llmPoolStatus()
	out := dashboardLLMPoolSummary{Members: []dashboardLLMPoolMember{}}
	out.Enabled, _ = raw["enabled"].(bool)
	out.BindFallback, _ = raw["bind_fallback"].(bool)
	chain, _ := raw["chain"].([]LLMPoolMemberStatus)
	for _, m := range chain {
		if !m.Excluded {
			switch m.State {
			case "tripped":
				out.TrippedCount++
			case "half_open":
				out.HalfOpenCount++
			case "degraded":
				out.DegradedCount++
			default:
				out.OkCount++
			}
		}
		out.Members = append(out.Members, dashboardLLMPoolMember{
			ProfileID:    m.ProfileID,
			Name:         m.Name,
			Model:        m.Model,
			State:        m.State,
			Active:       m.Active,
			Excluded:     m.Excluded,
			CooldownSecs: m.CooldownSecs,
		})
	}
	return out
}

func (s *Server) dashboardMobileSummary(ops db.DashboardOpsSummary, tasksTotal, tasksRunning int) map[string]any {
	lanRunning, transRunning := false, false
	pinningAlerts := 0
	if mp := s.m.MobileProxy(); mp != nil && mp.Running() {
		lanRunning = true
	}
	if tp := s.m.TransparentProxy(); tp != nil && tp.Running() {
		transRunning = true
	}
	if tr := s.m.Traffic(); tr != nil {
		pinningAlerts = len(tr.PinningAlerts())
	}
	return map[string]any{
		"devices_total":             ops.MobileDevicesTotal,
		"devices_bound":             ops.MobileDevicesBound,
		"devices_rooted":            ops.MobileDevicesRoot,
		"mobile_tasks_total":        tasksTotal,
		"mobile_tasks_running":      tasksRunning,
		"proxy_lan_running":         lanRunning,
		"proxy_transparent_running": transRunning,
		"pinning_alerts_count":      pinningAlerts,
	}
}

func (s *Server) dashboardRecency(t *Task, persisted map[int64]int64) int64 {
	latest := t.CreatedAt
	if v := persisted[t.ExpID]; v > latest {
		latest = v
	}
	if v := s.engine.LastActivity(t.ID); v > latest {
		latest = v
	}
	return latest
}
