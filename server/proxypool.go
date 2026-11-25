package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lin9527ya/blade/traffic"
)

// 全局出口代理池：多个代理 URL（http/https/socks5/socks5h），按并发健康检查与调度策略
// 自动切换。核心约束是**绝不直连**：配置了候选时 active 永不为空——全部失败就切到
// 本机关闭端口（blockedProxyURL），让目标流量失败，而不是泄漏真实 IP。
//
// 探测本身也经候选代理完成（Transport.Proxy 显式设置，不读 NO_PROXY、没有直连分支）。

// ProxyStrategy 定义代理池在多健康节点之间的选路策略。
type ProxyStrategy string

const (
	// ProxyStrategyOrdered 按候选顺序主备优先；高优先级节点恢复并连续健康后平滑回切。
	ProxyStrategyOrdered ProxyStrategy = "ordered"
	// ProxyStrategySticky 粘性故障转移：当前节点可用时不主动切换，失效后顺延下一个。
	ProxyStrategySticky ProxyStrategy = "sticky"
	// ProxyStrategyFastest 基于 EMA 平滑延迟 + 迟滞阈值选择延迟最低的健康节点。
	ProxyStrategyFastest ProxyStrategy = "fastest"
)

// ProxyErrorKind 对探测错误进行分类，以区分确定性的代理层不可用与目标/瞬态超时。
type ProxyErrorKind string

const (
	ProxyErrNone       ProxyErrorKind = ""
	ProxyErrAuth       ProxyErrorKind = "proxy_auth"        // 407 / SOCKS5 认证失败
	ProxyErrConnect    ProxyErrorKind = "proxy_connect"     // 代理端连接被拒/重置/域名解析或握手失败
	ProxyErrTimeout    ProxyErrorKind = "probe_timeout"     // 探测超时
	ProxyErrTargetHTTP ProxyErrorKind = "probe_target_http" // 经代理访问目标返回 5xx 等服务端错误
	ProxyErrOther      ProxyErrorKind = "other"
)

const (
	// blockedProxyURL 是"全部代理不可用"时的黑洞：本机关闭端口，连接立即失败。
	// 它必须是一个合法的代理 URL（会喂给 MITM 的 upstream），而不是空串——空串=直连。
	blockedProxyURL = "http://127.0.0.1:9"

	defaultProxyHealthURL        = "https://www.gstatic.com/generate_204"
	defaultProxyEgressIPURL      = "https://api.ipify.org"
	defaultProxyHealthTimeout    = 5 * time.Second
	defaultProxyHealthInterval   = 30 * time.Second
	defaultProxyFailThreshold    = 2
	defaultProxySwitchCooldown   = 30 * time.Second
	defaultProxyRecoverThreshold = 2
	defaultProxyStrategy         = ProxyStrategyOrdered
	// proxyFirstProbeWait 是 run 启动/每次工具调用等待代理池首探的上限。并发探测下
	// 一轮最坏耗时不超过单节点 Timeout（默认 5s），正常在 ~1s 内放行。
	proxyFirstProbeWait      = 10 * time.Second
	maxProxyCandidates       = 20
	maxProxyHealthURLs       = 5
	maxEgressIPChecks        = 20
	defaultProbeConcurrency  = 6
	egressIPCheckConcurrency = 4
	fastestSwitchRatio       = 0.70
	fastestSwitchMinDeltaMS  = int64(50)
)

// ProxyPoolConfig 是代理池的完整配置（设置页保存 / Manager 加载）。
type ProxyPoolConfig struct {
	Candidates    []string
	HealthURL     string
	HealthURLs    []string
	Timeout       time.Duration
	Interval      time.Duration
	Cooldown      time.Duration
	FailThreshold int
	Strategy      ProxyStrategy
	// configured 表示用户确实配了代理（哪怕全是非法项）。用于"配置了但不合法"时
	// fail-closed，而不是悄悄退回直连。
	configured bool
}

// ProxyHealth 是一个候选的最近探测结果与累计指标（设置页展示）。
type ProxyHealth struct {
	Proxy         string         `json:"proxy"`
	OK            bool           `json:"ok"`
	LatencyMS     int64          `json:"latency_ms,omitempty"`
	AvgLatencyMS  int64          `json:"avg_latency_ms,omitempty"`
	CheckedAt     time.Time      `json:"checked_at"`
	Error         string         `json:"error,omitempty"`
	ErrorKind     ProxyErrorKind `json:"error_kind,omitempty"`
	FailCount     int            `json:"fail_count,omitempty"`
	ConsecutiveOK int            `json:"consecutive_ok,omitempty"`
	Pinned        bool           `json:"pinned,omitempty"`
}

