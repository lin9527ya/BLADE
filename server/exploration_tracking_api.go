package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/db"
)

type taskSchedulingReport struct {
	*db.SchedulingReport
	DispatchMode    string  `json:"dispatch_mode"`
	GateReason      string  `json:"gate_reason,omitempty"`
	WorkerSlots     int     `json:"worker_slots"`
	ActiveWorks     int     `json:"active_works"`
	ContinuationIDs []int64 `json:"continuation_ids,omitempty"`
}

func (s *Server) taskScheduling(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok || t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	limit, err := trackingInt(r, "limit", 20, 1, 100)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var focus int64
	if raw := r.URL.Query().Get("intent_id"); raw != "" {
		focus, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || focus <= 0 {
			writeErr(w, 400, "intent_id must be positive")
			return
		}
		n, err := t.Store.GetNode(focus)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if n == nil || n.Kind != db.KindIntent {
			writeErr(w, 404, "intent not found in this task")
			return
		}
	}
	view, err := t.Store.ScheduleReport(limit, focus)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := taskSchedulingReport{SchedulingReport: view, DispatchMode: "regular", WorkerSlots: s.engine.TaskWorkers(t.ID)}
	state := t.lifecycleSnapshot()
	switch {
	case s.engine.IsDeleting(t.ID):
		out.GateReason = "task_deleting"
	case state.Paused || s.engine.IsPaused(t.ID):
		out.GateReason = "task_paused"
	case state.Queued:
		out.GateReason = "task_queued"
	case isTerminalStatus(state.Status):
		out.GateReason = "task_terminal"
	case s.engine.isSettling(t.ID):
		if s.engine.isGoalDraining(t.ID) {
			out.DispatchMode = "goal_drain"
			out.ContinuationIDs = append([]int64(nil), s.engine.goalDrainIDs(t.ID)...)
		} else {
			out.GateReason = "task_settling"
		}
	case !s.engine.ReadyFor(t):
		out.GateReason = "llm_unavailable"
	case !s.engine.Started(t.ID):
		out.GateReason = "engine_not_started"
	}
	if out.GateReason != "" {
		out.DispatchMode = "stopped"
	}
	s.engine.workMu.Lock()
	for _, work := range s.engine.work {
		if work.taskID == tidOfTask(t) {
			out.ActiveWorks++
		}
	}
	s.engine.workMu.Unlock()
	for i := range out.Rows {
		row := &out.Rows[i]
		if out.GateReason != "" {
			row.WaitReasons = append(row.WaitReasons, out.GateReason)
		} else if out.DispatchMode == "goal_drain" {
			allowed := false
			for _, id := range out.ContinuationIDs {
				if row.IntentID == id {
					allowed = true
					break
				}
			}
			if !allowed {
				row.WaitReasons = append(row.WaitReasons, "not_drain_continuation")
			}
		} else if out.WorkerSlots > 0 && out.ActiveWorks >= out.WorkerSlots {
			row.WaitReasons = append(row.WaitReasons, "worker_slots_busy")
		}
	}
	writeJSONETag(w, r, 200, out)
}

func (s *Server) taskLeadTraces(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok || t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	limit, err := trackingInt(r, "limit", 10, 1, 100)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	offset, err := trackingInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	state := r.URL.Query().Get("state")
	switch state {
	case "", "pending", "queued", "running", "paused", "completed", "needs_follow_up", "dismissed", "source_changed":
	default:
		writeErr(w, 400, "invalid lead state")
		return
	}
	leadID := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("lead_id")))
	if leadID != "" {
		if _, _, err := db.ParseExplorationLeadID(leadID); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	view, err := t.Store.LeadTraceReport(state, leadID, limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSONETag(w, r, 200, view)
}

func trackingInt(r *http.Request, key string, fallback, min, max int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be %d..%d", key, min, max)
	}
	return n, nil
}

func (s *Server) taskPlannerState(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok || t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	taskID := tidOfTask(t)
	st, found, err := t.Store.LoadPlannerState(taskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	todos := st.Todos
	if todos == nil {
		todos = []db.PlannerTodoItem{}
	}
	updatedAt := ""
	if found && !st.UpdatedAt.IsZero() {
		updatedAt = rfc3339(st.UpdatedAt)
	}
	writeJSONETag(w, r, 200, map[string]any{
		"found":           found,
		"todos":           todos,
		"last_round_note": st.LastRoundNote,
		"updated_at":      updatedAt,
	})
}
