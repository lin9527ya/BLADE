package server

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// TestTerminalTaskResumeRevivesPendingIntents pins the 「继续运行」 contract for a
// finished task: with never-executed (open) intents left in the frontier, control
// resume revives it (running or queued — no longer terminal); with nothing to run
// it refuses and points at the alternative instead of flip-flopping to done.
func TestTerminalTaskResumeRevivesPendingIntents(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("terminal resume", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteTask(tid)
	if err := m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 不启动后台 planner/worker，只验证准入与状态迁移
	s := &Server{m: m, engine: NewEngine(m), ctx: ctx}

	if _, err := s.applyTaskControl(task, "resume"); err == nil || !strings.Contains(err.Error(), "set_goals") {
		t.Fatalf("resume without pending work must refuse with guidance: %v", err)
	}
	if !isTerminalStatus(m.TaskStatus(task.ID)) {
		t.Fatalf("refused resume must not touch the status: %s", m.TaskStatus(task.ID))
	}

	if _, err := task.Store.AddIntent(map[string]any{"summary": "遗留方向"}, 5, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	res, err := s.applyTaskControl(task, "resume")
	if err != nil {
		t.Fatalf("resume with pending work: %v", err)
	}
	if isTerminalStatus(m.TaskStatus(task.ID)) {
		t.Fatalf("task still terminal after resume: status=%s result=%+v", m.TaskStatus(task.ID), res)
	}
}
