package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/blade/traffic"
)

// 全局出口代理池：多个代理 URL（Clash 的 http/socks5 混合端口即可），按健康检查
// 自动切换。核心约束是**绝不直连**：配置了候选时 active 永不为空——全部失败就切到
// 一个 loopback 关闭端口（`blockedProxyURL`），让流量失败，而不是泄漏真实 IP。
//
// 探测本身也经候选代理完成（Transport.Proxy 显式设置，不读 NO_PROXY、没有直连分支）。

const (
	// blockedProxyURL 是"全部代理不可用"时的黑洞：本机关闭端口，连接立即失败。
	// 它必须是一个合法的代理 URL（会喂给 MITM 的 upstream），而不是空串——空串=直连。
	blockedProxyURL = "http://127.0.0.1:9"

	defaultProxyHealthURL      = "https://www.gstatic.com/generate_204"
	defaultProxyHealthTimeout  = 5 * time.Second
	defaultProxyHealthInterval = 30 * time.Second
	defaultProxyFailThreshold  = 2
	defaultProxySwitchCooldown = 30 * time.Second
	maxProxyCandidates         = 20
)

// ProxyPoolConfig 是代理池的完整配置（设置页保存 / Manager 加载）。
type ProxyPoolConfig struct {
	Candidates []string
	HealthURL  string
	Timeout    time.Duration
	Interval   time.Duration
	Cooldown   time.Duration
	// FailThreshold 是 active 连续失败几次后切换。
	FailThreshold int
	// configured 表示用户确实配了代理（哪怕全是非法项）。用于"配置了但不合法"时
	// fail-closed，而不是悄悄退回直连。
	configured bool
}

