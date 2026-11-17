package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// verificationLedger is a read-only view over the existing tool_executions
// ledger. It does not create a parallel PoC table: the command/output is
// already the audit fact, and credential checks/retests stay in their own APIs.
func (s *Server) verificationLedger(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, err := pg.ListVerificationLedger(db.VerificationLedgerFilter{
		TaskID: strings.TrimSpace(q.Get("task_id")),
		Tool:   strings.TrimSpace(q.Get("tool")),
		Status: strings.TrimSpace(q.Get("status")),
		Query:  strings.TrimSpace(q.Get("q")),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
