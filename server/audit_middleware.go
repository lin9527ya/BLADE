package server

import (
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

func auditMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// auditMutations records every mutating API request after it completes. It sits
// after requireAuth so actorFrom carries either "admin" or "token:<name>".
func (s *Server) auditMutations(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !auditMutation(r.Method) || !strings.HasPrefix(path, "/api/") ||
			strings.HasPrefix(path, "/api/auth/") || path == "/api/health" || path == "/api/readyz" {
			h.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		h.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		actor := actorFrom(r)
		if actor == "" {
			actor = "admin"
		}
		if s.m != nil && s.m.PG() != nil {
			if err := s.m.PG().InsertAuditEvent(actor, r.Method, path, rec.status, loginClientIP(r), r.UserAgent()); err != nil {
				log.Printf("[audit] 写入失败 %s %s: %v", r.Method, path, err)
			}
		}
	})
}

func (s *Server) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	items, err := pg.ListAuditEventsFiltered(db.AuditFilter{
		Actor:  strings.TrimSpace(r.URL.Query().Get("actor")),
		Method: strings.TrimSpace(r.URL.Query().Get("method")),
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  queryInt(r, "limit", 100),
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) exportAuditEvents(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	items, err := pg.ListAuditEventsFiltered(db.AuditFilter{
		Actor:  strings.TrimSpace(r.URL.Query().Get("actor")),
		Method: strings.TrimSpace(r.URL.Query().Get("method")),
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  10000,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit-events.csv"`)
	_, _ = w.Write([]byte("\ufeff"))
	cw := csv.NewWriter(w)
	_ = cw.Write(csvSafeRow([]string{"id", "time", "actor", "method", "path", "status", "ip", "user_agent"}))
	for _, e := range items {
		_ = cw.Write(csvSafeRow([]string{fmt.Sprint(e.ID), e.CreatedAt.Format(time.RFC3339), e.Actor, e.Method, e.Path,
			fmt.Sprint(e.Status), e.IP, e.UserAgent}))
	}
	cw.Flush()
}