// ProxyPoolStatus 是设置页要的运行时状态。
type ProxyPoolStatus struct {
	Active               string        `json:"active"`
	Blocked              bool          `json:"blocked"`
	Strategy             ProxyStrategy `json:"strategy"`
	Pinned               string        `json:"pinned,omitempty"`
	Probing              bool          `json:"probing"`
	FailThreshold        int           `json:"fail_threshold"`
	CooldownRemainingSec int           `json:"cooldown_remaining_seconds,omitempty"`
	Candidates           []ProxyHealth `json:"candidates"`
}

// splitProxyList 解析多值列表（代理池/健康检查 URL）：支持换行或逗号，忽略空行与 # 注释。
func splitProxyList(raw string) []string {
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

// splitProxyCandidates 是候选列表解析的别名。
func splitProxyCandidates(raw string) []string { return splitProxyList(raw) }

// NormalizeProxyPoolConfig 校验并归一配置。非法代理/超限/非法策略直接报错（设置页应当
// 拒绝保存），而不是悄悄丢掉——丢掉后用户以为配了，实际流量可能走了别的出口。
func NormalizeProxyPoolConfig(c ProxyPoolConfig) (ProxyPoolConfig, error) {
	strategy := ProxyStrategy(strings.ToLower(strings.TrimSpace(string(c.Strategy))))
	if strategy == "" {
		strategy = defaultProxyStrategy
	}
	switch strategy {
	case ProxyStrategyOrdered, ProxyStrategySticky, ProxyStrategyFastest:
	default:
		return ProxyPoolConfig{}, fmt.Errorf("未知代理调度策略 %q（可选 ordered / sticky / fastest）", c.Strategy)
	}

	out := ProxyPoolConfig{
		HealthURL:     strings.TrimSpace(c.HealthURL),
		Timeout:       c.Timeout,
		Interval:      c.Interval,
		Cooldown:      c.Cooldown,
		FailThreshold: c.FailThreshold,
		Strategy:      strategy,
	}
	seen := map[string]bool{}
	for _, raw := range c.Candidates {
		for _, line := range splitProxyList(raw) {
			u, err := traffic.ValidateProxyURL(line)
			if err != nil {
				return out, err
			}
			key := u.String()
			if seen[key] {
				continue
			}
			if len(out.Candidates) >= maxProxyCandidates {
				return out, fmt.Errorf("代理池最多支持 %d 个候选代理", maxProxyCandidates)
			}
			seen[key] = true
			out.Candidates = append(out.Candidates, key)
		}
	}

	urls, err := normalizeHealthURLs(out.HealthURL)
	if err != nil {
		return out, err
	}
	out.HealthURLs = urls
	out.HealthURL = strings.Join(urls, "\n")

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

func normalizeHealthURLs(raw string) ([]string, error) {
	items := splitProxyList(raw)
	if len(items) == 0 {
		return []string{defaultProxyHealthURL}, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		hu, err := url.Parse(item)
		if err != nil || hu.Host == "" || (hu.Scheme != "http" && hu.Scheme != "https") {
			return nil, fmt.Errorf("健康检查 URL 必须是 http(s) 绝对地址：%q", item)
		}
		if seen[item] {
			continue
		}
		if len(out) >= maxProxyHealthURLs {
			return nil, fmt.Errorf("健康检查 URL 最多支持 %d 条", maxProxyHealthURLs)
		}
		seen[item] = true
		out = append(out, item)
	}
	return out, nil
}

type proxyProbeFunc func(ctx context.Context, proxyURL, healthURL string, timeout time.Duration) error

// ProxyPool 维护候选代理、active、固定选择与每个候选的健康状态。
type ProxyPool struct {
	probe proxyProbeFunc
	apply func(active string)

	checkMu sync.Mutex // 串行化完整探测轮次（后台 tick / RequestCheck / CheckNow / Pin）

	mu         sync.Mutex
	cfg        ProxyPoolConfig
	active     string
	pinned     string
	failures   int
	lastSwitch time.Time
	probing    bool
	health     map[string]ProxyHealth
	emaLatency map[string]int64
	stop        chan struct{}
	loopRunning bool
	wg          sync.WaitGroup
	applyMu    sync.Mutex
	switchSeq  uint64
	recheck    chan struct{}
	reload     chan struct{}
	// firstProbe 在第一次探测循环结束后关闭。run 启动时用它等首探完成，
	// 避免把 fail-closed 的启动态（blockedProxyURL）快照进整轮运行环境。
	firstProbe chan struct{}
	firstOnce  sync.Once
}

func newProxyPool(cfg ProxyPoolConfig, probe proxyProbeFunc, apply func(string)) *ProxyPool {
	if probe == nil {
		probe = probeHTTPProxy
	}
	if apply == nil {
		apply = func(string) {}
	}
	if len(cfg.HealthURLs) == 0 && cfg.HealthURL != "" {
		if urls, err := normalizeHealthURLs(cfg.HealthURL); err == nil {
			cfg.HealthURLs = urls
		}
	}
	if len(cfg.HealthURLs) == 0 {
		cfg.HealthURLs = []string{defaultProxyHealthURL}
	}
	if cfg.Strategy == "" {
		cfg.Strategy = defaultProxyStrategy
	}
	return &ProxyPool{
		cfg:        cfg,
		probe:      probe,
		apply:      apply,
		health:     map[string]ProxyHealth{},
		emaLatency: map[string]int64{},
		firstProbe: make(chan struct{}),
		recheck:    make(chan struct{}, 1),
		reload:     make(chan struct{}, 1),
	}
}

// closeFirstProbe 标记首次探测已结束（关一个 channel，幂等）。
func (p *ProxyPool) closeFirstProbe() {
	p.firstOnce.Do(func() { close(p.firstProbe) })
}

// WaitReady 等待首次探测完成（或 ctx 结束）。没有候选时 Start 会立刻关闭该信号，
// 因此这里不会阻塞。
func (p *ProxyPool) WaitReady(ctx context.Context) {
	if p == nil {
		return
	}
	select {
	case <-p.firstProbe:
	case <-ctx.Done():
	}
}

// Start 启动探测循环。配了候选时**先把出口置为阻断**，第一次探测成功后才放行，
// 避免启动窗口直连；没配候选（用户显式选择直连）时不启动后台协程（Zero-leak）。
func (p *ProxyPool) Start() {
	p.mu.Lock()
	if p.stop != nil {
		p.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	p.stop = stop
	hasCandidates := len(p.cfg.Candidates) > 0
	configured := p.cfg.configured
	if !hasCandidates {
		p.active = ""
		if configured {
			p.active = blockedProxyURL
		}
		target := p.active
		p.mu.Unlock()
		p.closeFirstProbe()
		if configured {
			log.Printf("[proxy] 代理配置存在但无有效候选，出口已阻断（不会直连）")
		}
		p.apply(target)
		return
	}
	p.active = blockedProxyURL
	p.loopRunning = true
	p.wg.Add(1)
	p.mu.Unlock()
	p.apply(blockedProxyURL)
	go p.run(stop)
}

func (p *ProxyPool) run(stop <-chan struct{}) {
	p.runLoop(stop, true)
}

func (p *ProxyPool) runLoop(stop <-chan struct{}, runInitial bool) {
	defer func() {
		p.mu.Lock()
		p.loopRunning = false
		p.mu.Unlock()
		p.wg.Done()
	}()
	if runInitial {
		p.checkContext(context.Background())
		p.closeFirstProbe()
	}
	p.mu.Lock()
	interval := p.cfg.Interval
	p.mu.Unlock()
	if interval <= 0 {
		interval = defaultProxyHealthInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-p.reload:
			p.mu.Lock()
			nextInterval := p.cfg.Interval
			hasCandidates := len(p.cfg.Candidates) > 0
			p.mu.Unlock()
			if !hasCandidates {
				return
			}
			if nextInterval > 0 && nextInterval != interval {
				interval = nextInterval
				t.Reset(interval)
			}
			p.checkContext(context.Background())
		case <-p.recheck:
			p.checkContext(context.Background())
		case <-t.C:
			p.checkContext(context.Background())
		}
	}
}

type probeCandidateResult struct {
	proxy   string
	ok      bool
	latency time.Duration
	err     error
	kind    ProxyErrorKind
}

func (p *ProxyPool) check() {
	p.checkContext(context.Background())
}

// checkContext 并发探测所有候选代理并汇总状态，依据当前策略与故障分级决策切线。
func (p *ProxyPool) checkContext(ctx context.Context) {
	p.checkMu.Lock()
	defer p.checkMu.Unlock()

	p.mu.Lock()
	candidates := append([]string(nil), p.cfg.Candidates...)
	healthURLs := append([]string(nil), p.cfg.HealthURLs...)
	timeout := p.cfg.Timeout
	configured := p.cfg.configured
	stopCh := p.stop
	if len(candidates) == 0 {
		p.mu.Unlock()
		if configured {
			p.switchTo(blockedProxyURL)
		} else {
			p.switchTo("")
		}
		return
	}
	p.probing = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.probing = false
		p.mu.Unlock()
	}()

	if len(healthURLs) == 0 {
		healthURLs = []string{defaultProxyHealthURL}
	}

	var cancelWg sync.WaitGroup
	defer cancelWg.Wait()
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if stopCh != nil {
		cancelWg.Add(1)
		go func() {
			defer cancelWg.Done()
			select {
			case <-stopCh:
				cancel()
			case <-probeCtx.Done():
			}
		}()
	}

	results := p.probeAllConcurrently(probeCtx, candidates, healthURLs, timeout)
	resMap := make(map[string]probeCandidateResult, len(results))
	for _, r := range results {
		resMap[r.proxy] = r
	}

	p.mu.Lock()
	// 仅合并仍处于最新配置中的候选结果（防止 check 进行期间发生 ReloadConfig 删改）。
	activeSet := make(map[string]bool, len(p.cfg.Candidates))
	for _, c := range p.cfg.Candidates {
		activeSet[c] = true
	}
	now := time.Now()
	for _, c := range candidates {
		if !activeSet[c] {
			continue
		}
		r := resMap[c]
		p.recordLocked(c, r.ok, r.latency, r.err, r.kind, now)
	}
	target, changed := p.evaluateSwitchLocked(resMap, now)
	p.mu.Unlock()

	if changed {
		p.switchTo(target)
	}
}

func (p *ProxyPool) probeAllConcurrently(ctx context.Context, candidates, healthURLs []string, timeout time.Duration) []probeCandidateResult {
	results := make([]probeCandidateResult, len(candidates))
	workers := len(candidates)
	if workers > defaultProbeConcurrency {
		workers = defaultProbeConcurrency
	}
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, c := range candidates {
		wg.Add(1)
		go func(idx int, proxy string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = probeCandidateResult{proxy: proxy, err: ctx.Err(), kind: ProxyErrTimeout}
				return
			}
			ok, lat, kind, err := p.probeOne(ctx, proxy, healthURLs, timeout)
			results[idx] = probeCandidateResult{proxy: proxy, ok: ok, latency: lat, kind: kind, err: err}
		}(i, c)
	}
	wg.Wait()
	return results
}

