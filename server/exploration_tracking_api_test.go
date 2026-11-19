package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestSchedulingAndLeadTraceAPIAreTaskScoped(t *testing.T) {
	s, f, request := trafficEvidenceServer(t)
	var taskID int64
	if err := s.m.pg.QueryRow(`SELECT t.id FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE n.id=$1`, f.NodeID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	task, _ := s.m.Task(fmt.Sprint(taskID))
	if task == nil {
		t.Fatal("missing fixture task")
	}
	fact, err := task.Store.AddNode(db.KindFact, map[string]any{"summary": "branches", "leads": []map[string]string{{"text": "direction"}}}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := task.Store.AddIntentGraph(map[string]any{"summary": "direction"}, 5, nil, "planner", []db.IntentEdge{{From: fact, Rel: db.RelDerivedFrom}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
	schedule := request(http.MethodGet, fmt.Sprintf("/api/tasks/%d/scheduling?intent_id=%d", taskID, id), "")
	if schedule.Code != 200 {
		t.Fatalf("schedule=%d %s", schedule.Code, schedule.Body.String())
	}
	var report taskSchedulingReport
	if err := json.Unmarshal(schedule.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.GateReason != "task_paused" || len(report.Rows) != 1 || !report.Rows[0].Evaluated {
		t.Fatal("task gate confused with candidate score")
	}
	leads := request(http.MethodGet, fmt.Sprintf("/api/tasks/%d/leads?limit=1", taskID), "")
	if leads.Code != 200 {
		t.Fatalf("leads=%d %s", leads.Code, leads.Body.String())
	}
	var trace db.LeadTraceReport
	if err := json.Unmarshal(leads.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.Stats.Total != 1 || trace.Stats.Adopted != 1 {
		t.Fatalf("bad trace %+v", trace)
	}
	other, err := s.m.CreateTask("other scheduling fixture", "g", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.DeleteTask(tidOfTask(other)) })
	cross := request(http.MethodGet, fmt.Sprintf("/api/tasks/%s/scheduling?intent_id=%d", other.ID, id), "")
	if cross.Code != 404 {
		t.Fatal("intent from another task leaked")
	}
	for _, suffix := range []string{"/leads?state=made_up", "/leads?offset=-1", "/scheduling?limit=1000"} {
		if rec := request(http.MethodGet, fmt.Sprintf("/api/tasks/%d%s", taskID, suffix), ""); rec.Code != 400 {
			t.Fatalf("invalid input accepted %s: %d", suffix, rec.Code)
		}
	}
}
