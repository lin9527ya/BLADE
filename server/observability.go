package server

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// readyz is intentionally unauthenticated (like /api/health): load balancers and
// systemd probes cannot log in. It reveals only readiness, not configuration.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	checks := map[string]any{"ok": true}
	status := http.StatusOK
	if s.m == nil || s.m.PG() == nil {
		checks["ok"] = false
		checks["database"] = "unavailable"
		status = http.StatusServiceUnavailable
	} else if err := s.m.PG().Ping(); err != nil {
		checks["ok"] = false
		checks["database"] = err.Error()
		status = http.StatusServiceUnavailable
	} else {
		checks["database"] = "ok"
	}
	writeJSON(w, status, checks)
}

func promLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

// metricsPrometheus exposes the same operational facts as /api/metrics in a
// scrape-friendly text format. It is auth-protected (use an API token for
// Prometheus) because task names/counts are operationally sensitive.
func (s *Server) metricsPrometheus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP blade_build_info Build and runtime identity.\n")
	fmt.Fprintf(&b, "# TYPE blade_build_info gauge\n")
	fmt.Fprintf(&b, "blade_build_info{version=%q,go=%q} 1\n", promLabel(BuildVersion), promLabel(runtime.Version()))
	fmt.Fprintf(&b, "# HELP blade_uptime_seconds Process uptime.\n# TYPE blade_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "blade_uptime_seconds %.0f\n", time.Since(processStarted).Seconds())

	pg := s.m.PG()
	if pg == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}

	taskCounts := map[string]int{}
	for _, task := range s.m.List() {
		taskCounts[s.resolvedTaskStatus(task)]++
	}
	fmt.Fprintf(&b, "# HELP blade_tasks Task count by effective status.\n# TYPE blade_tasks gauge\n")
	for status, count := range taskCounts {
		fmt.Fprintf(&b, "blade_tasks{status=%q} %d\n", promLabel(status), count)
	}

	if pending, err := pg.CountPendingIntercepts(); err == nil {
		fmt.Fprintf(&b, "# HELP blade_approvals_pending Unresolved approval requests.\n# TYPE blade_approvals_pending gauge\n")
		fmt.Fprintf(&b, "blade_approvals_pending %d\n", pending)
	}
	if stats, err := pg.NotificationStatsSnapshot(r.Context()); err == nil {
		fmt.Fprintf(&b, "# HELP blade_notification_deliveries Notification deliveries by state.\n# TYPE blade_notification_deliveries gauge\n")
		fmt.Fprintf(&b, "blade_notification_deliveries{state=\"pending\"} %d\n", stats.Pending)
		fmt.Fprintf(&b, "blade_notification_deliveries{state=\"failed\"} %d\n", stats.Failed)
		fmt.Fprintf(&b, "# HELP blade_notification_backlog_age_ms Age of the oldest pending delivery.\n# TYPE blade_notification_backlog_age_ms gauge\n")
		fmt.Fprintf(&b, "blade_notification_backlog_age_ms %d\n", stats.BacklogAgeMS)
	}
	if tr := s.m.Traffic(); tr != nil {
		if n, err := tr.Count(); err == nil {
			fmt.Fprintf(&b, "# HELP blade_traffic_exchanges Recorded HTTP exchanges.\n# TYPE blade_traffic_exchanges gauge\n")
			fmt.Fprintf(&b, "blade_traffic_exchanges %d\n", n)
		}
	}

	snap := httpStats.snapshot()
	fmt.Fprintf(&b, "# HELP blade_http_requests_total Process-level HTTP request count by path bucket.\n# TYPE blade_http_requests_total counter\n")
	if rows, ok := snap["by_path"].([]map[string]any); ok {
		for _, row := range rows {
			path, _ := row["path"].(string)
			count, _ := row["count"].(int64)
			fmt.Fprintf(&b, "blade_http_requests_total{path=%q} %d\n", promLabel(path), count)
		}
	}
	fmt.Fprintf(&b, "# HELP blade_http_slow_requests_total Requests slower than the configured threshold.\n# TYPE blade_http_slow_requests_total counter\n")
	if slow, ok := snap["slow_total"].(int64); ok {
		fmt.Fprintf(&b, "blade_http_slow_requests_total %d\n", slow)
	}
	_, _ = w.Write([]byte(b.String()))
}