func (p *ProxyPool) probeOne(ctx context.Context, proxyURL string, healthURLs []string, timeout time.Duration) (bool, time.Duration, ProxyErrorKind, error) {
	var (
		lastErr  error
		lastKind ProxyErrorKind
		lastLat  time.Duration
	)
	for _, hu := range healthURLs {
		start := time.Now()
		err := p.probe(ctx, proxyURL, hu, timeout)
		lat := time.Since(start)
		if err == nil {
			return true, lat, ProxyErrNone, nil
		}
		kind := classifyProxyError(proxyURL, err)
		lastErr, lastKind, lastLat = err, kind, lat
		// 代理自身的鉴权拒绝或端口拒绝不会因换另一个健康检查 URL 而恢复，立刻返回以便快切。
		if isFastFailoverError(kind) {
			break
		}
	}
	return false, lastLat, lastKind, lastErr
}

func (p *ProxyPool) recordLocked(proxy string, ok bool, latency time.Duration, err error, kind ProxyErrorKind, now time.Time) {
	prev := p.health[proxy]
	latMS := latency.Milliseconds()
	h := ProxyHealth{
		Proxy:        proxy,
		OK:           ok,
		LatencyMS:    latMS,
		AvgLatencyMS: prev.AvgLatencyMS,
		CheckedAt:    now,
		Pinned:       p.pinned != "" && p.pinned == proxy,
	}
	if ok {
		h.ConsecutiveOK = prev.ConsecutiveOK + 1
		h.FailCount = 0
		ema := p.emaLatency[proxy]
		if ema <= 0 {
			ema = latMS
		} else {
			ema = (ema*7 + latMS*3) / 10
			if latMS > 0 && ema == 0 {
				ema = latMS
			}
		}
		p.emaLatency[proxy] = ema
		h.AvgLatencyMS = ema
	} else {
		h.ConsecutiveOK = 0
		h.FailCount = prev.FailCount + 1
		h.ErrorKind = kind
		if err != nil {
			h.Error = clipProxyError(proxy, err)
		}
	}
	p.health[proxy] = h
}

