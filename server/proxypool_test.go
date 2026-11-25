package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

func TestParseEgressIP(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4\n":            "1.2.3.4",
		"ip=5.6.7.8\nloc=CN\n": "5.6.7.8",
		`{"ip":"9.10.11.12"}`:  "9.10.11.12",
		"  2001:db8::1  ":      "2001:db8::1",
	}
	for in, want := range cases {
		got, err := parseEgressIP([]byte(in))
		if err != nil || got != want {
			t.Errorf("parseEgressIP(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	if _, err := parseEgressIP([]byte("not an ip")); err == nil {
		t.Fatal("非 IP 响应应报错")
	}
}

// TestCheckProxyEgressIPsUsesProxyAndFlagsLocalLeak：出口 IP 查询必须经代理；如果返回的
// 是代理自己返回的"本机网卡地址"，要标 leaked（说明流量可能没真的出代理）。
func TestCheckProxyEgressIPsUsesProxyAndFlagsLocalLeak(t *testing.T) {
	var hits atomic.Int32
	body := "1.2.3.4"
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.IsAbs() {
			hits.Add(1)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer proxy.Close()

	res := checkProxyEgressIPs(context.Background(), []string{proxy.URL}, "http://ip.example/", time.Second)
	if len(res) != 1 || res[0].IP != "1.2.3.4" || res[0].Leaked || res[0].Error != "" {
		t.Fatalf("出口 IP 结果不对：%+v", res)
	}
	if hits.Load() == 0 {
		t.Fatal("出口 IP 查询没有经过代理")
	}

	// 代理返回本机地址（127.0.0.1 在 lo 上）→ 标记泄漏。
	body = "127.0.0.1"
	res = checkProxyEgressIPs(context.Background(), []string{proxy.URL}, "http://ip.example/", time.Second)
	if len(res) != 1 || !res[0].Leaked {
		t.Fatalf("本机地址应标记 leaked：%+v", res)
	}
}

// TestProbeEgressIPNoDirectFallback：代理不可达时不能直连查询服务。
func TestProbeEgressIPNoDirectFallback(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("1.2.3.4"))
	}))
	defer target.Close()
	if _, err := probeEgressIP(context.Background(), "http://127.0.0.1:1", target.URL, 150*time.Millisecond); err == nil {
		t.Fatal("代理不可达时出口 IP 查询应失败")
	}
	if hits.Load() != 0 {
		t.Fatal("出口 IP 查询绕过了代理，直连了查询服务")
	}
}

// 首探未完成时 WaitReady 必须等：否则重启后 fail-closed 的黑洞会被快照进
// 整轮 BashEnv（任务 26 实测：平台 1 秒后已恢复，这一轮却永远连不上）。
func TestProxyPoolWaitReadyBlocksUntilFirstProbe(t *testing.T) {
	cfg := mustNormalizePool(t, ProxyPoolConfig{
		Candidates: []string{"http://a:1"}, HealthURL: "https://health.example/",
		Timeout: 2 * time.Second, Interval: time.Hour,
	})
	release := make(chan struct{})
	p := newProxyPool(cfg, func(ctx context.Context, proxy, _ string, _ time.Duration) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, func(string) {})
	p.Start()
	defer p.Close()

	short, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	p.WaitReady(short)
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("首探未完成时 WaitReady 不应立即返回（实际 %s）", elapsed)
	}

	close(release)
	ctx, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	p.WaitReady(ctx)
	if got := p.Active(); got != "http://a:1" {
		t.Fatalf("首探完成后应放行健康候选，实际 %q", got)
	}
}

// 未配置代理（显式直连）时没有探测循环，WaitReady 必须立即返回。
func TestProxyPoolWaitReadyNoCandidates(t *testing.T) {
	cfg := mustNormalizePool(t, ProxyPoolConfig{})
	p := newProxyPool(cfg, nil, func(string) {})
	p.Start()
	defer p.Close()
	p.WaitReady(context.Background()) // 不阻塞即通过
}

// 健康检查/出口 IP 的 URL 是用户可配的：没有拨号守卫时把它指向环回地址，
// 平台就会替用户去连内网。这里钉住"拨号阶段按真实 IP 拒绝"。
func TestProbeDialGuardBlocksLoopbackAndLinkLocal(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80", "169.254.169.254:80"} {
		if err := blockProbeInternalDial("tcp", addr, nil); err == nil {
			t.Fatalf("%s 应被拨号守卫拒绝", addr)
		}
	}
	// RFC1918 放行：内网自建的健康检查服务是常见部署。
	for _, addr := range []string{"10.0.0.5:80", "192.168.1.10:443"} {
		if err := blockProbeInternalDial("tcp", addr, nil); err != nil {
			t.Fatalf("%s 不该被拒：%v", addr, err)
		}
	}
}

