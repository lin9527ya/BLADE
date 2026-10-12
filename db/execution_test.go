package db

import (
	"strconv"
	"testing"
)

// TestExecutionLedgerLifecycle pins the ledger's read/write surface: a row walks
// queued → running → completed, the tail is persisted, the absolute output path
// survives, and the "ran" counter (the negative-evidence gate's criterion) counts
// only terminal rows — a queued/running execution is not evidence yet.
func TestExecutionLedgerLifecycle(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	task := strconv.FormatInt(taskID, 10)
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM tool_executions WHERE task_id=$1`, task) })
	st := d.Exploration(expID)
	intent, err := st.AddIntent(map[string]any{"summary": "execution ledger"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	id, err := d.CreateExecution(&ToolExecution{
		TaskID: task, IntentID: intent, Worker: "work#1", Tool: "run_execution",
		Command: "nuclei -u https://x.example", Description: "全站扫描",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetExecutionOutputPath(id, "/data/tasks/"+task+"/executions/"+strconv.FormatInt(id, 10)+".log"); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkExecutionRunning(id); err != nil {
		t.Fatal(err)
	}
	if err := d.TouchExecutionTail(id, "partial output", 14); err != nil {
		t.Fatal(err)
	}

	// In flight: active on both axes, empty "ran" count.
	if rows, err := d.ListActiveExecutionsByIntent(intent); err != nil || len(rows) != 1 {
		t.Fatalf("active by intent: %v %d", err, len(rows))
	}
	if rows, err := d.ListActiveExecutions(task); err != nil || len(rows) != 1 {
		t.Fatalf("active by task: %v %d", err, len(rows))
	}
	if n, err := d.CountRanExecutions(intent); err != nil || n != 0 {
		t.Fatalf("ran count while running = %d (err=%v), want 0", n, err)
	}

	code := 0
	if err := d.FinishExecution(id, ExecCompleted, code, 4096, "final tail", ""); err != nil {
		t.Fatal(err)
	}
	row, err := d.GetExecution(id)
	if err != nil || row == nil {
		t.Fatalf("get: %v %v", row, err)
	}
	if row.Status != ExecCompleted || row.ExitCode == nil || *row.ExitCode != 0 {
		t.Fatalf("terminal row wrong: %+v", row)
	}
	if row.OutputBytes != 4096 || row.PartialTail != "final tail" {
		t.Fatalf("output accounting lost: %+v", row)
	}
	if row.Description != "全站扫描" || row.OutputPath == "" || row.StartedAt == nil || row.EndedAt == nil {
		t.Fatalf("metadata lost: %+v", row)
	}
	if ExecutionActive(row.Status) || !ExecutionTerminal(row.Status) {
		t.Fatalf("status classification wrong for %s", row.Status)
	}
	if n, err := d.CountRanExecutions(intent); err != nil || n != 1 {
		t.Fatalf("ran count after completion = %d (err=%v), want 1", n, err)
	}
	if rows, err := d.ListActiveExecutionsByIntent(intent); err != nil || len(rows) != 0 {
		t.Fatalf("active after completion: %v %d", err, len(rows))
	}
	if rows, err := d.ListExecutionsByIntent(intent, 10); err != nil || len(rows) != 1 {
		t.Fatalf("list by intent: %v %d", err, len(rows))
	}
}

// TestReconcileOrphanedExecutionsIsNotFailed pins the restart contract: an in-flight
// row with no live process becomes orphaned (nothing was observed about its outcome),
// NEVER failed; rows the live set still owns are left alone.
func TestReconcileOrphanedExecutionsIsNotFailed(t *testing.T) {
	d, taskID, _ := coverageMatrixFixture(t)
	task := strconv.FormatInt(taskID, 10)
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM tool_executions WHERE task_id=$1`, task) })
	dead, err := d.CreateExecution(&ToolExecution{TaskID: task, Worker: "work#1", Tool: "run_execution", Command: "ffuf -w list"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MarkExecutionRunning(dead); err != nil {
		t.Fatal(err)
	}
	alive, err := d.CreateExecution(&ToolExecution{TaskID: task, Worker: "work#1", Tool: "run_execution", Command: "nmap -sV"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MarkExecutionRunning(alive); err != nil {
		t.Fatal(err)
	}
	done, err := d.CreateExecution(&ToolExecution{TaskID: task, Worker: "work#1", Tool: "run_execution", Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.FinishExecution(done, ExecCompleted, 0, 0, "", ""); err != nil {
		t.Fatal(err)
	}

	n, err := d.ReconcileOrphanedExecutions(map[int64]bool{alive: true}, "执行已中断（服务重启或进程丢失）")
	if err != nil || n != 1 {
		t.Fatalf("reconcile: n=%d err=%v want 1", n, err)
	}
	got, _ := d.GetExecution(dead)
	if got == nil || got.Status != ExecOrphaned {
		t.Fatalf("dead row: %+v want orphaned", got)
	}
	if got.Error == "" {
		t.Fatal("orphaned row must carry why it was marked")
	}
	if got, _ := d.GetExecution(alive); got == nil || got.Status != ExecRunning {
		t.Fatalf("live row must stay running: %+v", got)
	}
	if got, _ := d.GetExecution(done); got == nil || got.Status != ExecCompleted {
		t.Fatalf("terminal row must stay untouched: %+v", got)
	}
	// 幂等：第二次对账没有新的活跃行，不会再动任何东西。
	if n, err := d.ReconcileOrphanedExecutions(map[int64]bool{alive: true}, "x"); err != nil || n != 0 {
		t.Fatalf("second reconcile: n=%d err=%v want 0", n, err)
	}
}