// ProxyHealth 是一个候选的最近探测结果（设置页展示）。
type ProxyHealth struct {
	Proxy     string    `json:"proxy"`
	OK        bool      `json:"ok"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

// ProxyPoolStatus 是设置页要的运行时状态。
type ProxyPoolStatus struct {
	Active     string        `json:"active"`
	Blocked    bool          `json:"blocked"`
	Candidates []ProxyHealth `json:"candidates"`
}

// splitProxyCandidates 解析设置里的代理列表：换行或逗号分隔，忽略空行与 # 注释。
func splitProxyCandidates(raw string) []string {
	out := make([]string, 0, 4)
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	}) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// NormalizeProxyPoolConfig 校验并归一配置。非法代理直接报错（设置页应当拒绝保存），
// 而不是悄悄丢掉——丢掉后用户以为配了，实际流量可能走了别的出口。
func NormalizeProxyPoolConfig(c ProxyPoolConfig) (ProxyPoolConfig, error) {
	out := ProxyPoolConfig{
		HealthURL:     strings.TrimSpace(c.HealthURL),
		Timeout:       c.Timeout,
		Interval:      c.Interval,
		Cooldown:      c.Cooldown,
		FailThreshold: c.FailThreshold,
	}
	seen := map[string]bool{}
	for _, raw := range c.Candidates {
		for _, line := range splitProxyCandidates(raw) {
			u, err := traffic.ValidateProxyURL(line)
			if err != nil {
				return out, err
			}
			key := u.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			out.Candidates = append(out.Candidates, key)
			if len(out.Candidates) >= maxProxyCandidates {
				break
			}
		}
	}
	if out.HealthURL == "" {
		out.HealthURL = defaultProxyHealthURL
	}
	hu, err := url.Parse(out.HealthURL)
	if err != nil || hu.Host == "" || (hu.Scheme != "http" && hu.Scheme != "https") {
		return out, fmt.Errorf("健康检查 URL 必须是 http(s) 绝对地址：%q", out.HealthURL)
	}
	if out.Timeout <= 0 {
		out.Timeout = defaultProxyHealthTimeout
	}
	if out.Interval <= 0 {
		out.Interval = defaultProxyHealthInterval
	}
	if out.Cooldown < 0 {
		out.Cooldown = 0
	}
	if out.FailThreshold <= 0 {
		out.FailThreshold = defaultProxyFailThreshold
	}
	// 夹逼到合理范围，避免误配把探测变成洪水或永久不切。
	if out.Timeout > 60*time.Second {
		out.Timeout = 60 * time.Second
	}
	if out.Interval < 5*time.Second {
		out.Interval = 5 * time.Second
	}
	if out.Interval > 10*time.Minute {
		out.Interval = 10 * time.Minute
	}
	if out.Cooldown > 10*time.Minute {
		out.Cooldown = 10 * time.Minute
	}
	if out.FailThreshold > 10 {
		out.FailThreshold = 10
	}
	out.configured = len(out.Candidates) > 0
	return out, nil
}

type proxyProbeFunc func(ctx context.Context, proxyURL, healthURL string, timeout time.Duration) error

// ProxyPool 维护候选代理、active 与每个候选的健康状态。
type ProxyPool struct {
	cfg   ProxyPoolConfig
	probe proxyProbeFunc
	apply func(active string)

	mu         sync.Mutex
	active     string
	failures   int
	lastSwitch time.Time
	health     map[string]ProxyHealth
	stop       chan struct{}
	wg         sync.WaitGroup
}

func newProxyPool(cfg ProxyPoolConfig, probe proxyProbeFunc, apply func(string)) *ProxyPool {
	if probe == nil {
		probe = probeHTTPProxy
	}
	if apply == nil {
		apply = func(string) {}
	}
	return &ProxyPool{cfg: cfg, probe: probe, apply: apply, health: map[string]ProxyHealth{}}
}

// Start 启动探测循环。配了候选时**先把出口置为阻断**，第一次探测成功后才放行，
// 避免启动窗口直连；没配候选（用户显式选择直连）则直接置空。
func (p *ProxyPool) Start() {
	p.mu.Lock()
	if p.stop != nil {
		p.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	p.stop = stop
	if len(p.cfg.Candidates) == 0 {
		p.active = ""
		p.mu.Unlock()
		if p.cfg.configured {
			// 配了但全是非法项：fail-closed（正常保存路径会拒绝非法项，这里兜手工改库）。
			log.Printf("[proxy] 代理配置存在但无有效候选，出口已阻断（不会直连）")
			p.apply(blockedProxyURL)
			return
		}
		p.apply("")
		return
	}
	p.active = blockedProxyURL
	p.mu.Unlock()
	p.apply(blockedProxyURL)
	p.wg.Add(1)
	go p.run(stop)
}

func (p *ProxyPool) run(stop <-chan struct{}) {
	defer p.wg.Done()
	p.check()
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			p.check()
		}
	}
}

// check 探测 active；失败达到阈值后按顺序找下一个健康的；全部失败则阻断。
func (p *ProxyPool) check() {
	p.mu.Lock()
	candidates := append([]string(nil), p.cfg.Candidates...)
	active := p.active
	p.mu.Unlock()
	if len(candidates) == 0 {
		return
	}

	probe := func(proxy string) (bool, time.Duration, error) {
		// 每个候选独立超时：探测多个候选时不能共用一个 deadline，否则后面的候选
		// 会因为前面的耗时被"秒判失败"。
		ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
		defer cancel()
		start := time.Now()
		err := p.probe(ctx, proxy, p.cfg.HealthURL, p.cfg.Timeout)
		return err == nil, time.Since(start), err
	}

	if active != "" && active != blockedProxyURL {
		ok, latency, err := probe(active)
		p.record(active, ok, latency, err)
		p.mu.Lock()
		if ok {
			p.failures = 0
			p.mu.Unlock()
			return
		}
		p.failures++
		failures := p.failures
		cooldownPassed := time.Since(p.lastSwitch) >= p.cfg.Cooldown
		p.mu.Unlock()
		if failures < p.cfg.FailThreshold {
			return
		}
		if !cooldownPassed {
			// 防抖：刚切过就再等等，避免两个抖动的代理来回跳。
			return
		}
	}

	// active 已阻断，或连续失败达到阈值：按顺序探测其它候选（从 active 之后开始，
	// 避免每次都先撞一个刚失败的）。全部失败 → 阻断（不是直连）。
	best := ""
	for _, cand := range rotateCandidates(candidates, active) {
		ok, latency, err := probe(cand)
		p.record(cand, ok, latency, err)
		if ok {
			best = cand
			break
		}
	}
	if best == "" {
		p.switchTo(blockedProxyURL)
		return
	}
	p.switchTo(best)
}

func rotateCandidates(candidates []string, active string) []string {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]string, 0, len(candidates))
	start := 0
	for i, c := range candidates {
		if c == active {
			start = (i + 1) % len(candidates)
			break
		}
	}
	for i := 0; i < len(candidates); i++ {
		out = append(out, candidates[(start+i)%len(candidates)])
	}
	return out
}

func (p *ProxyPool) record(proxy string, ok bool, latency time.Duration, err error) {
	h := ProxyHealth{Proxy: proxy, OK: ok, LatencyMS: latency.Milliseconds(), CheckedAt: time.Now()}
	if err != nil {
		h.Error = clipProxyError(err)
	}
	p.mu.Lock()
	p.health[proxy] = h
	p.mu.Unlock()
}

func clipProxyError(err error) string {
	s := strings.TrimSpace(err.Error())
	if len([]rune(s)) > 200 {
		s = string([]rune(s)[:200]) + "…"
	}
	return s
}

func (p *ProxyPool) switchTo(target string) {
	p.mu.Lock()
	if p.active == target {
		p.mu.Unlock()
		return
	}
	previous := p.active
	p.active = target
	p.failures = 0
	p.lastSwitch = time.Now()
	p.mu.Unlock()

	if target == blockedProxyURL {
		log.Printf("[proxy] 全部代理不可用（原 %s），出口已阻断：目标流量会失败，**不会直连**", previous)
	} else {
		log.Printf("[proxy] 出口切换：%s → %s", previous, target)
	}
	p.apply(target)
}

// Active 返回当前出口。配了候选时永不为空（全挂=blockedProxyURL）；没配=空（直连）。
func (p *ProxyPool) Active() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.cfg.Candidates) == 0 {
		if p.cfg.configured {
			return blockedProxyURL
		}
		return ""
	}
	if p.active == "" {
		return blockedProxyURL
	}
	return p.active
}

// Status 给设置页展示。
func (p *ProxyPool) Status() ProxyPoolStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := ProxyPoolStatus{Active: p.active, Blocked: p.active == blockedProxyURL}
	// 只回**已经探测过**的候选：设置页据此区分"正在检测"与"确实全挂"，
	// 刚保存、第一次探测还没回来时不会先闪一下红色的"全部不可用"。
	for _, c := range p.cfg.Candidates {
		if h, ok := p.health[c]; ok {
			st.Candidates = append(st.Candidates, h)
		}
	}
	return st
}

// Config 返回当前生效配置（设置页回显）。
func (p *ProxyPool) Config() ProxyPoolConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.cfg
	c.Candidates = append([]string(nil), p.cfg.Candidates...)
	return c
}

// Close 停止探测循环。
func (p *ProxyPool) Close() {
	p.mu.Lock()
	stop := p.stop
	p.stop = nil
	p.mu.Unlock()
	if stop != nil {
		close(stop)
		p.wg.Wait()
	}
}

// probeHTTPProxy 经候选代理访问健康检查 URL。Transport.Proxy 显式指向该代理：
// 代理挂了请求就失败，**没有直连回退**（不读 NO_PROXY，也不用 ProxyFromEnvironment）。
// HTTP 代理用 CONNECT 域名、SOCKS5 由代理侧解析域名，目标 DNS 不在本机泄漏。
func probeHTTPProxy(ctx context.Context, proxyURL, healthURL string, timeout time.Duration) error {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return err
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyURL(u),
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
	client := &http.Client{Transport: tr, Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	// 407（代理认证失败）与 5xx 视为代理不可用；其它状态码说明链路是通的
	// （健康检查 URL 本身返回 403/404 不代表代理挂了）。
	if resp.StatusCode == http.StatusProxyAuthRequired || resp.StatusCode >= 500 {
		return fmt.Errorf("代理返回 HTTP %d", resp.StatusCode)
	}
	return nil
}
