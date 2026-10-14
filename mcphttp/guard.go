package mcphttp

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// P24 外部 MCP 的并发上限与熔断（每 server）。
//
// 为什么：外部 MCP server（自带进程/远程服务）挂掉或变慢时，Agent 会一轮轮重试——
// 每次都是一个会超时的网络调用，把 run 的时间全烧在等待上；并发也不受控（N 个 worker
// 同时打同一个 server）。熔断让"当下不健康"这件事被记住：冷却期内直接快速失败并给出
// 可读理由，冷却后放行一次真实调用（成功即恢复）。
//
// 状态在**内存、按 server 名共享**（跨连接复用）：重连不该把"这个 server 刚才连续失败"
// 忘掉；重启进程归零（熔断是"此刻"的判断，不该跨重启持久化）。人可以在 MCP 页手动重置。

// GuardConfig 是每 server 的守卫参数（0 = 不限/用默认）。
type GuardConfig struct {
	// MaxConcurrent 是同时进行的调用上限；0/负 = 不限。
	MaxConcurrent int
	// BreakerThreshold 是连续失败多少次开熔断；0 = 用默认 5。
	BreakerThreshold int
	// BreakerCooldown 是熔断冷却时长；0 = 用默认 60s。
	BreakerCooldown time.Duration
}

// DefaultGuardConfig 是没注入解析器时用的保守默认（库自身也保护调用方）。
func DefaultGuardConfig() GuardConfig {
	return GuardConfig{MaxConcurrent: 2, BreakerThreshold: 5, BreakerCooldown: 60 * time.Second}
}

func (c GuardConfig) withDefaults() GuardConfig {
	d := DefaultGuardConfig()
	if c.BreakerThreshold <= 0 {
		c.BreakerThreshold = d.BreakerThreshold
	}
	if c.BreakerCooldown <= 0 {
		c.BreakerCooldown = d.BreakerCooldown
	}
	return c
}

// GuardStatus 是给 UI/日志看的快照。
type GuardStatus struct {
	Server              string `json:"server"`
	MaxConcurrent       int    `json:"max_concurrent"` // 0 = 不限
	Inflight            int    `json:"inflight"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	Open                bool   `json:"open"`
	OpenRemainingSec    int    `json:"open_remaining_sec,omitempty"`
	LastError           string `json:"last_error,omitempty"`
}

// guard 是单 server 的信号量 + 熔断状态机。
type guard struct {
	mu       sync.Mutex
	waitCh   chan struct{} // 释放时 close 并换新（等待者被唤醒后重新竞争）
	inflight int
	fails    int
	openAt   time.Time
	openTill time.Time
	lastErr  string
}

func newGuard() *guard { return &guard{waitCh: make(chan struct{})} }

// acquire 取一个并发槽；熔断开时立刻返回可读错误（不发网络请求、不空等超时）。
func (g *guard) acquire(ctx context.Context, server string, cfg GuardConfig) error {
	cfg = cfg.withDefaults()
	for {
		g.mu.Lock()
		if now := time.Now(); now.Before(g.openTill) {
			remain := g.openTill.Sub(now)
			last := g.lastErr
			g.mu.Unlock()
			msg := fmt.Sprintf("MCP server %q 处于熔断冷却（还剩 %s）：最近连续失败已开闸停止调用", server, remain.Round(time.Second))
			if last != "" {
				msg += "；最后一次错误：" + last
			}
			msg += "。可在 系统设置 → MCP 页手动重置，或稍后自动恢复。"
			return fmt.Errorf("%s", msg)
		}
		if cfg.MaxConcurrent <= 0 || g.inflight < cfg.MaxConcurrent {
			g.inflight++
			g.mu.Unlock()
			return nil
		}
		ch := g.waitCh
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
			// 有槽释放了，回到循环重新竞争。
		}
	}
}

// release 归还并发槽并唤醒等待者。
func (g *guard) release() {
	g.mu.Lock()
	if g.inflight > 0 {
		g.inflight--
	}
	close(g.waitCh)
	g.waitCh = make(chan struct{})
	g.mu.Unlock()
}

// success 记一次成功：连续失败清零；熔断若在冷却后就地恢复（不需要等冷却走完再"试一次"）。
func (g *guard) success() {
	g.mu.Lock()
	g.fails = 0
	g.openTill = time.Time{}
	g.openAt = time.Time{}
	g.lastErr = ""
	g.mu.Unlock()
}

// failure 记一次失败：连续失败达阈值就开熔断（记录理由与开闸时刻）。
func (g *guard) failure(err error, cfg GuardConfig) {
	cfg = cfg.withDefaults()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fails++
	if err != nil {
		g.lastErr = err.Error()
	}
	if g.fails >= cfg.BreakerThreshold {
		g.openTill = time.Now().Add(cfg.BreakerCooldown)
		if g.openAt.IsZero() {
			g.openAt = time.Now()
		}
		g.fails = 0 // 开闸后重新计数：冷却结束再失败一轮才重新开闸
	}
}

func (g *guard) status(server string, cfg GuardConfig) GuardStatus {
	cfg = cfg.withDefaults()
	g.mu.Lock()
	defer g.mu.Unlock()
	st := GuardStatus{
		Server: server, MaxConcurrent: cfg.MaxConcurrent, Inflight: g.inflight,
		ConsecutiveFailures: g.fails, LastError: g.lastErr,
	}
	if remain := time.Until(g.openTill); remain > 0 {
		st.Open = true
		st.OpenRemainingSec = int(remain.Round(time.Second) / time.Second)
	}
	return st
}

// ---- 注册表（按 server 名共享状态，跨连接复用）----

var (
	guardReg  sync.Map // server name → *guard
	guardCfg  sync.Map // 指向 func(string) GuardConfig 的容器
	guardOnce sync.Once
)

// SetGuardConfigResolver 注入"按 server 名取配置"的解析器（server 层从设置现读）。
// 不注入时用 DefaultGuardConfig（库自身也保护调用方）。
func SetGuardConfigResolver(fn func(server string) GuardConfig) {
	guardCfg.Store("resolver", fn)
}

func guardConfigFor(server string) GuardConfig {
	if v, ok := guardCfg.Load("resolver"); ok {
		if fn, ok := v.(func(string) GuardConfig); ok && fn != nil {
			return fn(server)
		}
	}
	return DefaultGuardConfig()
}

func guardFor(server string) *guard {
	guardOnce.Do(func() {})
	if v, ok := guardReg.Load(server); ok {
		return v.(*guard)
	}
	g := newGuard()
	actual, _ := guardReg.LoadOrStore(server, g)
	return actual.(*guard)
}

// GuardStatuses 返回所有"有状态"的 server 的守卫快照（UI 用；按 server 名排序）。
func GuardStatuses() []GuardStatus {
	var out []GuardStatus
	guardReg.Range(func(k, v any) bool {
		server, _ := k.(string)
		g, _ := v.(*guard)
		if g == nil {
			return true
		}
		out = append(out, g.status(server, guardConfigFor(server)))
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Server < out[j].Server })
	return out
}

// ResetGuard 手动重置某个 server 的熔断（保留并发槽计数；人修好配置后用）。
func ResetGuard(server string) {
	if v, ok := guardReg.Load(server); ok {
		if g, ok := v.(*guard); ok {
			g.success()
		}
	}
}

// Guard 返回本连接的 server 守卫快照。
func (c *Client) Guard() GuardStatus {
	return guardFor(c.server).status(c.server, guardConfigFor(c.server))
}

// ResetGuard 手动重置本连接的熔断状态。
func (c *Client) ResetGuard() { ResetGuard(c.server) }
