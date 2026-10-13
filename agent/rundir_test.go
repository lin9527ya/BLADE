package agent

import (
	"path/filepath"
	"strconv"
	"testing"
)

// 一次运行（worker 的一条意图 / planner 的一轮 / 主 agent / 对话）产生的所有东西都
// 落在同一个根下——这是 P17 的布局契约：产物同根，任务删除时的文件清理、备份、
// 人工排查才只需要面对一个目录，而不是"任务目录 + $TMPDIR"两套。
func TestRunArtifactsShareOneRoot(t *testing.T) {
	base := t.TempDir()
	runDir := ensureRunDir(base, 7, 3)
	wantRun := filepath.Join(base, "tasks", strconv.Itoa(7), "i3")
	if runDir != wantRun {
		t.Fatalf("run dir = %q, want %q", runDir, wantRun)
	}
	for _, sub := range []string{cmdOutDir(runDir), taskOutDir(runDir), filepath.Join(runDir, "executions"), filepath.Join(runDir, "compaction")} {
		if filepath.Dir(sub) != runDir {
			t.Fatalf("%q 不在运行根 %q 下", sub, runDir)
		}
	}
	// planner/main 用任务级目录（intentID<=0）。
	if got := ensureRunDir(base, 7, 0); got != filepath.Join(base, "tasks", "7") {
		t.Fatalf("task-level run dir = %q", got)
	}
	// 后台任务输出目录名固定，便于清理与排查时按名字找。
	if filepath.Base(taskOutDir("/x/y")) != "task-output" {
		t.Fatalf("task output dir name = %q", filepath.Base(taskOutDir("/x/y")))
	}
}
