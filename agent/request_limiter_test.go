package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 秒窗口额度必须真的生效：3 个并发请求，额度 2/s，第三个要等约 1 秒。
func TestRequestLimiterEnforcesSecondWindow(t *testing.T) {
	var l RequestLimiter
	if err := l.Configure(2, 0); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Wait(context.Background()); err != nil {
				t.Errorf("wait: %v", err)
			}
		}()
	}
	wg.Wait()
	// 前两个立即放行，第三个要等窗口里最早那次过期。
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Fatalf("第三个请求应被限速等待，实际只用了 %s", elapsed)
	}
}

// 取消必须立即返回，而不是等到额度可用。
func TestRequestLimiterWaitHonoursContext(t *testing.T) {
	var l RequestLimiter
	if err := l.Configure(1, 0); err != nil {
		t.Fatal(err)
	}
	if err := l.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("额度耗尽时第二个请求应等到 ctx 取消")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("ctx 取消后应立即返回，实际 %s", elapsed)
	}
}

// 重新配置额度必须唤醒等待者并按新额度判断，而不是继续按旧额度睡。
func TestRequestLimiterReconfigureWakesWaiters(t *testing.T) {
	var l RequestLimiter
	if err := l.Configure(1, 0); err != nil {
		t.Fatal(err)
	}
	if err := l.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.Wait(context.Background())
	}()
	time.Sleep(50 * time.Millisecond)
	if err := l.Configure(1000, 0); err != nil { // 放开额度
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("放开额度后等待者应被唤醒")
	}
}

// 限流挂在 transport 上：SDK 内部重试发出的每个 HTTP 请求都要计数。
func TestRateLimitedTransportCountsEveryAttempt(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var l RequestLimiter
	if err := l.Configure(2, 0); err != nil {
		t.Fatal(err)
	}
	rt := rateLimitedTransport{base: http.DefaultTransport, limiter: &l}
	client := &http.Client{Transport: rt}
	start := time.Now()
	for i := 0; i < 3; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if hits.Load() != 3 {
		t.Fatalf("三个请求都应发出，实际 %d", hits.Load())
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Fatalf("transport 层的限流没生效，三个请求只用了 %s", elapsed)
	}
}

// 未配置额度时 limiter 必须完全不阻塞（fail-open 的最小形态）。
func TestRequestLimiterDisabledIsPassThrough(t *testing.T) {
	var l RequestLimiter
	if err := l.Configure(0, 0); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 50; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("未配置额度时不该有任何等待，实际 %s", elapsed)
	}
}
