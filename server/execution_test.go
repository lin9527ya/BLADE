package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
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

	st, err := svc.Submit(ctx, task, 0, "work#1", "printf 'hello-blade\\n'; sleep 5", "测试：部分输出", 0)
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
	if !strings.Contains(mid.Tail, "hello-blade") {
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
	if err != nil || !strings.Contains(string(body), "hello-blade") {
		t.Fatalf("output file content: %v %q", err, string(body))
	}
}

// TestExecutionServiceInheritsAgentShellEnvAndCwd pins the P13 shell contract: a
// hosted command runs in the intent's work dir (relative artifacts land where the
// agent expects them) and carries the env the resolver hands it — the recording
// proxy + CA, without which a long scan bypasses the MITM and fails TLS.
func TestExecutionServiceInheritsAgentShellEnvAndCwd(t *testing.T) {
	svc, _, td, task := newExecFixture(t)
	svc.SetEnvResolver(func() []string { return []string{"BLADE_EXEC_TEST_ENV=proxied"} })
	st, err := svc.Submit(context.Background(), task, 7, "work#1",
		`pwd; printf 'env=%s\n' "$BLADE_EXEC_TEST_ENV"`, "环境与目录", 0)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	fin := waitTerminal(t, svc, st.ID, 15*time.Second)
	if fin.Status != db.ExecCompleted {
		t.Fatalf("status=%s want completed: %+v", fin.Status, fin)
	}
	wantDir := filepath.Join(td, "tasks", task, "i7")
	if !strings.Contains(fin.Tail, wantDir) {
		t.Fatalf("长任务必须在意图工作目录里跑 %q，实际输出 %q", wantDir, fin.Tail)
	}
	if !strings.Contains(fin.Tail, "env=proxied") {
		t.Fatalf("托管执行必须继承注入的环境（记录代理/CA）: %q", fin.Tail)
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
	// 对账有 2 分钟宽限（覆盖 Submit→Start 与刚重启的窗口）：把「上一进程遗留」设老。
	if _, err := pg.Exec(`UPDATE tool_executions SET created_at=now()-interval '5 minutes' WHERE id=$1`, left); err != nil {
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

// TestExecutionServiceSubmitToolLabel：cve_exploit 的台账标签必须与 run_execution
// 区分，审计/界面才能看出这是一次库内脚本执行。
func TestExecutionServiceSubmitToolLabel(t *testing.T) {
	svc, pg, _, task := newExecFixture(t)
	ctx := context.Background()
	st, err := svc.SubmitTool(ctx, task, 0, "work#1", "cve_exploit", "printf 'cve-ok\\n'", "CVE 测试", 0)
	if err != nil {
		t.Fatalf("submit tool: %v", err)
	}
	final := waitTerminal(t, svc, st.ID, 10*time.Second)
	if final.Status != db.ExecCompleted || !strings.Contains(final.Tail, "cve-ok") {
		t.Fatalf("final: %+v", final)
	}
	row, err := pg.GetExecution(st.ID)
	if err != nil || row == nil || row.Tool != "cve_exploit" {
		t.Fatalf("ledger tool label: %+v %v", row, err)
	}
	if _, err := svc.SubmitTool(ctx, task, 0, "work#1", "Bad-Label", "true", "", 0); err == nil {
		t.Fatal("invalid tool label must be rejected")
	}
}

// TestExecutionServiceStageScript：脚本落在任务 cve 子目录，服务端生成随机文件名，
// 0640 可读；内容原样落盘。
func TestExecutionServiceStageScript(t *testing.T) {
	svc, _, _, task := newExecFixture(t)
	ctx := context.Background()
	content := []byte("#!/bin/sh\necho staged\n")
	path, err := svc.StageScript(ctx, task, 0, "cve-2098-0001-abcdef.sh", content)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !filepath.IsAbs(path) || !strings.Contains(path, filepath.Join("tasks", task, "cve")) {
		t.Fatalf("bad staged path: %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(content) {
		t.Fatalf("staged content: %q %v", got, err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("staged mode: %v %v", fi, err)
	}
}

// countProcs 数 /proc 里 cmdline 含 needle 的进程（用于断言孤儿是否真被杀掉）。
func countProcs(t *testing.T, needle string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	n := 0
	for _, e := range entries {
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || len(b) == 0 {
			continue
		}
		if strings.Contains(strings.ReplaceAll(string(b), "\x00", " "), needle) {
			n++
		}
	}
	return n
}

// run_execution 正常退出时也必须清场进程组：`sh -c 'scanner &'` 会让 shell 先退出、
// 后台子进程留在同一进程组里。只 Wait 不收尾的话它们就是孤儿——没有 TTL、没人引用，
// 一直跑下去，累积起来能把机器 CPU 吃满。
func TestExecutionServiceNormalExitKillsLeftoverGroup(t *testing.T) {
	svc, _, _, task := newExecFixture(t)
	tag := "blade-orphan-a-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	// 子进程重定向了输出 → 不占管道，shell 立刻退出，只剩它留在进程组里。
	cmd := "sh -c 'sleep 120' " + tag + " >/dev/null 2>&1 & echo shell-done"
	st, err := svc.Submit(context.Background(), task, 0, "work#1", cmd, "孤儿清理", 0)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	fin := waitTerminal(t, svc, st.ID, 15*time.Second)
	if fin.Status != db.ExecCompleted {
		t.Fatalf("status=%s want completed", fin.Status)
	}
	// 允许一点调度延迟，然后断言后台子进程已被整组杀掉。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if countProcs(t, tag) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("正常退出后进程组未清理，仍有 %d 个后台子进程存活", countProcs(t, tag))
}

// 后台子进程继承了 stdout（未重定向）时，它会一直握着写端：没有 WaitDelay 的话
// cmd.Wait 永远不返回，台账卡在 running、收尾门永远悬着——而 shell 早就退出了。
// 这里钉住「不卡死 + 子进程被清理 + 状态是 completed 而不是 failed」。
func TestExecutionServiceChildHoldingStdoutDoesNotWedge(t *testing.T) {
	svc, _, _, task := newExecFixture(t)
	tag := "blade-orphan-b-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	cmd := "sh -c 'sleep 120' " + tag + " & echo shell-done"
	st, err := svc.Submit(context.Background(), task, 0, "work#1", cmd, "占管道", 0)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	fin := waitTerminal(t, svc, st.ID, 20*time.Second)
	if fin.Status != db.ExecCompleted {
		t.Fatalf("status=%s want completed（WaitDelay 到点收尾，不应误报 failed）", fin.Status)
	}
	if !strings.Contains(fin.Tail, "shell-done") {
		t.Fatalf("输出丢失: %q", fin.Tail)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if countProcs(t, tag) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("占管道的后台子进程未被清理，仍有 %d 个存活", countProcs(t, tag))
}

// 停机必须整组杀掉在跑的长任务并落 cancelled：托管进程是 Setsid 出来的，不随服务退出
// 而死；不杀的话每次重启都会留下一批还在烧 CPU 的扫描器。
func TestExecutionServiceShutdownKillsLiveGroup(t *testing.T) {
	svc, pg, _, task := newExecFixture(t)
	tag := "blade-orphan-c-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	st, err := svc.Submit(context.Background(), task, 0, "work#1",
		"sh -c 'sleep 120' "+tag+" >/dev/null 2>&1 & sleep 120", "停机清理", 0)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if countProcs(t, tag) == 0 {
		t.Fatal("前置条件不成立：后台子进程没起来")
	}
	if n := svc.Shutdown(agent.AbortShutdown.Text, 2*time.Second); n != 1 {
		t.Fatalf("shutdown 应报告 1 条在跑执行，得到 %d", n)
	}
	row, err := pg.GetExecution(st.ID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if row.Status != db.ExecCancelled {
		t.Fatalf("status=%s want cancelled（停机是明确停止，不是 orphaned）", row.Status)
	}
	if countProcs(t, tag) != 0 {
		t.Fatalf("停机后仍有 %d 个后台子进程存活", countProcs(t, tag))
	}
}
