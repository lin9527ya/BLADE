package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/lin9527ya/blade/db"
)

// liveHub is a small in-process fan-out for snapshot streams. Each topic has at
// most one producer loop shared by all subscribers; the loop is cancelled when
// the last subscriber leaves. Every tick sends a full snapshot, so a dropped
// frame is self-healing on the next tick (no replay protocol needed).
type liveHub[T any] struct {
	mu      sync.Mutex
	subs    map[string]map[chan T]struct{}
	cancels map[string]context.CancelFunc
}

func newLiveHub[T any]() *liveHub[T] {
	return &liveHub[T]{subs: map[string]map[chan T]struct{}{}, cancels: map[string]context.CancelFunc{}}
}

func (h *liveHub[T]) subscribe(key string, build func() T) (<-chan T, func()) {
	ch := make(chan T, 16)
	h.mu.Lock()
	if h.subs[key] == nil {
		h.subs[key] = map[chan T]struct{}{}
	}
	h.subs[key][ch] = struct{}{}
	if len(h.subs[key]) == 1 {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancels[key] = cancel
		go h.loop(ctx, key, build)
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if m := h.subs[key]; m != nil {
			delete(m, ch)
			if len(m) == 0 {
				delete(h.subs, key)
				if cancel := h.cancels[key]; cancel != nil {
					cancel()
					delete(h.cancels, key)
				}
			}
		}
		h.mu.Unlock()
	}
}

func (h *liveHub[T]) loop(ctx context.Context, key string, build func() T) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	h.publish(key, build())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.publish(key, build())
		}
	}
}

func (h *liveHub[T]) publish(key string, value T) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[key] {
		select {
		case ch <- value:
		default:
			// The next snapshot supersedes this one; never block the producer.
		}
	}
}

type TaskLiveStats struct {
	EngineMode        string `json:"engine_mode"`
	InFlight          int    `json:"in_flight"`
	GoalsTotal        int    `json:"goals_total"`
	GoalsMet          int    `json:"goals_met"`
	Paused            bool   `json:"paused"`
	Running           bool   `json:"running"`
	LLMInFlight       int    `json:"llm_in_flight"`
	LastActivity      int64  `json:"last_activity"`
	Stalled           bool   `json:"stalled"`
	PendingIntercepts int    `json:"pending_intercepts"`
	ChatRunning       bool   `json:"chat_running"`
	PlannerRunning    bool   `json:"planner_running"`
}

type TaskLiveEvent struct {
	Task  TaskDTO       `json:"task"`
	Stats TaskLiveStats `json:"stats"`
	At    int64         `json:"at"`
	Error string        `json:"error,omitempty"`
}

type ApprovalLiveEvent struct {
	Pending      []interceptPendingDTO `json:"pending"`
	PendingCount int                   `json:"pending_count"`
	SLA          *db.InterceptSLAStats `json:"sla,omitempty"`
	At           int64                 `json:"at"`
	Error        string                `json:"error,omitempty"`
}

func (s *Server) taskLiveSnapshot(taskID string) TaskLiveEvent {
	t := s.m.ResolveTask(taskID)
	if t == nil {
		return TaskLiveEvent{At: time.Now().Unix(), Error: "task not found"}
	}
	status := s.resolvedTaskStatus(t)
	stats := TaskLiveStats{}
	if states, err := t.Store.CountKindStates(db.KindIntent); err == nil {
		stats.InFlight = states["running"]
	}
	if states, err := t.Store.CountKindStates(db.KindGoal); err == nil {
		stats.GoalsMet = states["met"]
		for _, n := range states {
			stats.GoalsTotal += n
		}
	}
	stats.Paused = s.engine.IsPaused(t.ID)
	stats.LastActivity = s.engine.LastActivity(t.ID)
	stats.LLMInFlight = int(s.engine.ActiveLLMCalls(t.ID))
	stats.Running = s.engine.ReadyFor(t) && s.engine.Started(t.ID) && !stats.Paused
	stats.Stalled = stats.Running && stats.LLMInFlight == 0 && stats.InFlight > 0 &&
		stats.LastActivity > 0 && time.Now().Unix()-stats.LastActivity > 120
	switch {
	case stats.Paused:
		stats.EngineMode = "paused"
	case stats.Stalled:
		stats.EngineMode = "stalled"
	case stats.Running && stats.LLMInFlight > 0:
		stats.EngineMode = "exploring"
	default:
		stats.EngineMode = "idle"
	}
	if n, err := s.m.PG().CountTaskPendingIntercepts(t.ID); err == nil {
		stats.PendingIntercepts = n
	}
	s.chatRuns.mu.Lock()
	stats.ChatRunning = s.chatRuns.busy[t.ID]
	s.chatRuns.mu.Unlock()
	stats.PlannerRunning = s.engine.IsPlannerRunning(t.ID)
	return TaskLiveEvent{Task: taskDTO(t, status), Stats: stats, At: time.Now().Unix()}
}

func (s *Server) approvalLiveSnapshot() ApprovalLiveEvent {
	pg := s.m.PG()
	if pg == nil {
		return ApprovalLiveEvent{At: time.Now().Unix(), Error: "database unavailable"}
	}
	pending, err := pg.ListPendingIntercepts()
	if err != nil {
		return ApprovalLiveEvent{At: time.Now().Unix(), Error: err.Error()}
	}
	out := make([]interceptPendingDTO, 0, len(pending))
	for i := range pending {
		out = append(out, pendingApprovalDTO(&pending[i]))
	}
	sla, _ := pg.InterceptSLAStats(3600)
	return ApprovalLiveEvent{Pending: out, PendingCount: len(out), SLA: sla, At: time.Now().Unix()}
}

func writeSSEJSON(w http.ResponseWriter, flusher http.Flusher, event string, v any) bool {
	body, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return sseWrite(w, flusher, fmt.Sprintf("event: %s\ndata: %s\n\n", event, body))
}

func (s *Server) taskLiveStream(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if s.m.ResolveTask(taskID) == nil {
		writeErr(w, 404, "task not found")
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
	s.liveInit.Do(func() {
		if s.taskLive == nil {
			s.taskLive = newLiveHub[TaskLiveEvent]()
		}
		if s.approvalLive == nil {
			s.approvalLive = newLiveHub[ApprovalLiveEvent]()
		}
	})
	ch, unsub := s.taskLive.subscribe(taskID, func() TaskLiveEvent { return s.taskLiveSnapshot(taskID) })
	defer unsub()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if !writeSSEJSON(w, flusher, "task", ev) {
				return
			}
		case <-ping.C:
			if !sseWrite(w, flusher, ": ping\n\n") {
				return
			}
		}
	}
}

func (s *Server) approvalLiveStream(w http.ResponseWriter, r *http.Request) {
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
	s.liveInit.Do(func() {
		if s.taskLive == nil {
			s.taskLive = newLiveHub[TaskLiveEvent]()
		}
		if s.approvalLive == nil {
			s.approvalLive = newLiveHub[ApprovalLiveEvent]()
		}
	})
	ch, unsub := s.approvalLive.subscribe("pending", s.approvalLiveSnapshot)
	defer unsub()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if !writeSSEJSON(w, flusher, "approvals", ev) {
				return
			}
		case <-ping.C:
			if !sseWrite(w, flusher, ": ping\n\n") {
				return
			}
		}
	}
}
