package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// TestLoginGuardAccountLockAcrossIPs：账户级连续失败跨 IP 生效；5 次后锁 10 分钟，
// 成功登录清零。
func TestLoginGuardAccountLockAcrossIPs(t *testing.T) {
	g := &loginGuard{cfg: loginGuardConfig{
		threshold: 5, baseLock: 10 * time.Minute, maxLock: 10 * time.Minute,
		decay: time.Hour, globalWindow: time.Hour, globalBudget: 100, maxEntries: 64,
		accountThreshold: 5, accountLock: 10 * time.Minute,
	}}
	for i := 0; i < 5; i++ {
		g.fail(fmt.Sprintf("198.51.100.%d", i+1))
	}
	left, locked := g.accountLockedFor()
	if !locked || left < 9*time.Minute || left > 10*time.Minute {
		t.Fatalf("5 次失败后应锁约 10 分钟，得到 locked=%v left=%v", locked, left)
	}
	// 换个来源也一样被账户锁拦住。
	if _, locked := g.lockedFor("203.0.113.50"); locked {
		t.Fatal("另一个来源不应有独立的 IP 锁（账户锁是另一层）")
	}
	g.succeed("198.51.100.1")
	if _, locked := g.accountLockedFor(); locked {
		t.Fatal("成功登录后账户锁应解除")
	}
}

// TestLoginLocksAccountAfterFiveFailures：端到端——5 次错误（来自 5 个不同 IP）后，
// 即使第 6 次用户名/密码正确也返回 429，且 Retry-After ≈ 600 秒。
func TestLoginLocksAccountAfterFiveFailures(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	defer seedPassword(t, m, "right-password-123")()
	defer seedAdminUsername(t, m, "ops_admin")()

	s := newAuthTestServer(t, m)
	s.auth.login.cfg = loginGuardConfig{
		threshold: 5, baseLock: 10 * time.Minute, maxLock: 10 * time.Minute,
		decay: time.Hour, globalWindow: time.Hour, globalBudget: 100, maxEntries: 64,
		accountThreshold: 5, accountLock: 10 * time.Minute,
	}
	login := func(remote, password string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/login",
			strings.NewReader(fmt.Sprintf(`{"username":"ops_admin","password":%q}`, password)))
		req.RemoteAddr = remote
		s.authLogin(w, req)
		return w
	}

	for i := 0; i < 5; i++ {
		w := login(fmt.Sprintf("198.51.100.%d:1234", i+1), "wrong-password")
		if i < 4 && w.Code != 401 {
			t.Fatalf("第 %d 次错误应 401，得到 %d：%s", i+1, w.Code, w.Body.String())
		}
		if i == 4 && w.Code != 429 {
			t.Fatalf("第 5 次错误应触发账户锁 429，得到 %d：%s", w.Code, w.Body.String())
		}
	}

	w := login("203.0.113.9:1234", "right-password-123")
	if w.Code != 429 || !strings.Contains(w.Body.String(), "账户已锁定") {
		t.Fatalf("锁定期内即使密码正确也应 429（账户锁跨 IP），得到 %d：%s", w.Code, w.Body.String())
	}
	if ra := w.Header().Get("Retry-After"); ra == "" || ra < "590" {
		t.Fatalf("Retry-After 应约 600 秒，得到 %q", ra)
	}
}

// TestJWTOnlyAcceptsHS256：同一把密钥签名但换算法的 token 必须被拒。
func TestJWTOnlyAcceptsHS256(t *testing.T) {
	key, err := newJWTKey()
	if err != nil {
		t.Fatal(err)
	}
	claims := jwt.RegisteredClaims{
		Subject:   "admin",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}
	hs256, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyJWT(hs256, key) {
		t.Fatal("HS256 token should verify")
	}
	hs512, err := jwt.NewWithClaims(jwt.SigningMethodHS512, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	if verifyJWT(hs512, key) {
		t.Fatal("HS512 token must be rejected")
	}
	// 无 exp 的 token 不再被接受（WithExpirationRequired）。
	noExp, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "admin"}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	if verifyJWT(noExp, key) {
		t.Fatal("token without exp must be rejected")
	}
}

// TestQueryTokenOnlyForSSE：?token= 只在 EventSource 请求上被读取。
func TestQueryTokenOnlyForSSE(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/logs/stream?token=abc", nil)
	r.Header.Set("Accept", "text/event-stream")
	if got := extractToken(r); got != "abc" {
		t.Fatalf("SSE query token = %q, want abc", got)
	}
	plain := httptest.NewRequest(http.MethodGet, "/api/findings?token=abc", nil)
	if got := extractToken(plain); got != "" {
		t.Fatalf("non-SSE query token = %q, want empty", got)
	}
	// 显式 Authorization 头仍然优先，且不依赖 Accept。
	hdr := httptest.NewRequest(http.MethodGet, "/api/findings", nil)
	hdr.Header.Set("Authorization", "Bearer xyz")
	if got := extractToken(hdr); got != "xyz" {
		t.Fatalf("header token = %q, want xyz", got)
	}
}

// TestChangePasswordWrongOldPasswordLocksAccount：持有效 token 在线猜当前密码，
// 与登录共用失败计数：5 次后账户锁定，正确密码也进不来。
func TestChangePasswordWrongOldPasswordLocksAccount(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := newAuthTestServer(t, m)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	prev, had, err := m.pg.GetSetting(authPassKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting(authPassKey, string(hash)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if had {
			_ = m.pg.SetSetting(authPassKey, prev)
		} else {
			_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, authPassKey)
		}
	}()
	tok, err := signJWTFor(s.auth.signKey.get(), adminUsername(m.pg))
	if err != nil {
		t.Fatal(err)
	}
	call := func(ip, oldPW string) int {
		body := fmt.Sprintf(`{"old_password":%q,"new_password":"new-password-123"}`, oldPW)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", strings.NewReader(body))
		req.RemoteAddr = ip
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		s.authChangePassword(rec, req)
		return rec.Code
	}
	for i := 0; i < 5; i++ {
		code := call(fmt.Sprintf("203.0.113.%d:1234", i+1), "wrong-password")
		if i < 4 && code != http.StatusUnauthorized {
			t.Fatalf("attempt %d code=%d, want 401", i+1, code)
		}
		if i == 4 && code != http.StatusTooManyRequests {
			t.Fatalf("attempt 5 code=%d, want 429", code)
		}
	}
	if code := call("198.51.100.7:1234", "correct-password"); code != http.StatusTooManyRequests {
		t.Fatalf("correct password while locked code=%d, want 429", code)
	}
}
