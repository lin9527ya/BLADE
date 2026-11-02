package server

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 目标连接持续不可达（超时/DNS/代理网关错误）连续两次，必须自动切换出口——
// 出口 IP 被目标侧防火墙静默丢弃时，代理端口本身健康，代理池健康检查发现不了。
func TestObserveEgressRotatesOnRepeatedUnreachable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	if err := m.pg.SetSetting("clash_api_url", "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting("clash_auto_rotate", "true"); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("egress observe", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(tid)
	e := NewEngine(m)
	rotated := make(chan string, 4)
	e.rotateEgressHook = func(context.Context, string) (ClashRotation, error) {
		rotated <- "called"
		return ClashRotation{Group: "G", From: "A", To: "B", EgressIP: "203.0.113.9"}, nil
	}
	dead := db.Activity{Kind: "tool_result",
		Detail: "curl: (28) Failed to connect to https://t.example/ port 443 after 21000 ms: Connection timed out"}

	e.observeEgress(task, dead)
	select {
	case <-rotated:
		t.Fatal("单次连接失败不该切换出口")
	case <-time.After(300 * time.Millisecond):
	}

	e.observeEgress(task, dead)
	select {
	case <-rotated:
	case <-time.After(3 * time.Second):
		t.Fatal("连续两次不可达应自动切换出口")
	}
	time.Sleep(50 * time.Millisecond) // 让切换后的活动落库
}

// WAF 触发仍然有效（不能被不可达逻辑挤掉）。
func TestObserveEgressRotatesOnWAF(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	if err := m.pg.SetSetting("clash_api_url", "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting("clash_auto_rotate", "true"); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("egress waf", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(tid)
	e := NewEngine(m)
	rotated := make(chan string, 4)
	e.rotateEgressHook = func(_ context.Context, reason string) (ClashRotation, error) {
		rotated <- reason
		return ClashRotation{Group: "G", From: "A", To: "B", EgressIP: "203.0.113.9"}, nil
	}
	waf := db.Activity{Kind: "tool_result", Detail: "HTTP 403 https://t.example/ Just a moment... cloudflare"}
	e.observeEgress(task, waf)
	e.observeEgress(task, waf)
	select {
	case reason := <-rotated:
		if !strings.Contains(reason, "WAF") {
			t.Fatalf("WAF 触发的切换原因应说明 WAF：%s", reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("连续两次 WAF 特征应自动切换出口")
	}
	time.Sleep(50 * time.Millisecond)
}
