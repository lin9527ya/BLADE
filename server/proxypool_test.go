package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustNormalizePool(t *testing.T, c ProxyPoolConfig) ProxyPoolConfig {
	t.Helper()
	n, err := NormalizeProxyPoolConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func waitPool(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestNormalizeProxyPoolConfig(t *testing.T) {
	cfg, err := NormalizeProxyPoolConfig(ProxyPoolConfig{
		Candidates: []string{"http://a:1, socks5://b:2\n# 注释\nhttp://a:1", "https://u:p@c:3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Candidates) != 3 {
		t.Fatalf("候选归一/去重不对：%v", cfg.Candidates)
	}
	if cfg.HealthURL == "" || cfg.Timeout <= 0 || cfg.Interval <= 0 || cfg.FailThreshold <= 0 {
		t.Fatalf("默认值没补齐：%+v", cfg)
	}
	if _, err := NormalizeProxyPoolConfig(ProxyPoolConfig{Candidates: []string{"ftp://x"}}); err == nil {
		t.Fatal("不支持的协议应被拒收")
	}
	if _, err := NormalizeProxyPoolConfig(ProxyPoolConfig{Candidates: []string{"http://a:1"}, HealthURL: "not-a-url"}); err == nil {
		t.Fatal("非法健康检查 URL 应被拒收")
	}
}

// TestProxyPoolFailoverFailClosedAndRecovery 钉住核心语义：
//   - 配了候选时启动先是"阻断"，第一个健康候选出现后才放行；
//   - active 连续失败达阈值 → 切下一个；
//   - 全挂 → active=阻断地址（不是空串/直连）；
//   - 恢复后自动切回。
func TestProxyPoolFailoverFailClosedAndRecovery(t *testing.T) {
	cfg := mustNormalizePool(t, ProxyPoolConfig{
		Candidates: []string{"http://a:1", "http://b:2"}, HealthURL: "https://health.example/",
		Timeout: 50 * time.Millisecond, Interval: time.Hour, FailThreshold: 2, Cooldown: 0,
	})
	var mu sync.Mutex
	health := map[string]bool{"http://a:1": true, "http://b:2": true}
	var applied []string
	p := newProxyPool(cfg, func(_ context.Context, proxy, _ string, _ time.Duration) error {
		mu.Lock()
		ok := health[proxy]
		mu.Unlock()
		if ok {
			return nil
		}
		return errors.New("down")
	}, func(active string) {
		mu.Lock()
		applied = append(applied, active)
		mu.Unlock()
	})
	p.Start()
	defer p.Close()

	waitPool(t, "首个健康候选生效", func() bool { return p.Active() == "http://a:1" })

	// a 失败一次：还不到阈值，保持。
	mu.Lock()
	health["http://a:1"] = false
	mu.Unlock()
	p.check()
	if p.Active() != "http://a:1" {
		t.Fatalf("未达阈值就切换了：%s", p.Active())
	}
	// 达到阈值 → 切到 b。
	p.check()
	if p.Active() != "http://b:2" {
		t.Fatalf("应切到 b，实际 %s", p.Active())
	}

	// b 也挂 → 阻断，绝不能是空串（空串=直连）。
	mu.Lock()
	health["http://b:2"] = false
	mu.Unlock()
	p.check()
	p.check()
	if p.Active() != blockedProxyURL {
		t.Fatalf("全挂时应阻断（%s），实际 %q", blockedProxyURL, p.Active())
	}

	// a 恢复 → 从阻断切回。
	mu.Lock()
	health["http://a:1"] = true
	mu.Unlock()
	p.check()
	if p.Active() != "http://a:1" {
		t.Fatalf("恢复后应切回 a，实际 %s", p.Active())
	}

	mu.Lock()
	defer mu.Unlock()
	for _, a := range applied {
		if a == "" {
			t.Fatalf("代理池在配置了候选时绝不应切换到直连：%v", applied)
		}
	}
}

func TestProxyPoolEmptyMeansDirect(t *testing.T) {
	cfg := mustNormalizePool(t, ProxyPoolConfig{})
	var applied []string
	p := newProxyPool(cfg, func(context.Context, string, string, time.Duration) error { return nil }, func(a string) { applied = append(applied, a) })
	p.Start()
	defer p.Close()
	if p.Active() != "" {
		t.Fatalf("未配置代理应直连（Active 为空），实际 %q", p.Active())
	}
	if len(applied) != 1 || applied[0] != "" {
		t.Fatalf("应只应用一次直连：%v", applied)
	}
}

// TestProbeHTTPProxyNoDirectFallback：代理不可达时探测必须失败，且**不能**绕过代理
// 直接访问目标（否则健康检查本身就泄漏真实 IP）。
func TestProbeHTTPProxyNoDirectFallback(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	if err := probeHTTPProxy(context.Background(), "http://127.0.0.1:1", target.URL, 150*time.Millisecond); err == nil {
		t.Fatal("代理不可达时探测应失败")
	}
	if hits.Load() != 0 {
		t.Fatal("探测绕过了代理、直接打到了目标：真实 IP 会泄漏")
	}
}

// TestProbeHTTPProxyGoesThroughProxy：正常路径必须经代理（HTTP 代理收到绝对 URI）。
func TestProbeHTTPProxyGoesThroughProxy(t *testing.T) {
	var viaProxy atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.IsAbs() { // proxy-form request
			viaProxy.Store(true)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()

	if err := probeHTTPProxy(context.Background(), proxy.URL, "http://health.example/", time.Second); err != nil {
		t.Fatal(err)
	}
	if !viaProxy.Load() {
		t.Fatal("请求没有经过代理")
	}
}
