package mcphttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 并发上限：满员时第三个调用要**排队等槽**，释放后继续（不是直接失败）。
func TestGuardConcurrencyCap(t *testing.T) {
	g := newGuard()
	cfg := GuardConfig{MaxConcurrent: 2, BreakerThreshold: 100, BreakerCooldown: time.Second}
	ctx := context.Background()
	if err := g.acquire(ctx, "cap-test", cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.acquire(ctx, "cap-test", cfg); err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() { got <- g.acquire(ctx, "cap-test", cfg) }()
	select {
	case err := <-got:
		t.Fatalf("第三个调用应排队而不是立即返回（err=%v）", err)
	case <-time.After(50 * time.Millisecond):
	}
	g.release()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("释放槽位后应拿到槽: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("释放槽位后仍在等待")
	}
	if st := g.status("cap-test", cfg); st.Inflight != 2 {
		t.Fatalf("占用计数不对: %+v", st)
	}
	// ctx 取消时等待者要立刻返回（不永远挂着）。
	blocked := make(chan error, 1)
	cctx, cancel := context.WithCancel(context.Background())
	go func() { blocked <- g.acquire(cctx, "cap-test", cfg) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("ctx 取消后不该拿到槽")
		}
	case <-time.After(time.Second):
		t.Fatal("ctx 取消后仍在等待")
	}
	// 清零占用（不影响其他用例：server 名唯一）。
	g.release()
	g.release()
}

// 熔断：连续失败达阈值 → 立刻失败（不空等）→ 冷却后放行 → 成功后恢复。
func TestGuardBreakerOpensAndFailsFast(t *testing.T) {
	g := newGuard()
	const server = "breaker-test"
	cfg := GuardConfig{MaxConcurrent: 1, BreakerThreshold: 3, BreakerCooldown: 120 * time.Millisecond}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		g.failure(fmt.Errorf("boom %d", i), cfg)
	}
	if st := g.status(server, cfg); st.Open || st.ConsecutiveFailures != 2 {
		t.Fatalf("未达阈值不该开闸: %+v", st)
	}
	g.failure(fmt.Errorf("boom 3"), cfg)
	st := g.status(server, cfg)
	if !st.Open || st.OpenRemainingSec < 0 {
		t.Fatalf("达阈值应开闸: %+v", st)
	}
	start := time.Now()
	err := g.acquire(ctx, server, cfg)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("熔断期内不该放行")
	}
	if elapsed > 30*time.Millisecond {
		t.Fatalf("熔断应立刻失败而不是等超时（耗时 %s）", elapsed)
	}
	for _, want := range []string{"熔断冷却", server, "boom 3", "手动重置"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("熔断错误话术应含 %q: %v", want, err)
		}
	}
	time.Sleep(140 * time.Millisecond) // 等冷却结束
	if err := g.acquire(ctx, server, cfg); err != nil {
		t.Fatalf("冷却后应放行一次真实调用: %v", err)
	}
	g.release()
	g.success()
	if st := g.status(server, cfg); st.Open || st.ConsecutiveFailures != 0 {
		t.Fatalf("成功后应恢复: %+v", st)
	}
}

// 成功清零连续失败；失败计数在开闸后重新开始（冷却结束再失败一轮才重新开闸）。
func TestGuardSuccessResetsFailures(t *testing.T) {
	g := newGuard()
	cfg := GuardConfig{MaxConcurrent: 0, BreakerThreshold: 3, BreakerCooldown: time.Second}
	g.failure(fmt.Errorf("x"), cfg)
	g.failure(fmt.Errorf("x"), cfg)
	g.success()
	if st := g.status("reset-test", cfg); st.ConsecutiveFailures != 0 {
		t.Fatalf("成功后应清零: %+v", st)
	}
	if err := g.acquire(context.Background(), "reset-test", cfg); err != nil {
		t.Fatalf("不该开闸: %v", err)
	}
	g.release()
}