func (p *ProxyPool) evaluateSwitchLocked(resMap map[string]probeCandidateResult, now time.Time) (string, bool) {
	candidates := p.cfg.Candidates
	if len(candidates) == 0 {
		target := ""
		if p.cfg.configured {
			target = blockedProxyURL
		}
		p.failures = 0
		return target, target != p.active
	}

	isHealthy := func(c string) bool {
		if r, ok := resMap[c]; ok {
			return r.ok
		}
		return p.health[c].OK
	}

	activeInPool := containsString(candidates, p.active)
	cooldownPassed := p.cfg.Cooldown <= 0 || p.lastSwitch.IsZero() || now.Sub(p.lastSwitch) >= p.cfg.Cooldown

	// 手动固定节点：固定项健康时无视策略优先级切入/保持；固定项异常时按故障转移逻辑临时降级。
	if p.pinned != "" && containsString(candidates, p.pinned) && isHealthy(p.pinned) {
		if p.active == p.pinned {
			p.failures = 0
			return p.active, false
		}
		if p.active == "" || p.active == blockedProxyURL || !activeInPool || !isHealthy(p.active) || cooldownPassed {
			return p.pinned, true
		}
		return p.active, false
	}

	// 启动态、黑洞阻断态或原活跃节点已被删除：立刻从健康候选中选优。
	if p.active == "" || p.active == blockedProxyURL || !activeInPool {
		p.failures = 0
		best := p.pickBestHealthyLocked(candidates, resMap, "", isHealthy)
		if best == "" {
			best = blockedProxyURL
		}
		return best, best != p.active
	}

	// 当前活跃代理本轮探测健康：检查是否触发高优先级回切或延迟择优。
	if isHealthy(p.active) {
		p.failures = 0
		if !cooldownPassed {
			return p.active, false
		}
		switch p.cfg.Strategy {
		case ProxyStrategyOrdered:
			for _, c := range candidates {
				if c == p.active {
					break
				}
				if isHealthy(c) && p.health[c].ConsecutiveOK >= defaultProxyRecoverThreshold {
					return c, true
				}
			}
		case ProxyStrategyFastest:
			activeEMA := p.effectiveLatencyLocked(p.active)
			if activeEMA > 0 {
				var bestFast string
				bestEMA := activeEMA
				for _, c := range candidates {
					if c == p.active || !isHealthy(c) || p.health[c].ConsecutiveOK < defaultProxyRecoverThreshold {
						continue
					}
					ema := p.effectiveLatencyLocked(c)
					if ema <= 0 {
						continue
					}
					if float64(ema) <= float64(activeEMA)*fastestSwitchRatio && activeEMA-ema >= fastestSwitchMinDeltaMS && ema < bestEMA {
						bestFast, bestEMA = c, ema
					}
				}
				if bestFast != "" {
					return bestFast, true
				}
			}
		}
		return p.active, false
	}

	// 当前活跃代理本轮失败：区分确定性代理错误（快切）与普通/超时错误（阈值 + 冷却防抖）。
	p.failures++
	fastFail := false
	if r, ok := resMap[p.active]; ok && isFastFailoverError(r.kind) {
		for _, c := range candidates {
			if c != p.active && isHealthy(c) {
				fastFail = true
				break
			}
		}
	}
	if !fastFail && (p.failures < p.cfg.FailThreshold || !cooldownPassed) {
		return p.active, false
	}

	best := p.pickBestHealthyLocked(candidates, resMap, p.active, isHealthy)
	if best == "" {
		best = blockedProxyURL
	}
	return best, best != p.active
}

