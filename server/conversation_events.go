package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/lin9527ya/blade/db"
)

// conversationEvents streams one conversation's activity tail. On first connect
// it sends the latest page (so a client that missed the gap recovers without
// replaying the entire thread); on reconnect EventSource's Last-Event-ID is
// honored. Each frame carries the activity id, so the browser can resume.
func (s *Server) conversationEvents(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if !sseWrite(w, flusher, "retry: 3000\n\n") {
		return
	}

	since := int64(0)
	if le := r.Header.Get("Last-Event-ID"); le != "" {
		since, _ = strconv.ParseInt(le, 10, 64)
	}
	if since == 0 {
		since, _ = strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	}

	cursor := since
	if cursor == 0 {
		items, _, err := pg.ConvActivityPageContext(r.Context(), c.ID, 0, 200)
		if err != nil {
			_ = writeSSEJSON(w, flusher, "status", map[string]any{"error": err.Error()})
			return
		}
		for _, a := range items {
			if !writeConversationActivity(w, flusher, a) {
				return
			}
			cursor = a.ID
		}
	}

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		s.chatRuns.mu.Lock()
		running := s.chatRuns.busy[s.convBusyKey(c.ID)]
		s.chatRuns.mu.Unlock()
		items, next, hasMore, err := pg.ConvActivityTailPageContext(r.Context(), c.ID, cursor, 500)
		if err != nil {
			_ = writeSSEJSON(w, flusher, "status", map[string]any{"error": err.Error()})
			return
		}
		for _, a := range items {
			if !writeConversationActivity(w, flusher, a) {
				return
			}
			cursor = a.ID
		}
		if next > cursor {
			cursor = next
		}
		if !writeSSEJSON(w, flusher, "status", map[string]any{
			"running": running, "cursor": cursor, "has_more": hasMore, "at": time.Now().Unix(),
		}) {
			return
		}
		delay := 3 * time.Second
		if running {
			delay = time.Second
		}
		if hasMore {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-ping.C:
			timer.Stop()
			if !sseWrite(w, flusher, ": ping\n\n") {
				return
			}
		case <-timer.C:
		}
	}
}

func writeConversationActivity(w http.ResponseWriter, flusher http.Flusher, a db.Activity) bool {
	body, err := jsonMarshal(activityDTO(a))
	if err != nil {
		return false
	}
	return sseWrite(w, flusher, fmt.Sprintf("id: %d\nevent: activity\ndata: %s\n\n", a.ID, body))
}

// jsonMarshal is a tiny indirection so the SSE writer and tests share one
// serialization path without pulling encoding/json into every call site.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
