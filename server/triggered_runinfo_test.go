package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lin9527ya/blade/agent"
)

// buildTaskDomainReg must produce a registry whose tools are bound to the task's
// exploration store, not the server-level nil-store copy.
func TestBuildTaskDomainRegBindsTask(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	task, err := s.m.CreateTask("triggered-domain-reg", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.DeleteTask(task.ID, DeleteTaskOptions{}) })

	reg := buildTaskDomainReg(s.m.pg, task.ExpID, 1)
	if len(reg) == 0 {
		t.Fatal("task domain registry is empty")
	}
	record, ok := reg["record_fact"]
	if !ok {
		t.Skip("record_fact not in this build's domain tool set")
	}
	// With a bound exploration store the tool must not answer "需要任务上下文".
	res, err := record.Call(agent.WithRunInfo(context.Background(), agent.RunInfo{
		TaskID: 1, ExplorationID: task.ExpID,
	}), json.RawMessage(`{"summary":"triggered run fact"}`), nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("task-bound record_fact should succeed: %+v", res.Content)
	}
}