func (p *ProxyPool) effectiveLatencyLocked(proxy string) int64 {
	if ema := p.emaLatency[proxy]; ema > 0 {
		return ema
	}
	return p.health[proxy].LatencyMS
}

func (p *ProxyPool) pickBestHealthyLocked(candidates []string, resMap map[string]probeCandidateResult, skip string, isHealthy func(string) bool) string {
	switch p.cfg.Strategy {
	case ProxyStrategyFastest:
		best := ""
		bestLat := int64(1<<62 - 1)
		for _, c := range candidates {
			if c == skip || !isHealthy(c) {
				continue
			}
			lat := p.effectiveLatencyLocked(c)
			if lat < 0 {
				lat = 0
			}
			if best == "" || lat < bestLat {
				best, bestLat = c, lat
			}
		}
		return best
	case ProxyStrategySticky:
		order := candidates
		if skip != "" {
			order = rotateCandidates(candidates, skip)
		}
		for _, c := range order {
			if c != skip && isHealthy(c) {
				return c
			}
		}
		return ""
	default: // ordered
		for _, c := range candidates {
			if c != skip && isHealthy(c) {
				return c
			}
		}
		return ""
	}
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
	p.mu.Lock()
	p.recordLocked(proxy, ok, latency, err, classifyProxyError(proxy, err), time.Now())
	p.mu.Unlock()
}

func clipProxyError(proxyURL string, err error) string {
	s := traffic.SanitizeProxyError(proxyURL, err)
	if len([]rune(s)) > 200 {
		s = string([]rune(s)[:200]) + "…"
	}
	return s
}

func classifyProxyError(proxyURL string, err error) ProxyErrorKind {
	if err == nil {
		return ProxyErrNone
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "407") || strings.Contains(s, "proxy auth") ||
		strings.Contains(s, "authentication failed") || strings.Contains(s, "authentication required") {
		return ProxyErrAuth
	}
	if strings.Contains(s, "socks") && (strings.Contains(s, "auth") || strings.Contains(s, "username/password")) {
		return ProxyErrAuth
	}
	if strings.Contains(s, "proxyconnect") || strings.Contains(s, "socks connect") {
		if strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded") {
			return ProxyErrTimeout
		}
		return ProxyErrConnect
	}
	var hostPort string
	if u, perr := url.Parse(strings.TrimSpace(proxyURL)); perr == nil && u.Host != "" {
		hostPort = strings.ToLower(u.Host)
	}
	if hostPort != "" && strings.Contains(s, hostPort) &&
		(strings.Contains(s, "connection refused") || strings.Contains(s, "connection reset") || strings.Contains(s, "no such host")) {
		return ProxyErrConnect
	}
	if strings.Contains(s, "proxy") && strings.Contains(s, "http 5") {
		return ProxyErrTargetHTTP
	}
	if errors.Is(err, context.DeadlineExceeded) || osIsTimeout(err) || strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded") {
		return ProxyErrTimeout
	}
	return ProxyErrOther
}

func osIsTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isFastFailoverError(kind ProxyErrorKind) bool {
	return kind == ProxyErrAuth || kind == ProxyErrConnect
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
	p.switchSeq++
	seq := p.switchSeq
	p.mu.Unlock()

	// Apply is slower than the protected state update. If a newer switch won
	// while this call waited, skip this stale apply so the live proxy can never
	// be overwritten by an older target.
	p.applyMu.Lock()
	defer p.applyMu.Unlock()
	p.mu.Lock()
	current := p.switchSeq
	p.mu.Unlock()
	if seq != current {
		return
	}

	if target == blockedProxyURL {
		log.Printf("[proxy] 全部代理不可用（原 %s），出口已阻断：目标流量会失败，**不会直连**", traffic.RedactProxyURL(previous))
	} else {
		log.Printf("[proxy] 出口切换：%s → %s", traffic.RedactProxyURL(previous), traffic.RedactProxyURL(target))
	}
	p.apply(target)
}

// ReloadConfig 原地热更新代理池配置，保留仍存在且健康的 active 与历史探测数据，避免
// 每次保存设置都引发全平台闪断为 blockedProxyURL。
func (p *ProxyPool) ReloadConfig(norm ProxyPoolConfig) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.cfg = norm
	keep := make(map[string]bool, len(norm.Candidates))
	for _, c := range norm.Candidates {
		keep[c] = true
	}
	for k := range p.health {
		if !keep[k] {
			delete(p.health, k)
		}
	}
	for k := range p.emaLatency {
		if !keep[k] {
			delete(p.emaLatency, k)
		}
	}
	if p.pinned != "" && !keep[p.pinned] {
		p.pinned = ""
	}
	for c, h := range p.health {
		h.Pinned = p.pinned != "" && p.pinned == c
		p.health[c] = h
	}

	prev := p.active
	needSwitch := false
	nextTarget := prev
	switch {
	case len(norm.Candidates) == 0:
		nextTarget = ""
		if norm.configured {
			nextTarget = blockedProxyURL
		}
		needSwitch = nextTarget != prev
		p.failures = 0
	case keep[prev]:
		// 当前活跃节点仍在新列表中：继续无中断运行，并发起一次后台复查更新其余节点。
	default:
		// 当前活跃节点被移除或原本为空：先从已有健康记录中择优接管，全未知则 fail-closed 并立刻复查。
		best := p.pickBestHealthyLocked(norm.Candidates, nil, "", func(c string) bool {
			h, ok := p.health[c]
			return ok && h.OK
		})
		if best == "" {
			best = blockedProxyURL
		}
		nextTarget = best
		needSwitch = nextTarget != prev
		p.failures = 0
	}
	reloadCh := p.reload
	recheckCh := p.recheck
	var spawnStop chan struct{}
	if p.stop != nil && len(norm.Candidates) > 0 && !p.loopRunning {
		p.loopRunning = true
		p.wg.Add(1)
		spawnStop = p.stop
	}
	p.mu.Unlock()

	if len(norm.Candidates) == 0 {
		p.closeFirstProbe()
	}
	if needSwitch {
		p.switchTo(nextTarget)
	}
	if spawnStop != nil {
		go p.runLoop(spawnStop, false)
	}
	if reloadCh != nil && spawnStop == nil {
		select {
		case reloadCh <- struct{}{}:
		default:
		}
	}
	if len(norm.Candidates) > 0 && recheckCh != nil && spawnStop == nil {
		select {
		case recheckCh <- struct{}{}:
		default:
		}
	}
}

// CheckNow 同步执行一轮全量并发探测并返回最新状态。
func (p *ProxyPool) CheckNow(ctx context.Context) ProxyPoolStatus {
	if p == nil {
		return ProxyPoolStatus{}
	}
	p.checkContext(ctx)
	p.closeFirstProbe()
	return p.Status()
}

