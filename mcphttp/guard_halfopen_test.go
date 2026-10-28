package mcphttp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 冷却结束只放行一个探针：其余调用快速失败，避免所有等待者同时涌向
// 可能仍不健康的 server（探针风暴）。
func TestGuardHalfOpenSingleProbe(t *testing.T) {
	g := newGuard()
	cfg := GuardConfig{BreakerThreshold: 1, BreakerCooldown: 30 * time.Millisecond}
	ctx := context.Background()

	if err := g.acquire(ctx, "srv", cfg); err != nil {
		t.Fatal(err)
	}
	g.failure(errors.New("boom"), cfg)
	g.release()

	// 冷却中：快速失败。
	if err := g.acquire(ctx, "srv", cfg); err == nil || !strings.Contains(err.Error(), "熔断冷却") {
		t.Fatalf("cooling acquire = %v, want fast failure", err)
	}

	time.Sleep(40 * time.Millisecond)

	// 冷却结束：第一个调用是探针。
	if err := g.acquire(ctx, "srv", cfg); err != nil {
		t.Fatalf("probe acquire: %v", err)
	}
	if st := g.status("srv", cfg); !st.HalfOpen {
		t.Fatal("status must report half-open while the probe runs")
	}
	// 第二个调用在探针返回前必须被拒。
	if err := g.acquire(ctx, "srv", cfg); err == nil || !strings.Contains(err.Error(), "单探针") {
		t.Fatalf("second acquire during probe = %v, want single-probe rejection", err)
	}

	// 探针失败 → 重新整段冷却（阈值 1 也成立，探针失败不依赖累计）。
	g.failure(errors.New("still down"), cfg)
	g.release()
	if err := g.acquire(ctx, "srv", cfg); err == nil || !strings.Contains(err.Error(), "熔断冷却") {
		t.Fatalf("after failed probe acquire = %v, want re-open", err)
	}
}

// 探针成功即恢复；探针被取消（没有回报成败）则交还资格，让下一个调用探测，
// 不能把半开状态永久占住。
func TestGuardHalfOpenSuccessAndCancelledProbe(t *testing.T) {
	g := newGuard()
	cfg := GuardConfig{BreakerThreshold: 1, BreakerCooldown: 10 * time.Millisecond}
	ctx := context.Background()

	g.failure(errors.New("boom"), cfg)
	time.Sleep(20 * time.Millisecond)
	if err := g.acquire(ctx, "srv", cfg); err != nil {
		t.Fatal(err)
	}
	g.success()
	g.release()
	if err := g.acquire(ctx, "srv", cfg); err != nil {
		t.Fatalf("breaker must close after a successful probe: %v", err)
	}
	g.release()

	// 再次熔断，冷却后探针被取消（只 acquire + release）。
	g.failure(errors.New("boom"), cfg)
	time.Sleep(20 * time.Millisecond)
	if err := g.acquire(ctx, "srv", cfg); err != nil {
		t.Fatal(err)
	}
	g.release() // 没有 success/failure
	if err := g.acquire(ctx, "srv", cfg); err != nil {
		t.Fatalf("cancelled probe must hand the slot to the next caller: %v", err)
	}
	g.success()
	g.release()
}
