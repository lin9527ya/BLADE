package server

import (
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
	wafHitWindow    = 2 * time.Minute
	wafHitThreshold = 2
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

// observeWAF is called from the worker's activity callback. It never blocks the
// tool call: threshold bookkeeping is cheap, and the actual rotation runs in a
// background goroutine.
func (e *Engine) observeWAF(t *Task, r db.Activity) {
	if e == nil || t == nil || e.m == nil || r.Kind != "tool_result" {
		return
	}
	text := r.Detail
	if strings.TrimSpace(text) == "" {
		text = r.Summary
	}
	host, hit := detectWAFBlock(text)
	if !hit {
		return // 便宜的特征判断先行；只有像 WAF 才去读设置
	}
	if !e.m.ClashAutoRotateEnabled() {
		return
	}
	now := time.Now()
	key := t.ID + "|" + host
	e.wafMu.Lock()
	if e.wafHits == nil {
		e.wafHits = map[string][]time.Time{}
	}
	hits := e.wafHits[key][:0]
	for _, at := range e.wafHits[key] {
		if now.Sub(at) <= wafHitWindow {
			hits = append(hits, at)
		}
	}
	hits = append(hits, now)
	e.wafHits[key] = hits
	if len(hits) < wafHitThreshold || e.wafRotating {
		e.wafMu.Unlock()
		return
	}
	delete(e.wafHits, key)
	e.wafRotating = true
	e.wafMu.Unlock()

	goSafe("engine/waf-rotate", nil, func() {
		defer func() {
			e.wafMu.Lock()
			e.wafRotating = false
			e.wafMu.Unlock()
		}()
		reason := fmt.Sprintf("WAF 拦截（%s）", host)
		res, err := e.m.RotateEgress(reason)
		if err != nil {
			log.Printf("[waf] task %s 自动切换出口失败（%s）：%v", t.ID, host, err)
			return
		}
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
			Summary: "检测到 WAF 拦截，已自动切换 Clash 出口",
			Detail:  res.Summary() + "\n触发：" + host + "\n后续：用最小请求复测；仍被拦就停这条方向，不要重放同一 payload。"})
	})
}