// Pin 手动固定某个候选节点（传入空串则取消固定）。固定前会实时探测确认其确实可用，
// 不可用时返回可读错误而不把在线流量切向死节点。
func (p *ProxyPool) Pin(ctx context.Context, candidate string) error {
	if p == nil {
		return errors.New("代理池未启用")
	}
	raw := strings.TrimSpace(candidate)
	if raw == "" {
		p.mu.Lock()
		p.pinned = ""
		for c, h := range p.health {
			h.Pinned = false
			p.health[c] = h
		}
		p.mu.Unlock()
		p.RequestCheck()
		return nil
	}
	u, err := traffic.ValidateProxyURL(raw)
	if err != nil {
		return err
	}
	target := u.String()

	p.mu.Lock()
	var matched string
	for _, c := range p.cfg.Candidates {
		if c == target || traffic.RedactProxyURL(c) == target {
			matched = c
			break
		}
	}
	healthURLs := append([]string(nil), p.cfg.HealthURLs...)
	timeout := p.cfg.Timeout
	p.mu.Unlock()
	if matched == "" {
		return fmt.Errorf("代理 %s 不在当前候选列表中", traffic.RedactProxyURL(target))
	}
	if len(healthURLs) == 0 {
		healthURLs = []string{defaultProxyHealthURL}
	}
	ok, lat, kind, perr := p.probeOne(ctx, matched, healthURLs, timeout)

	p.mu.Lock()
	p.recordLocked(matched, ok, lat, perr, kind, time.Now())
	if !ok {
		p.mu.Unlock()
		return fmt.Errorf("固定节点失败（%s 不可用）：%s", traffic.RedactProxyURL(matched), clipProxyError(matched, perr))
	}
	p.pinned = matched
	for c, h := range p.health {
		h.Pinned = c == matched
		p.health[c] = h
	}
	p.mu.Unlock()
	p.switchTo(matched)
	return nil
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

// Status 给设置页与出口只读视图展示。
func (p *ProxyPool) Status() ProxyPoolStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	cooldownRem := 0
	if p.cfg.Cooldown > 0 && !p.lastSwitch.IsZero() {
		if rem := p.cfg.Cooldown - time.Since(p.lastSwitch); rem > 0 {
			cooldownRem = int(rem.Seconds()) + 1
		}
	}
	st := ProxyPoolStatus{
		Active:               p.active,
		Blocked:              p.active == blockedProxyURL,
		Strategy:             p.cfg.Strategy,
		Pinned:               p.pinned,
		Probing:              p.probing,
		FailThreshold:        p.cfg.FailThreshold,
		CooldownRemainingSec: cooldownRem,
	}
	// 只回**已经探测过**的候选：设置页据此区分"正在检测"与"确实全挂"，
	// 刚保存、第一次探测还没回来时不会先闪一下红色的"全部不可用"。
	for _, c := range p.cfg.Candidates {
		if h, ok := p.health[c]; ok {
			h.Pinned = p.pinned != "" && p.pinned == c
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
	c.HealthURLs = append([]string(nil), p.cfg.HealthURLs...)
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
	// 还没跑完首探就被关掉：别让 WaitReady 的调用方一直等到自己的超时。
	p.closeFirstProbe()
}

// normalizeEgressIPURL 校验并补默认的出口 IP 查询地址。
func normalizeEgressIPURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultProxyEgressIPURL, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("出口 IP 查询 URL 必须是 http(s) 绝对地址：%q", raw)
	}
	return raw, nil
}

// ProxyEgress 是一次"经代理查出口 IP"的结果。
type ProxyEgress struct {
	Proxy  string `json:"proxy"`
	IP     string `json:"ip,omitempty"`
	Leaked bool   `json:"leaked,omitempty"` // 出口 IP 与本机网卡地址相同（可能没走代理）
	Error  string `json:"error,omitempty"`
}

// localInterfaceIPs 返回本机所有网卡地址（用于识别"出口 IP 其实就是本机"）。
func localInterfaceIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP != nil {
			out = append(out, ipnet.IP)
		}
	}
	return out
}

// checkProxyEgressIPs 并行地经每个候选代理查询出口 IP。它**只走代理**，没有直连
// 分支；leaked 只在本机网卡地址与出口 IP 相同时置位（用来发现"代理规则其实直连"）。
func checkProxyEgressIPs(ctx context.Context, proxies []string, ipURL string, timeout time.Duration) []ProxyEgress {
	if timeout <= 0 {
		timeout = defaultProxyHealthTimeout
	}
	results := make([]ProxyEgress, len(proxies))
	sem := make(chan struct{}, egressIPCheckConcurrency)
	var wg sync.WaitGroup
	local := localInterfaceIPs()
	for i, proxy := range proxies {
		wg.Add(1)
		go func(i int, proxy string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ip, err := probeEgressIP(ctx, proxy, ipURL, timeout)
			res := ProxyEgress{Proxy: proxy, IP: ip}
			if err != nil {
				res.Error = clipProxyError(proxy, err)
				results[i] = res
				return
			}
			parsed := net.ParseIP(ip)
			for _, lip := range local {
				if parsed != nil && lip.Equal(parsed) {
					res.Leaked = true
					break
				}
			}
			results[i] = res
		}(i, proxy)
	}
	wg.Wait()
	return results
}

