package server

import (
	"encoding/json"
	"net/http"
)

func (s *Server) listAPITokens(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	items, err := pg.ListAPITokens()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	token, plain, err := pg.CreateAPIToken(req.Name)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// The plaintext is returned exactly once; the list endpoint never includes it.
	writeJSON(w, 201, map[string]any{"token": token, "plaintext": plain})
}

func (s *Server) revokeAPIToken(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad token id")
		return
	}
	// DELETE 默认是“撤销”（保留审计记录）；?purge=1 才是永久删除该行。
	if r.URL.Query().Get("purge") == "1" {
		n, err := pg.PurgeAPIToken(id)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if n == 0 {
			writeErr(w, 404, "token not found")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "purged": true})
		return
	}
	n, err := pg.RevokeAPIToken(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if n == 0 {
		writeErr(w, 404, "token not found")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
