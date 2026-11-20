package server

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRunWaitsForBootstrapGoalDecomposition(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	task, err := m.CreateTask(fmt.Sprintf("decomposing-%d", time.Now().UnixNano()), "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteTask(tidOfTask(task)) })
	e := NewEngine(m)
	e.BeginDecomposing(task.ID)
	e.Run(context.Background(), task)
	if e.Started(task.ID) {
		t.Fatal("Engine.Run started before goals were persisted")
	}
	if !e.IsDecomposing(task.ID) {
		t.Fatal("decomposing marker lost")
	}
	e.EndDecomposing(task.ID)
	if e.IsDecomposing(task.ID) {
		t.Fatal("decomposing marker not cleared")
	}
}

func TestSetActiveCannotCompleteTaskDuringDecomposition(t *testing.T) {
	s, f, request := trafficEvidenceServer(t)
	_ = f
	task, err := s.m.CreateTask("set-active-decomposing", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.DeleteTask(tidOfTask(task)) })
	s.engine.BeginDecomposing(task.ID)
	t.Cleanup(func() { s.engine.EndDecomposing(task.ID) })
	rec := request("POST", "/api/active", fmt.Sprintf(`{"id":%q}`, task.ID))
	if rec.Code != 200 {
		t.Fatalf("setActive status=%d body=%s", rec.Code, rec.Body.String())
	}
	if s.engine.Started(task.ID) {
		t.Fatal("setActive started the engine during bootstrap decomposition")
	}
	if got := s.m.TaskStatus(task.ID); got == "done" {
		t.Fatal("setActive completed a task whose goals are still decomposing")
	}
}
