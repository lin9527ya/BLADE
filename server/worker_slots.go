package server

import (
	"log"
	"net/http"
	"strconv"
)

// WorkerSlotDTO is one fixed execution slot of a task. Sessions are reusable
// across intents; the UI renders exactly Slots sessions and no per-intent ones.
type WorkerSlotDTO struct {
	Slot         int    `json:"slot"`
	Name         string `json:"name"`
	Session      string `json:"session"` // "slot:<n>" activity-session key
	State        string `json:"state"`   // running | paused | open | idle
	IntentID     string `json:"intent_id,omitempty"`
	Summary      string `json:"summary,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	LastActivity string `json:"last_activity,omitempty"`
}

// workerSlots returns the task's fixed N worker slots with their current intent.
func (s *Server) workerSlots(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	n := s.m.TaskWorkerSlots(t.ID)
	if n <= 0 {
		n = 1
	}
	names := make([]string, n)
	nums := make([]int, n)
	for i := 0; i < n; i++ {
		nums[i] = i + 1
		names[i] = slotName(i + 1)
	}
	items := make([]WorkerSlotDTO, n)
	for i := range items {
		items[i] = WorkerSlotDTO{Slot: i + 1, Name: names[i], Session: "slot:" + strconv.Itoa(i+1), State: "idle"}
	}
	if intents, err := t.Store.ActiveSlotIntents(names); err != nil {
		log.Printf("[worker-slots] task=%s intents: %v", t.ID, err)
	} else {
		bySlot := make(map[int]*WorkerSlotDTO, len(items))
		for i := range items {
			bySlot[items[i].Slot] = &items[i]
		}
		for _, node := range intents {
			if node == nil {
				continue
			}
			if d := bySlot[slotNumber(node.Owner)]; d != nil {
				d.IntentID = i64s(node.ID)
				d.Summary = intentSummaryOf(node)
				d.State = node.State
				d.StartedAt = rfc3339(node.CreatedAt)
			}
		}
	}
	if last, err := t.Store.LastActivityBySlot(nums); err == nil {
		for i := range items {
			if ts, ok := last[items[i].Slot]; ok {
				items[i].LastActivity = rfc3339(ts)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"slots": items, "count": len(items)})
}
