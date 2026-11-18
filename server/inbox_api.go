package server

import (
	"net/http"
	"time"
)

const settingInboxReadAt = "inbox_read_at"

// inboxList returns the operator's in-app inbox and an unread count derived from
// the single persisted read cursor. There is no per-item read table: this is a
// single-admin self-hosted product, and "mark all read" is the useful operation.
func (s *Server) inboxList(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	limit := queryInt(r, "limit", 100)
	items, err := pg.ListInboxItems(limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	readAt := time.Time{}
	if raw, ok, err := pg.GetSetting(settingInboxReadAt); err == nil && ok {
		readAt, _ = time.Parse(time.RFC3339Nano, raw)
	}
	unread := 0
	for _, item := range items {
		if item.CreatedAt.After(readAt) {
			unread++
		}
	}
	resp := map[string]any{
		"items":   items,
		"unread":  unread,
		"read_at": "",
	}
	if !readAt.IsZero() {
		resp["read_at"] = readAt.UTC().Format(time.RFC3339Nano)
	}
	writeJSONETag(w, r, 200, resp)
}

func (s *Server) inboxMarkRead(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	now := time.Now().UTC()
	if err := pg.SetSetting(settingInboxReadAt, now.Format(time.RFC3339Nano)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "read_at": now.Format(time.RFC3339Nano)})
}
