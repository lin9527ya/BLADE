package server

import (
	"log"
	"net/http"
	"sort"
)

// dashboardSummary returns only what the dashboard renders: task counts, a
// recent-task window, and whole-task token aggregates. The former dashboard
// fetched every task DTO (with per-task metrics) every five seconds.
func (s *Server) dashboardSummary(w http.ResponseWriter, r *http.Request) {
	all := s.m.List()
	statuses := map[string]int{}
	for _, t := range all {
		statuses[s.resolvedTaskStatus(t)]++
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
	if len(recent) > 8 {
		recent = recent[:8]
	}

	tokens, err := s.cachedDashboardTokens(r.Context())
	if err != nil {
		log.Printf("[dashboard] token aggregate: %v", err)
	}

	writeJSON(w, 200, map[string]any{
		"task_total":       len(all),
		"tasks_by_status":  statuses,
		"recent_tasks":     s.taskDTOsFor(r.Context(), recent),
		"token_by_profile": tokens.ByProfile,
		"daily_tokens":     tokens.Daily,
	})
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
