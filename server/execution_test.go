package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// newExecFixture builds the P13 execution service over the test database and a
// temp data dir, and cleans up the rows this test owns.
func newExecFixture(t *testing.T) (*ExecutionService, *db.DB, string, string) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	td := t.TempDir()
	task := "exec-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() {
		_, _ = pg.Exec(`DELETE FROM tool_executions WHERE task_id=$1`, task)
		pg.Close()
	})
	return NewExecutionService(pg, td), pg, td, task
}

// waitTerminal polls until the row leaves the active set (or the deadline passes).
func waitTerminal(t *testing.T, svc *ExecutionService, id int64, within time.Duration) agent.ExecStatus {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		st, err := svc.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get %d: %v", id, err)
		}
		if !agent.ExecActive(st.Status) || time.Now().After(deadline) {
			return st
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestExecutionServiceSubmitAndWait pins the core contract: a submitted command is
// hosted detached with an ABSOLUTE output path on disk, wait returns a still-running
// snapshot WITHOUT an error at its deadline (超时是观察不是失败), and a later wait
// returns the finished run with its output captured.
func TestExecutionServiceSubmitAndWait(t *testing.T) {
	svc, _, _, task := newExecFixture(t)
	ctx := context.Background()

	st, err := svc.Submit(ctx, task, 0, "work#1", "printf 'hello-artex\\n'; sleep 5", "测试：部分输出", 0)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if st.ID <= 0 || st.Status != db.ExecRunning {
		t.Fatalf("submit snapshot wrong: %+v", st)
	}
	if !filepath.IsAbs(st.OutputPath) {
		t.Fatalf("output path must be absolute: %q", st.OutputPath)
	}
	if !strings.Contains(st.OutputPath, filepath.Join("tasks", task, "executions")) {
		t.Fatalf("output path not under the task's executions dir: %q", st.OutputPath)
	}
	if _, err := os.Stat(st.OutputPath); err != nil {
		t.Fatalf("output file missing: %v", err)
	}

	// 有界等待：到点仍是 running —— 这不是错误，是观察。
	mid, err := svc.Wait(ctx, st.ID, 400*time.Millisecond)
	if err != nil {
		t.Fatalf("wait at deadline must not error: %v", err)
	}
	if mid.Status != db.ExecRunning {
		t.Fatalf("expected still-running snapshot, got %+v", mid)
	}
	if !strings.Contains(mid.Tail, "hello-artex") {
		t.Fatalf("partial output not surfaced: %q", mid.Tail)
	}

	done, err := svc.Wait(ctx, st.ID, 15*time.Second)
	if err != nil {
		t.Fatalf("second wait: %v", err)
	}
	if done.Status != db.ExecCompleted || done.ExitCode != 0 {
		t.Fatalf("terminal snapshot wrong: %+v", done)
	}
	if done.OutputBytes == 0 {
		t.Fatalf("output bytes not accounted: %+v", done)
	}
	body, err := os.ReadFile(st.OutputPath)
	if err != nil || !strings.Contains(string(body), "hello-artex") {
		t.Fatalf("output file content: %v %q", err, string(body))
	}
}

// TestExecutionServiceCancelKillsProcessGroupAndRecordsReason pins cancel: the
// reason lands in the ledger (and comes back to the caller), the process group is
// killed for real (the command never reaches its trailing marker), and the row is
// terminal — cancelled, not failed.
func TestExecutionServiceCancelKillsProcessGroupAndRecordsReason(t *testing.T) {
	svc, _, td, task := newExecFixture(t)
	ctx := context.Background()
	marker := filepath.Join(td, "should-not-exist")
	st, err := svc.Submit(ctx, task, 0, "work#1", "sleep 6; echo late > "+marker, "测试：取消", 0)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	ok, err := svc.Cancel(ctx, st.ID, "方向判断错误，改用 ffuf")
	if err != nil || !ok {
		t.Fatalf("cancel: ok=%v err=%v", ok, err)
	}
	fin := waitTerminal(t, svc, st.ID, 10*time.Second)
	if fin.Status != db.ExecCancelled {
		t.Fatalf("status=%s want cancelled", fin.Status)
	}
	if fin.CancelNote != "方向判断错误，改用 ffuf" {
		t.Fatalf("cancel reason must be recorded verbatim: %q", fin.CancelNote)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("process group survived cancel: trailing marker was written")
	}
	// 已终态再取消 = 无事可做，而不是错误。
	if ok, err := svc.Cancel(ctx, st.ID, "再来一次"); err != nil || ok {
		t.Fatalf("re-cancel of a terminal row: ok=%v err=%v want false,nil", ok, err)
	}
}

// TestExecutionServiceHardTimeout pins the hard cap: past timeout_ms the process
// group is killed and the row is hard_timeout with the cause's explanation — the
// distinction a reader needs to tell "到点被杀" from "跑失败".
func TestExecutionServiceHardTimeout(t *testing.T) {
	svc, _, _, task := newExecFixture(t)
	st, err := svc.Submit(context.Background(), task, 0, "work#1", "sleep 30", "测试：硬超时", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	fin := waitTerminal(t, svc, st.ID, 15*time.Second)
	if fin.Status != db.ExecHardTimeout {
		t.Fatalf("status=%s want hard_timeout (%+v)", fin.Status, fin)
	}
	if !strings.Contains(fin.Error, "hard_timeout") {
		t.Fatalf("hard-timeout explanation missing: %q", fin.Error)
	}
}

// TestExecutionServiceOrphanReconcile pins restart handling: an in-flight row this
// process does not own is marked orphaned (NOT failed — nothing was observed),
// while a row this service is actively hosting is left running.
func TestExecutionServiceOrphanReconcile(t *testing.T) {
	svc, pg, _, task := newExecFixture(t)
	ctx := context.Background()
	// 模拟上一进程遗留的活跃行：直接写库，服务内存里没有它。
	left, err := pg.CreateExecution(&db.ToolExecution{TaskID: task, Worker: "work#1", Tool: "run_execution", Command: "nuclei -u https://x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.MarkExecutionRunning(left); err != nil {
		t.Fatal(err)
	}
	live, err := svc.Submit(ctx, task, 0, "work#1", "sleep 4", "仍在跑", 0)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if n := svc.ReconcileOrphaned(); n < 1 {
		t.Fatalf("reconcile marked %d rows, want >=1", n)
	}
	got, _ := pg.GetExecution(left)
	if got == nil || got.Status != db.ExecOrphaned {
		t.Fatalf("leftover row: %+v want orphaned", got)
	}
	if got.Status == db.ExecFailed {
		t.Fatal("orphaned must never be recorded as failed")
	}
	if !strings.Contains(got.Error, "orphaned") && !strings.Contains(got.Error, "重启") {
		t.Fatalf("orphaned row must explain itself: %q", got.Error)
	}
	if cur, _ := pg.GetExecution(live.ID); cur == nil || cur.Status != db.ExecRunning {
		t.Fatalf("hosted row must stay running: %+v", cur)
	}
	_ = svc.CancelAll(task, "测试收尾")
}

// TestExecutionServiceCancelAllStopsEverything pins the task-stop path: every
// in-flight execution of one task is asked to stop and ends terminal.
func TestExecutionServiceCancelAllStopsEverything(t *testing.T) {
	svc, pg, _, task := newExecFixture(t)
	ctx := context.Background()
	a, err := svc.Submit(ctx, task, 0, "work#1", "sleep 20", "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Submit(ctx, task, 0, "work#2", "sleep 20", "b", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := svc.CancelAll(task, "任务已停止，执行被取消"); n != 2 {
		t.Fatalf("CancelAll stopped %d, want 2", n)
	}
	for _, id := range []int64{a.ID, b.ID} {
		fin := waitTerminal(t, svc, id, 10*time.Second)
		if fin.Status != db.ExecCancelled {
			t.Fatalf("execution %d status=%s want cancelled", id, fin.Status)
		}
		if !strings.Contains(fin.CancelNote, "任务已停止") {
			t.Fatalf("execution %d lost the task-level reason: %q", id, fin.CancelNote)
		}
	}
	if rows, err := pg.ListActiveExecutions(task); err != nil || len(rows) != 0 {
		t.Fatalf("still active after CancelAll: %d (err=%v)", len(rows), err)
	}
}
