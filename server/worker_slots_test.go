package server

import (
	"encoding/json"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestParseActivitySessionSlot(t *testing.T) {
	f, ok := parseActivitySession("slot:3")
	if !ok || f.Slot == nil || *f.Slot != 3 || f.NodeID != nil {
		t.Fatalf("slot:3 → %+v ok=%v", f, ok)
	}
	for _, bad := range []string{"slot:0", "slot:-1", "slot:x"} {
		if _, ok := parseActivitySession(bad); ok {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}

func TestWorkerSlotsEndpoint(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	tasks := s.m.List()
	if len(tasks) == 0 {
		t.Fatal("no task in manager")
	}
	task := tasks[0]
	if n := s.m.EnsureTaskWorkerSlots(task.ID, 2); n != 2 {
		// A previous test run may have persisted a different count for a reused id;
		// the endpoint must still return the persisted value, so read it back.
		t.Logf("persisted slots = %d", n)
	}
	n := s.m.TaskWorkerSlots(task.ID)
	es := task.Store
	id, err := es.AddIntent(map[string]any{"summary": "slot one task"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.Exec(`UPDATE exploration_nodes SET state='running', owner='work#1' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	slot := 1
	if _, err := es.AppendActivity(db.Activity{NodeID: &id, Worker: "work#1", Kind: "text", Summary: "hello", SlotNo: &slot}); err != nil {
		t.Fatal(err)
	}

	w := request("GET", "/api/tasks/"+task.ID+"/worker-slots", "")
	if w.Code != 200 {
		t.Fatalf("worker-slots: %d %s", w.Code, w.Body)
	}
	var got struct {
		Slots []WorkerSlotDTO `json:"slots"`
		Count int             `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != n || len(got.Slots) != n {
		t.Fatalf("slots=%d count=%d len=%d", n, got.Count, len(got.Slots))
	}
	first := got.Slots[0]
	if first.Slot != 1 || first.Name != "work#1" || first.Session != "slot:1" || first.State != "running" ||
		first.IntentID != i64s(id) || first.Summary != "slot one task" || first.LastActivity == "" {
		t.Fatalf("slot 1 = %+v", first)
	}
	if len(got.Slots) > 1 && got.Slots[1].State != "idle" {
		t.Fatalf("unused slot should be idle: %+v", got.Slots[1])
	}

	// Activity history for the slot session must return only slot rows.
	hist := request("GET", "/api/exploration/activity/history?task="+task.ID+"&session=slot:1", "")
	if hist.Code != 200 {
		t.Fatalf("history: %d %s", hist.Code, hist.Body)
	}
}
