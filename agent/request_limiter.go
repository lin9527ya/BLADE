package agent

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"
)

// RequestLimiter 是**HTTP 尝试级**的滚动窗口限流器：秒窗口 + 分窗口各一个。
//
// 为什么不用 SDK 自带限流：Norma 的 llm.RateLimit 是 token bucket，允许整窗突发，
// 而且只统计「逻辑 Stream 调用」——SDK 内部的连接重试、空响应重试都绕过它。
// 实测里网关的 429 恰恰来自这些重试：配了 10/min，一分钟仍能打出 30 个请求。
// 这里把计数放到 RoundTripper 上，任何真正发出的 HTTP 请求都会占用额度。
//
// 限流器必须**跨 provider 重建存活**（见 server 侧的 profile→limiter 表），
// 否则用户每改一次配置，配额就从零重新开始，限流形同虚设。
type RequestLimiter struct {
	mu      sync.Mutex
	windows [2]requestWindow
	sent    []time.Time
	// changed 在限额被重新配置时关闭并替换：等待中的请求会重新检查窗口，
	// 而不是继续按旧限额睡到天亮。
	changed chan struct{}
}

type requestWindow struct {
	period time.Duration
	limit  float64
}

// Configure 设置秒/分两个窗口的额度（<=0 表示该窗口不限）。重复调用是安全的：
// 已有等待者会被唤醒并按新额度重新判断。
func (l *RequestLimiter) Configure(perSecond, perMinute float64) error {
	if l == nil {
		return fmt.Errorf("nil request limiter")
	}
	var windows [2]requestWindow
	for i, rate := range [2]float64{perSecond, perMinute} {
		if math.IsNaN(rate) || math.IsInf(rate, 0) {
			return fmt.Errorf("LLM request rate must be finite")
		}
		if rate <= 0 {
			continue
		}
		period := [2]time.Duration{time.Second, time.Minute}[i]
		// 整数个 HTTP 尝试放不进小数额度里。
		limit := math.Floor(rate)
		if rate < 1 {
			// 0.5/s 这种小数额度 = 每 2 秒 1 次。
			d := math.Ceil(float64(period) / rate)
			if d >= float64(math.MaxInt64) {
				return fmt.Errorf("LLM request rate is too small")
			}
			period, limit = time.Duration(d), 1
		}
		windows[i] = requestWindow{period: period, limit: limit}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.windows = windows
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
	return nil
}

// Limits 返回当前生效的 (每秒, 每分钟) 额度，供日志与测试使用。
func (l *RequestLimiter) Limits() (float64, float64) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out [2]float64
	for i, w := range l.windows {
		if w.limit > 0 {
			out[i] = float64(w.limit) * float64(time.Second) / float64(w.period)
		}
	}
	return out[0], out[1]
}

// Wait 阻塞直到本次请求可以发出，并把它计入窗口。只有真正能放行时才计数，
// 所以并发与取消都不会超额。返回的 error 只在 ctx 结束时非空。
func (l *RequestLimiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	for {
		l.mu.Lock()
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return err
		}
		now := time.Now()
		// 至少保留一分钟的历史（额度关闭期间也要保留），小数额度可能需要更长。
		retention := time.Minute
		for _, w := range l.windows {
			if w.period > retention {
				retention = w.period
			}
		}
		first := 0
		// 至少保留最后一次尝试：之后改成小数额度时还要靠它拉开间隔。
		for first+1 < len(l.sent) && !l.sent[first].After(now.Add(-retention)) {
			first++
		}
		l.sent = l.sent[first:]
		var delay time.Duration
		for _, w := range l.windows {
			if w.limit == 0 {
				continue
			}
			start := len(l.sent)
			for start > 0 && l.sent[start-1].After(now.Add(-w.period)) {
				start--
			}
			count := len(l.sent) - start
			if float64(count) >= w.limit {
				// 额度被调低时，要等足够多的旧请求过期才腾得出位置，
				// 而不是只等最老的那一个。
				d := l.sent[len(l.sent)-int(w.limit)].Add(w.period).Sub(now)
				if d > delay {
					delay = d
				}
			}
		}
		if delay <= 0 {
			l.sent = append(l.sent, now)
			l.mu.Unlock()
			return nil
		}
		changed := l.changed
		l.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// rateLimitedTransport 把限流挂到 HTTP 层。放在最外层（包住 quotaAwareTransport），
// 这样 SDK 的连接重试、空响应重试发出的每一个请求都会被计数。
type rateLimitedTransport struct {
	base    http.RoundTripper
	limiter *RequestLimiter
}

func (t rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.limiter.Wait(req.Context()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}
