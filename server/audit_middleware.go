package server

import (
	"log"
	"net/http"
	"strings"
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
	items, err := pg.ListAuditEvents(queryInt(r, "limit", 100))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
