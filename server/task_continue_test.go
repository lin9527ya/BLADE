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

// 终态任务的「继续运行」也要认未达成目标：policy_blocked（策略停摆）收尾时目标通常
// 仍是 open，停摆只说明上一轮规划走不下去，不等于目标作废。只认同一轮遗留的 open
// 意图会让这类任务永远无法从 UI 复活（任务 18 的现场）。
func TestTerminalTaskResumeRevivesOpenGoals(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("terminal resume goals", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteTask(tid)
	if err := m.SetTaskStatus(task.ID, "policy_blocked"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 不启动后台 planner/worker，只验证准入与状态迁移
	s := &Server{m: m, engine: NewEngine(m), ctx: ctx}

	if _, err := s.applyTaskControl(task, "resume"); err == nil || !strings.Contains(err.Error(), "未达成的目标") {
		t.Fatalf("没有遗留工作（含未达成目标）时必须拒绝并说明原因: %v", err)
	}
	if !isTerminalStatus(m.TaskStatus(task.ID)) {
		t.Fatalf("refused resume must not touch the status: %s", m.TaskStatus(task.ID))
	}

	if _, err := task.Store.AddGoal(map[string]any{"text": "拿下后台管理权限"}, "human"); err != nil {
		t.Fatal(err)
	}
	res, err := s.applyTaskControl(task, "resume")
	if err != nil {
		t.Fatalf("resume with an open goal: %v", err)
	}
	if isTerminalStatus(m.TaskStatus(task.ID)) {
		t.Fatalf("task still terminal after resume: status=%s result=%+v", m.TaskStatus(task.ID), res)
	}
}
