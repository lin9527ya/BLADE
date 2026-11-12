package server

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

// WAF 自动切换出口：worker 的工具输出里出现**强 WAF 特征**（挑战页/厂商指纹 +
// 403/429）时，连续命中两次就切一次 Clash 出口。普通登录 403 不会触发——只认
// Cloudflare / Incapsula / Akamai / Sucuri / AWS WAF / ModSecurity 等明确指纹。
//
// 为什么从工具输出识别，而不是只看流量代理：流量捕获默认关闭，且 Bash/WebFetch
// 的输出本身就是最贴近现场的证据；同时不依赖抓包开关。

var (
	wafStatusRE = regexp.MustCompile(`(?i)(?:^|[^0-9])(403|429)(?:[^0-9]|$)`)
	wafHostRE   = regexp.MustCompile(`(?i)https?://([a-z0-9._~%\-:\[\]]+)`)
)

const (
	egressHitWindow    = 2 * time.Minute
	egressHitThreshold = 2 // WAF 或目标不可达，连续命中两次才切
)

var wafMarkers = []string{
	"cf-mitigated",
	"just a moment",
	"attention required",
	"cloudflare",
	"incapsula",
	"akamai",
	"akamaighost",
	"sucuri",
	"aws waf",
	"web application firewall",
	"modsecurity",
	"request blocked",
	"request unsuccessful",
	"the requested url was rejected",
	"reference #",
}

// detectWAFBlock reports whether text looks like a WAF block page, plus the host
// it came from when a URL is present.
func detectWAFBlock(text string) (string, bool) {
	if len(text) == 0 {
		return "", false
	}
	if len(text) > 16384 {
		text = text[:16384]
	}
	lower := strings.ToLower(text)
	if !wafStatusRE.MatchString(lower) &&
		!strings.Contains(lower, "forbidden") &&
		!strings.Contains(lower, "too many requests") {
		return "", false
	}
	hit := false
	for _, m := range wafMarkers {
		if strings.Contains(lower, m) {
			hit = true
			break
		}
	}
	if !hit {
		return "", false
	}
	host := ""
	if mm := wafHostRE.FindStringSubmatch(text); len(mm) > 1 {
		host = strings.ToLower(mm[1])
	}
	return host, true
}

// egressSignal 区分两类「当前出口不可用」的证据：WAF 拦截与目标不可达。
// 两者的处理相同（切 Clash 出口），但阈值语义与给模型的建议不同。
type egressSignal int

const (
	egressSignalWAF egressSignal = iota
	egressSignalUnreachable
)

func (s egressSignal) label() string {
	if s == egressSignalUnreachable {
		return "目标不可达"
	}
	return "WAF 拦截"
}

// unreachableMarkers 是"连接层面根本没打通"的强特征。
//
// 刻意不收 `connection refused` 与 `curl: (7) ... refused`：拒绝连接说明出口 IP
// 已经抵达目标、只是端口没开——端口扫描会大量产生这种输出，换出口毫无意义。
// 这里只认超时/不可路由/DNS 失败/代理网关错误这一类"包被丢弃或链路不通"的证据。
var unreachableMarkers = []string{
	"connection timed out",
	"connect timeout",
	"connect: connection timed out",
	"i/o timeout",
	"tls handshake timeout",
	"no route to host",
	"network is unreachable",
	"host is unreachable",
	"destination unreachable",
	"temporary failure in name resolution",
	"could not resolve host",
	"name or service not known",
	"curl: (28)",
	"curl: (35)",
	"curl: (52)",
	"curl: (56)",
	"502 bad gateway",
	"504 gateway timeout",
}

