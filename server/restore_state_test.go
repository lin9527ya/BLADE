package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 启动恢复失败时 readiness 必须失败：任务表读不出来却报健康，会让升级/编排层
// 以为实例可用。响应不能回显原始错误（免认证端点）。
func TestHealthFailsWhenBootRestoreFailed(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	if w := request("GET", "/api/health", ""); w.Code != 200 {
		t.Fatalf("healthy boot: %d %s", w.Code, w.Body)
	}
	s.boot.set(errors.New("database dsn: postgres://secret@host/db"))
	w := request("GET", "/api/health", "")
	if w.Code != 503 {
		t.Fatalf("boot failed: %d %s, want 503", w.Code, w.Body)
	}
	if body := w.Body.String(); len(body) == 0 || strings.Contains(body, "secret") || strings.Contains(body, "postgres://") {
		t.Fatalf("health leaked raw boot error: %s", body)
	}
	s.boot.set(nil)
	if w := request("GET", "/api/health", ""); w.Code != 200 {
		t.Fatalf("after clearing boot error: %d", w.Code)
	}
}

// 普通删除的 journal：启动恢复按「任务是否还在库里」决定回滚还是清掉暂存。
func TestRecoverDeleteStaging(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	m := s.m
	// 仍在库中的任务：staged 文件必须回到原处。
	tasks := m.List()
	if len(tasks) == 0 {
		t.Fatal("no task registered")
	}
	liveID := tasks[0].ID

	src := filepath.Join(m.dir, "tasks", liveID, "keep.txt")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage, err := stageTaskFiles(m.dir, liveID, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃：不 commit/rollback 就重启。
	m.RecoverDeleteStaging()
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("存活任务的文件未回滚：%v", err)
	}
	if _, err := os.Stat(stage.stageDir); !os.IsNotExist(err) {
		t.Fatalf("回滚后暂存目录应被清理：%v", err)
	}

	// 已不存在的任务：暂存目录必须被清掉，而不是永久残留。
	src2 := filepath.Join(m.dir, "tasks", "888888", "gone.txt")
	if err := os.MkdirAll(filepath.Dir(src2), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src2, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage2, err := stageTaskFiles(m.dir, "888888", 0)
	if err != nil {
		t.Fatal(err)
	}
	m.RecoverDeleteStaging()
	if _, err := os.Stat(stage2.stageDir); !os.IsNotExist(err) {
		t.Fatalf("已删除任务的暂存目录应被清理：%v", err)
	}
}
