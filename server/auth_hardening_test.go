package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 登录节流按**来源 IP** 分开计数：此前是单个全局计数器，任何人失败 5 次就把所有人锁住。
func TestLoginGuardPerIPBackoff(t *testing.T) {
	g := &loginGuard{cfg: loginGuardConfig{
		threshold: 2, baseLock: 20 * time.Millisecond, maxLock: 200 * time.Millisecond,
		decay: time.Minute, maxEntries: 8,
	}}
	const a, b = "203.0.113.1", "203.0.113.2"

	if lock := g.fail(a); lock != 0 {
		t.Fatalf("第一次失败不该锁，得到 %v", lock)
	}
	if _, locked := g.lockedFor(a); locked {
		t.Fatal("未达阈值就锁了")
	}
	if lock := g.fail(a); lock != 20*time.Millisecond {
		t.Fatalf("达阈值应锁 baseLock，得到 %v", lock)
	}
	if _, locked := g.lockedFor(a); !locked {
		t.Fatal("达阈值后应处于锁定状态")
	}
	if _, locked := g.lockedFor(b); locked {
		t.Fatal("另一个来源被连坐了——这正是全局计数器的问题")
	}

	// 指数退避：同来源继续失败，锁期翻倍（到上限为止）。
	if lock := g.fail(a); lock != 40*time.Millisecond {
		t.Fatalf("第二次锁应为 40ms，得到 %v", lock)
	}
	for i := 0; i < 10; i++ {
		g.fail(a)
	}
	left, locked := g.lockedFor(a)
	if !locked {
		t.Fatal("连续失败后应仍处于锁定状态")
	}
	if left > 200*time.Millisecond {
		t.Fatalf("锁期超过 maxLock：%v", left)
	}

	// 成功登录立刻解除该来源的锁定。
	g.succeed(a)
	if _, locked := g.lockedFor(a); locked {
		t.Fatal("成功登录后该来源应解锁")
	}
}

// 久不失败要自动衰减：否则计数永远停在阈值之上，正常用户会"一次尝试/锁期"。
func TestLoginGuardDecaysAfterQuietPeriod(t *testing.T) {
	g := &loginGuard{cfg: loginGuardConfig{
		threshold: 2, baseLock: 10 * time.Millisecond, maxLock: 50 * time.Millisecond,
		decay: 60 * time.Millisecond, maxEntries: 8,
	}}
	const ip = "203.0.113.9"
	g.fail(ip)
	if lock := g.fail(ip); lock == 0 {
		t.Fatal("达阈值应锁")
	}
	time.Sleep(80 * time.Millisecond) // > decay
	if lock := g.fail(ip); lock != 0 {
		t.Fatalf("衰减后应从零计数（本次只算第 1 次失败、不锁），得到 %v", lock)
	}
}

// 全局预算兜住"换着 IP 猜"：超出窗口预算就短暂 429，窗口滑过自动恢复。
func TestLoginGuardGlobalBudget(t *testing.T) {
	g := &loginGuard{cfg: loginGuardConfig{
		threshold: 5, baseLock: time.Minute, maxLock: time.Minute,
		decay: time.Hour, globalWindow: 80 * time.Millisecond, globalBudget: 2, maxEntries: 64,
	}}
	if _, throttled := g.globalThrottledFor(); throttled {
		t.Fatal("空窗口不该限流")
	}
	g.fail("198.51.100.1")
	g.fail("198.51.100.2")
	if _, throttled := g.globalThrottledFor(); !throttled {
		t.Fatal("超过全局预算应限流")
	}
	time.Sleep(100 * time.Millisecond)
	if _, throttled := g.globalThrottledFor(); throttled {
		t.Fatal("窗口滑过后应恢复")
	}
}

// 反代头只在直连方是 loopback 时才可信：直接暴露 8787 时伪造 CF-Connecting-IP
// 不能绕过按来源的节流。
func TestLoginClientIPTrustsHeadersOnlyFromLoopback(t *testing.T) {
	cases := []struct {
		remote, cf, real, xff, want string
	}{
		{"127.0.0.1:5555", "203.0.113.5", "", "", "203.0.113.5"},
		{"127.0.0.1:5555", "", "203.0.113.6", "", "203.0.113.6"},
		{"127.0.0.1:5555", "", "", "203.0.113.7, 10.0.0.1", "203.0.113.7"},
		{"127.0.0.1:5555", "not-an-ip", "", "", "127.0.0.1"},
		{"198.51.100.20:5555", "203.0.113.5", "203.0.113.6", "203.0.113.7", "198.51.100.20"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/api/auth/login", nil)
		r.RemoteAddr = c.remote
		if c.cf != "" {
			r.Header.Set("CF-Connecting-IP", c.cf)
		}
		if c.real != "" {
			r.Header.Set("X-Real-IP", c.real)
		}
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := loginClientIP(r); got != c.want {
			t.Fatalf("remote=%s cf=%q real=%q xff=%q → %q, want %q", c.remote, c.cf, c.real, c.xff, got, c.want)
		}
	}
}

// 安全响应头：页面与 API 都要带（登录页可被 iframe 是点击劫持面）。
func TestSecurityHeadersApplied(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/login", nil))

	want := map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Fatalf("%s=%q, want %q", k, got, v)
		}
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("CSP 缺失关键指令：%q", csp)
	}
}

// 认证端点是免认证入口，必须挡住超大请求体（项目里其它 handler 都有 MaxBytesReader）。
func TestAuthLoginRejectsOversizedBody(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	s := &Server{m: m}

	body := fmt.Sprintf(`{"username":"BLADE","password":"%s"}`, strings.Repeat("a", maxAuthBodyBytes*2))
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.authLogin(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大请求体应回 413，得到 %d：%s", w.Code, w.Body)
	}
}