// detectEgressFailure 判断一段工具输出是否说明"当前出口到目标不通"。
// host 可能为空（很多脚本只打印状态码）。
func detectEgressFailure(text string) (host string, sig egressSignal, ok bool) {
	if host, hit := detectWAFBlock(text); hit {
		return host, egressSignalWAF, true
	}
	if len(text) == 0 {
		return "", 0, false
	}
	if len(text) > 16384 {
		text = text[:16384]
	}
	lower := strings.ToLower(text)
	hit := false
	for _, m := range unreachableMarkers {
		if strings.Contains(lower, m) {
			hit = true
			break
		}
	}
	// `code=000` 是探测脚本里"连接都没建立"的通用记号（curl -w '%{http_code}'）。
	if !hit && !strings.Contains(lower, "code=000") {
		return "", 0, false
	}
	if mm := wafHostRE.FindStringSubmatch(text); len(mm) > 1 {
		host = strings.ToLower(mm[1])
	}
	return host, egressSignalUnreachable, true
}

// observeEgress is called from the worker's activity callback. It never blocks the
// tool call: threshold bookkeeping is cheap, and the actual rotation runs in a
// background goroutine.
//
// 两类证据都会触发换出口：强 WAF 指纹（挑战页 + 403/429），以及连接层的持续
// 不可达（超时/DNS/代理网关错误）。后者是用户的实际场景：出口 IP 被目标侧防火墙
// 静默丢弃时，代理端口本身健康、既有的代理池健康检查永远不会切换。
func (e *Engine) observeEgress(t *Task, r db.Activity) {
	if e == nil || t == nil || e.m == nil || r.Kind != "tool_result" {
		return
	}
	text := r.Detail
	if strings.TrimSpace(text) == "" {
		text = r.Summary
	}
	host, sig, hit := detectEgressFailure(text)
	if !hit {
		return // 便宜的特征判断先行；只有像 WAF/不可达才去读设置
	}
	if !e.m.ClashAutoRotateEnabled() {
		return
	}
	now := time.Now()
	key := fmt.Sprintf("%s|%d|%s", t.ID, sig, host)
	e.egressMu.Lock()
	if e.egressHits == nil {
		e.egressHits = map[string][]time.Time{}
	}
	hits := e.egressHits[key][:0]
	for _, at := range e.egressHits[key] {
		if now.Sub(at) <= egressHitWindow {
			hits = append(hits, at)
		}
	}
	hits = append(hits, now)
	e.egressHits[key] = hits
	if len(hits) < egressHitThreshold {
		e.egressMu.Unlock()
		return
	}
	group := e.m.TaskEgressGroup(t.ID)
	if group == "" {
		group = "__default__"
	}
	if e.egressRotating == nil {
		e.egressRotating = map[string]bool{}
	}
	if e.egressRotating[group] {
		e.egressMu.Unlock()
		return
	}
	delete(e.egressHits, key)
	e.egressRotating[group] = true
	e.egressMu.Unlock()

	// 用任务的运行时 ctx：任务停掉/进程关停时不该再继续切出口（此前传
	// context.Background()，关停后最多还会跑 45 秒并改写设置与出口）。
	rotateCtx := e.runtimeCtx(t.ID)
	goSafe("engine/egress-rotate", nil, func() {
		defer func() {
			e.egressMu.Lock()
			delete(e.egressRotating, group)
			e.egressMu.Unlock()
		}()
		reason := fmt.Sprintf("%s（%s）", sig.label(), host)
		rotate := func(ctx context.Context, reason string) (ClashRotation, error) {
			return e.m.RotateEgressForTask(ctx, t.ID, reason)
		}
		if e.rotateEgressHook != nil {
			rotate = e.rotateEgressHook
		}
		res, err := rotate(rotateCtx, reason)
		if err != nil {
			log.Printf("[egress] task %s 自动切换出口失败（%s %s）：%v", t.ID, sig.label(), host, err)
			return
		}
		summary := "检测到 WAF 拦截，已自动切换 Clash 出口"
		next := "用最小请求复测；仍被拦就停这条方向，不要重放同一 payload。"
		if sig == egressSignalUnreachable {
			summary = "目标连接持续不可达，已自动切换 Clash 出口"
			next = "用一条最小请求复测连通性；仍不可达就不要原样重放，记录事实并换方向（目标本身宕机时换出口不会有效）。"
		}
		detail := res.Summary() + "\n触发：" + sig.label()
		if host != "" {
			detail += "（" + host + "）"
		}
		detail += "\n后续：" + next
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
			Summary: summary, Detail: detail})
	})
}