// 熔断只由**传输层失败**计数：server 答了但 RPC 报错（比如工具名写错）不算"不健康"。
func TestClientBreakerCountsTransportNotRPCErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"fake","version":"1"}}}`, req.ID)
		case "tools/call":
			// RPC 层错误：server 明确答复"这个工具不存在"。
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unknown tool"}}`, req.ID)
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
		}
	}))
	defer srv.Close()

	SetGuardConfigResolver(func(string) GuardConfig {
		return GuardConfig{MaxConcurrent: 4, BreakerThreshold: 2, BreakerCooldown: time.Second}
	})
	t.Cleanup(func() { SetGuardConfigResolver(nil) })

	const server = "rpc-vs-transport"
	cl, err := New(context.Background(), server, srv.URL, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	for i := 0; i < 5; i++ {
		if _, err := cl.Call(context.Background(), "nope", map[string]any{}); err == nil {
			t.Fatal("RPC 错误应向上返回")
		}
	}
	st := cl.Guard()
	if st.Open {
		t.Fatalf("RPC 层错误不该开熔断: %+v", st)
	}
	// 传输层失败（5xx）→ 计数并开闸，之后调用立刻失败且**不再打服务端**。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()
	const server2 = "transport-fail"
	SetGuardConfigResolver(func(string) GuardConfig {
		return GuardConfig{MaxConcurrent: 4, BreakerThreshold: 2, BreakerCooldown: 500 * time.Millisecond}
	})
	// 连接就会失败（initialize 也走守卫）——连两次达阈值后，第三次必须是"立刻失败"。
	for i := 0; i < 2; i++ {
		if _, err := New(context.Background(), server2, bad.URL, nil, false); err == nil {
			t.Fatal("坏服务应连接失败")
		}
	}
	before := atomic.LoadInt32(&calls)
	start := time.Now()
	_, err = New(context.Background(), server2, bad.URL, nil, false)
	if err == nil || !strings.Contains(err.Error(), "熔断冷却") {
		t.Fatalf("熔断期内应立刻失败并说明: %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("熔断应不发起网络请求: 耗时 %s", time.Since(start))
	}
	if got := atomic.LoadInt32(&calls); got != before {
		t.Fatalf("熔断期内不该再打服务端（%d → %d）", before, got)
	}
	// 手动重置 → 恢复真实调用（会再打服务端并失败，但不再是熔断话术）。
	ResetGuard(server2)
	if _, err := New(context.Background(), server2, bad.URL, nil, false); err == nil ||
		strings.Contains(err.Error(), "熔断冷却") {
		t.Fatalf("重置后应恢复真实调用: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got == before {
		t.Fatal("重置后应真的打了服务端")
	}
}

// 状态快照：UI 靠它显示"熔断中（剩余 Ns）"。
func TestGuardStatusesAndReset(t *testing.T) {
	SetGuardConfigResolver(func(string) GuardConfig {
		return GuardConfig{MaxConcurrent: 3, BreakerThreshold: 1, BreakerCooldown: 5 * time.Second}
	})
	t.Cleanup(func() { SetGuardConfigResolver(nil) })
	g := guardFor("status-test-server")
	g.failure(fmt.Errorf("down"), guardConfigFor("status-test-server"))
	found := false
	for _, st := range GuardStatuses() {
		if st.Server == "status-test-server" {
			found = true
			if !st.Open || st.MaxConcurrent != 3 || st.OpenRemainingSec <= 0 || st.LastError != "down" {
				t.Fatalf("状态快照不对: %+v", st)
			}
		}
	}
	if !found {
		t.Fatal("GuardStatuses 应包含有状态的 server")
	}
	ResetGuard("status-test-server")
	for _, st := range GuardStatuses() {
		if st.Server == "status-test-server" && st.Open {
			t.Fatalf("重置后不该还开着: %+v", st)
		}
	}
}