// 跨主机重定向必须拒绝：否则"校验时公网、实际连内网"又成立了。
func TestProbeRedirectPolicyRejectsCrossHost(t *testing.T) {
	first := &http.Request{URL: mustURL(t, "https://health.example/204")}
	next := &http.Request{URL: mustURL(t, "http://169.254.169.254/latest/meta-data")}
	if err := probeRedirectPolicy(next, []*http.Request{first}); err == nil {
		t.Fatal("跨主机重定向应被拒绝")
	}
	same := &http.Request{URL: mustURL(t, "https://health.example/other")}
	if err := probeRedirectPolicy(same, []*http.Request{first}); err != nil {
		t.Fatalf("同主机重定向应放行：%v", err)
	}
	// 最多 3 跳。
	via := []*http.Request{first, first, first}
	if err := probeRedirectPolicy(same, via); err == nil {
		t.Fatal("超过 3 跳应被拒绝")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestProxyPoolAppliesLatestSwitchAfterSlowPreviousApply(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var applied []string
	p := newProxyPool(ProxyPoolConfig{}, nil, func(target string) {
		if target == "http://a:1" {
			close(started)
			<-release
		}
		mu.Lock()
		applied = append(applied, target)
		mu.Unlock()
	})
	p.active = ""
	doneA := make(chan struct{})
	go func() { p.switchTo("http://a:1"); close(doneA) }()
	<-started
	doneB := make(chan struct{})
	go func() { p.switchTo("http://b:1"); close(doneB) }()
	close(release)
	<-doneA
	<-doneB
	p.mu.Lock()
	got := p.active
	p.mu.Unlock()
	if got != "http://b:1" {
		t.Fatalf("active=%q", got)
	}
	mu.Lock()
	last := applied[len(applied)-1]
	mu.Unlock()
	if last != "http://b:1" {
		t.Fatalf("stale apply won: %v", applied)
	}
}

func TestNormalizeProxyPoolLimitsAndMultiHealthAndStrategy(t *testing.T) {
	// Rejects >20 candidates instead of silently dropping entries.
	var many []string
	for i := 1; i <= maxProxyCandidates+1; i++ {
		many = append(many, "http://127.0.0.1:"+strconv.Itoa(10000+i))
	}
	if _, err := NormalizeProxyPoolConfig(ProxyPoolConfig{Candidates: many}); err == nil {
		t.Fatal("超过 maxProxyCandidates 时应拒绝保存而不是静默截断")
	}

	// Rejects invalid strategy.
	if _, err := NormalizeProxyPoolConfig(ProxyPoolConfig{Strategy: "random"}); err == nil {
		t.Fatal("未知调度策略应拒绝")
	}

	// Supports multiline/comma fallback HealthURLs and preserves socks5h.
	cfg, err := NormalizeProxyPoolConfig(ProxyPoolConfig{
		Candidates: []string{"socks5h://u:p@127.0.0.1:7891"},
		HealthURL:  "https://a.example/204, https://b.example/204\n# comment\nhttps://a.example/204",
		Strategy:   "fastest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HealthURLs) != 2 || cfg.Candidates[0] != "socks5h://u:p@127.0.0.1:7891" || cfg.Strategy != ProxyStrategyFastest {
		t.Fatalf("normalized config mismatch: %+v", cfg)
	}
}

func TestProxyPoolConcurrentProbingAndFullCandidateStatus(t *testing.T) {
	cfg := mustNormalizePool(t, ProxyPoolConfig{
		Candidates: []string{"http://slow1:1", "http://slow2:2", "http://fast3:3"},
		Timeout:    time.Second, Interval: time.Hour,
	})
	p := newProxyPool(cfg, func(ctx context.Context, proxy, _ string, _ time.Duration) error {
		if proxy != "http://fast3:3" {
			select {
			case <-time.After(350 * time.Millisecond):
				return errors.New("timeout")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, nil)
	start := time.Now()
	p.Start()
	defer p.Close()

	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.WaitReady(waitCtx)
	if elapsed := time.Since(start); elapsed >= 700*time.Millisecond {
		t.Fatalf("并发探测耗时长于单轮并发上限：%s", elapsed)
	}
	if p.Active() != "http://fast3:3" {
		t.Fatalf("应切到第3个健康候选，实际 %q", p.Active())
	}
	st := p.Status()
	if len(st.Candidates) != 3 {
		t.Fatalf("所有 3 个候选都应有最近健康记录，实际 %d: %+v", len(st.Candidates), st.Candidates)
	}
}

func TestProxyPoolOrderedFailbackFastFailoverAndPin(t *testing.T) {
	cfg := mustNormalizePool(t, ProxyPoolConfig{
		Candidates: []string{"http://p1:1", "http://p2:2", "http://p3:3"},
		HealthURL:  "https://primary.example/204\nhttps://backup.example/204",
		Timeout:    200 * time.Millisecond, Interval: time.Hour, FailThreshold: 3, Cooldown: 0, Strategy: ProxyStrategyOrdered,
	})
	var mu sync.Mutex
	errFor := map[string]error{}
	primaryDown := false
	p := newProxyPool(cfg, func(_ context.Context, proxy, healthURL string, _ time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		if primaryDown && healthURL == "https://primary.example/204" {
			return errors.New("primary health url timeout")
		}
		return errFor[proxy]
	}, nil)
	p.Start()
	defer p.Close()
	waitPool(t, "ordered init", func() bool { return p.Active() == "http://p1:1" })

	// 单个健康检查 URL 挂了但备用 HealthURL 存活：不应误判阻断。
	mu.Lock()
	primaryDown = true
	mu.Unlock()
	p.check()
	if p.Active() != "http://p1:1" {
		t.Fatalf("备用 HealthURL 可达时不应切走，实际 %q", p.Active())
	}

	// p1 触发确定性代理端口拒绝（connection refused）：立刻快切到 p2，不死等 3 次。
	mu.Lock()
	errFor["http://p1:1"] = errors.New("proxyconnect tcp: dial tcp p1:1: connection refused")
	mu.Unlock()
	p.check()
	if p.Active() != "http://p2:2" {
		t.Fatalf("确定性 proxy_connect 错误应立即快切到 p2，实际 %q", p.Active())
	}

	// p1 恢复：需连续 defaultProxyRecoverThreshold(2) 轮健康才平滑回切。
	mu.Lock()
	errFor["http://p1:1"] = nil
	mu.Unlock()
	p.check()
	if p.Active() != "http://p2:2" {
		t.Fatalf("第 1 次恢复后不应立即抖动回切，实际 %q", p.Active())
	}
	p.check()
	if p.Active() != "http://p1:1" {
		t.Fatalf("连续两次健康后应回切主节点 p1，实际 %q", p.Active())
	}

	// 手动固定失效节点被拒绝；固定健康节点 p3 成功并切换。
	mu.Lock()
	errFor["http://p2:2"] = errors.New("407 Proxy Authentication Required")
	mu.Unlock()
	if err := p.Pin(context.Background(), "http://p2:2"); err == nil {
		t.Fatal("固定不可用节点应返回错误")
	}
	if err := p.Pin(context.Background(), "http://p3:3"); err != nil {
		t.Fatalf("固定健康节点失败: %v", err)
	}
	if p.Active() != "http://p3:3" || p.Status().Pinned != "http://p3:3" {
		t.Fatalf("固定 p3 后状态不对: %+v", p.Status())
	}

	// ReloadConfig 新增候选且保留 p3 时，不得闪断为 blockedProxyURL。
	reloaded := mustNormalizePool(t, ProxyPoolConfig{
		Candidates: []string{"http://p1:1", "http://p2:2", "http://p3:3", "http://p4:4"},
		Interval:   time.Hour,
	})
	p.ReloadConfig(reloaded)
	if p.Active() == blockedProxyURL {
		t.Fatal("ReloadConfig 保留活跃节点时不应出现黑洞闪断")
	}
}

func TestProxyPoolFastestStrategyHysteresis(t *testing.T) {
	cfg := mustNormalizePool(t, ProxyPoolConfig{
		Candidates: []string{"http://p1:1", "http://p2:2"},
		Timeout:    time.Second, Interval: time.Hour, Cooldown: 0, Strategy: ProxyStrategyFastest,
	})
	var mu sync.Mutex
	delays := map[string]time.Duration{
		"http://p1:1": 2 * time.Millisecond,
		"http://p2:2": 120 * time.Millisecond,
	}
	p := newProxyPool(cfg, func(_ context.Context, proxy, _ string, _ time.Duration) error {
		mu.Lock()
		d := delays[proxy]
		mu.Unlock()
		time.Sleep(d)
		return nil
	}, nil)
	p.Start()
	defer p.Close()
	waitPool(t, "fastest init", func() bool { return p.Active() == "http://p1:1" })

	// 反转延迟，使 p2 远快于 p1（触发 >=50ms & >=30% 门槛 + 2 轮确认）。
	mu.Lock()
	delays["http://p1:1"] = 120 * time.Millisecond
	delays["http://p2:2"] = 1 * time.Millisecond
	mu.Unlock()
	for i := 0; i < 6 && p.Active() != "http://p2:2"; i++ {
		p.check()
	}
	if p.Active() != "http://p2:2" {
		t.Fatalf("fastest 策略应在持续显著更低延迟后切到 p2，实际 %q (%+v)", p.Active(), p.Status())
	}
}
