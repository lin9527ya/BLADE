package server

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// diagnosticsBundle packages the operational facts an operator needs when asking
// for help: counts, recent audit events and version. It deliberately excludes
// settings/secrets, task payloads and traffic bodies.
func (s *Server) diagnosticsBundle(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	summary := map[string]any{
		"version":        BuildVersion,
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"uptime_seconds": int64(time.Since(processStarted).Seconds()),
	}
	if e := s.m.Enrich(); e != nil {
		summary["system"] = e.Stats()
	}
	if pending, err := pg.CountPendingIntercepts(); err == nil {
		summary["approvals_pending"] = pending
	}
	if stats, err := pg.NotificationStatsSnapshot(r.Context()); err == nil {
		summary["notifications"] = stats
	}
	tasks := map[string]int{}
	for _, task := range s.m.List() {
		tasks[s.resolvedTaskStatus(task)]++
	}
	summary["tasks"] = tasks
	audit, _ := pg.ListAuditEvents(200)
	summary["recent_audit"] = audit

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "blade-diagnostics-"+time.Now().Format("20060102-150405")+".zip"))
	zw := zip.NewWriter(w)
	defer zw.Close()
	if err := writeZipJSON(zw, "summary.json", summary); err != nil {
		return
	}
	_ = writeZipText(zw, "README.txt", "BLADE diagnostics bundle\n\nContains version, uptime, task counts, approval/notification backlog and recent audit events.\nIt does not contain credentials, settings, task reports, traffic bodies or uploaded files.\n")
}

func writeZipJSON(zw *zip.Writer, name string, value any) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func writeZipText(zw *zip.Writer, name, text string) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(text))
	return err
}