// probeEgressIP 经代理 GET 一个返回出口 IP 的服务。Transport.Proxy 显式设置，
// 代理失败即失败，**没有直连回退**。
func probeEgressIP(ctx context.Context, proxyURL, ipURL string, timeout time.Duration) (string, error) {
	u, err := traffic.TransportProxyURL(proxyURL)
	if err != nil {
		return "", err
	}
	tr := probeTransport(u, timeout)
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: probeRedirectPolicy}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ipURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("出口 IP 查询服务返回 HTTP %d", resp.StatusCode)
	}
	return parseEgressIP(body)
}

// probeTransport 是健康检查与出口 IP 查询共用的 Transport。
//
// 两条防护缺一不可：
//   - **拨号守卫**：健康检查/出口 IP 的 URL 是用户可配的，没有守卫时把它指向
//     127.0.0.1 或云元数据地址，平台就会替用户去连内网（DNS 重绑定也一样，只有
//     在拨号阶段检查才挡得住）。复用 mcphttp 的判定口径：拒绝环回、链路本地、
//     未指定与组播，放行 RFC1918（自建探测服务常见）。
//   - **重定向限制**：跨主机 302 会让"校验时是公网域名、实际连到内网"重新成立。
func probeTransport(proxy *url.URL, timeout time.Duration) *http.Transport {
	tr := &http.Transport{
		Proxy:                 http.ProxyURL(proxy),
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
	// 只有「直连到探测 URL」这一跳要过守卫。经代理时 DialContext 连的是代理本身，
	// 代理常常就在本机（Clash 的 127.0.0.1:7890）——在那里拦环回会把正常配置判死。
	if proxy == nil {
		tr.DialContext = (&net.Dialer{Timeout: timeout, Control: blockProbeInternalDial}).DialContext
	}
	return tr
}

// blockProbeInternalDial 是拨号阶段的 Control 钩子：连接建立时按真实 IP 判定。
func blockProbeInternalDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("无法解析探测目标地址 %q", host)
	}
	if probeBlockedIP(ip) {
		return fmt.Errorf("拒绝连接本机/链路本地探测地址 %s", ip)
	}
	return nil
}

// probeBlockedIP 与 mcphttp.isBlockedDialIP 同一口径：只挡环回、链路本地、
// 未指定与组播；RFC1918 私网放行（内网自建的健康检查服务是常见部署）。
func probeBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// probeRedirectPolicy 只允许同主机重定向，最多 3 跳。
func probeRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return fmt.Errorf("健康检查重定向次数过多")
	}
	if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		return fmt.Errorf("健康检查重定向到其它主机被拒绝：%s → %s", via[0].URL.Host, req.URL.Host)
	}
	return nil
}

// parseEgressIP 兼容三种常见返回：纯 IP（ipify）、cloudflare trace 的 ip= 行、
// {"ip":"..."} JSON。
func parseEgressIP(body []byte) (string, error) {
	text := strings.TrimSpace(string(body))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ip=") {
			text = strings.TrimSpace(strings.TrimPrefix(line, "ip="))
			break
		}
	}
	if ip := net.ParseIP(text); ip != nil {
		return ip.String(), nil
	}
	var m struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal([]byte(text), &m) == nil {
		if ip := net.ParseIP(strings.TrimSpace(m.IP)); ip != nil {
			return ip.String(), nil
		}
	}
	clipped := text
	if len([]rune(clipped)) > 120 {
		clipped = string([]rune(clipped)[:120]) + "…"
	}
	return "", fmt.Errorf("无法从响应解析出口 IP：%q", clipped)
}

// probeHTTPProxy 经候选代理访问健康检查 URL。Transport.Proxy 显式指向该代理：
// 代理挂了请求就失败，**没有直连回退**（不读 NO_PROXY，也不用 ProxyFromEnvironment）。
// HTTP 代理用 CONNECT 域名、SOCKS5/SOCKS5H 由代理侧解析域名，目标 DNS 不在本机泄漏。
func probeHTTPProxy(ctx context.Context, proxyURL, healthURL string, timeout time.Duration) error {
	u, err := traffic.TransportProxyURL(proxyURL)
	if err != nil {
		return err
	}
	tr := probeTransport(u, timeout)
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: probeRedirectPolicy}
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

// RequestCheck nudges the existing probe loop instead of spawning an unmanaged
// goroutine. Close() -> wg.Wait therefore covers every health check.
func (p *ProxyPool) RequestCheck() {
	if p == nil {
		return
	}
	p.mu.Lock()
	started := p.loopRunning
	ch := p.recheck
	p.mu.Unlock()
	if !started || ch == nil {
		p.check()
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}
