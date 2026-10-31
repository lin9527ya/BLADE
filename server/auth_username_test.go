package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedAdminUsername 设置管理员用户名并返回恢复函数（测试共用同一个库的 settings）。
func seedAdminUsername(t *testing.T, m *Manager, name string) func() {
	t.Helper()
	prev, had, err := m.pg.GetSetting(authUserKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting(authUserKey, name); err != nil {
		t.Fatal(err)
	}
	return func() {
		if had {
			_ = m.pg.SetSetting(authUserKey, prev)
			return
		}
		_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, authUserKey)
	}
}

func newAuthTestServer(t *testing.T, m *Manager) *Server {
	t.Helper()
	s := &Server{m: m}
	key, err := newJWTKey()
	if err != nil {
		t.Fatal(err)
	}
	s.auth.signKey.set(key)
	return s
}

func tryLogin(t *testing.T, s *Server, username, password string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/login",
		strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)))
	s.authLogin(w, req)
	var body struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body.Token
}

func jwtSubject(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("bad jwt: %q", tok)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims.Sub
}

// TestLoginUsesConfiguredAdminUsername：登录必须按配置的用户名校验；旧默认名不再生效，
// 错的用户名和错的密码一样失败（并照常走节流）。
func TestLoginUsesConfiguredAdminUsername(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行（Go 的测试清理顺序），所以关库
	// 只能注册成 Cleanup——写 defer 会让它先于下面的数据清理跑，清理语句全部落到
	// 已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = m.Close() })
	defer seedPassword(t, m, "right-password-123")()
	defer seedAdminUsername(t, m, "ops_admin")()

	s := newAuthTestServer(t, m)
	if code, _ := tryLogin(t, s, "BLADE", "right-password-123"); code != 401 {
		t.Fatalf("旧默认用户名不该还能登录，得到 %d", code)
	}
	if code, _ := tryLogin(t, s, "ops_admin", "wrong-password"); code != 401 {
		t.Fatalf("错密码应 401，得到 %d", code)
	}
	code, tok := tryLogin(t, s, "ops_admin", "right-password-123")
	if code != 200 || tok == "" {
		t.Fatalf("配置的用户名+正确密码应登录成功：%d %q", code, tok)
	}
	if got := jwtSubject(t, tok); got != "ops_admin" {
		t.Fatalf("token subject=%q，want ops_admin", got)
	}
}

// TestChangeUsernameRequiresPasswordAndUpdatesLogin：改用户名要当前密码；改完旧名失效、
// 新名可登，返回的新 token 的 sub 跟着变。
func TestChangeUsernameRequiresPasswordAndUpdatesLogin(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行（Go 的测试清理顺序），所以关库
	// 只能注册成 Cleanup——写 defer 会让它先于下面的数据清理跑，清理语句全部落到
	// 已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = m.Close() })
	defer seedPassword(t, m, "right-password-123")()
	defer seedAdminUsername(t, m, "ops_admin")()

	s := newAuthTestServer(t, m)
	_, tok := tryLogin(t, s, "ops_admin", "right-password-123")
	change := func(password, name string) (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/change-username",
			strings.NewReader(fmt.Sprintf(`{"password":%q,"new_username":%q}`, password, name)))
		req.Header.Set("Authorization", "Bearer "+tok)
		s.authChangeUsername(w, req)
		var body struct {
			Username string `json:"username"`
			Token    string `json:"token"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Token
	}

	if code, _ := change("wrong-password", "new_admin"); code != 401 {
		t.Fatalf("当前密码错误应 401，得到 %d", code)
	}
	code, newTok := change("right-password-123", "new_admin")
	if code != 200 || newTok == "" {
		t.Fatalf("改用户名失败：%d", code)
	}
	if got := adminUsername(m.pg); got != "new_admin" {
		t.Fatalf("用户名没落库：%q", got)
	}
	if got := jwtSubject(t, newTok); got != "new_admin" {
		t.Fatalf("新 token subject=%q，want new_admin", got)
	}
	if code, _ := tryLogin(t, s, "ops_admin", "right-password-123"); code != 401 {
		t.Fatalf("旧用户名应失效，得到 %d", code)
	}
	if code, tok := tryLogin(t, s, "new_admin", "right-password-123"); code != 200 || tok == "" {
		t.Fatalf("新用户名应能登录：%d", code)
	}
}

// TestAuthInitAcceptsUsername：首次初始化可以指定管理员用户名；不指定则仍是 BLADE。
func TestAuthInitAcceptsUsername(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行（Go 的测试清理顺序），所以关库
	// 只能注册成 Cleanup——写 defer 会让它先于下面的数据清理跑，清理语句全部落到
	// 已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = m.Close() })

	prevPass, hadPass, _ := m.pg.GetSetting(authPassKey)
	prevUser, hadUser, _ := m.pg.GetSetting(authUserKey)
	t.Cleanup(func() {
		if hadPass {
			_ = m.pg.SetSetting(authPassKey, prevPass)
		} else {
			_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, authPassKey)
		}
		if hadUser {
			_ = m.pg.SetSetting(authUserKey, prevUser)
		} else {
			_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, authUserKey)
		}
	})
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, authPassKey)
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, authUserKey)

	s := newAuthTestServer(t, m)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/init",
		strings.NewReader(`{"username":"init_admin","password":"init-password-123"}`))
	s.authInit(w, req)
	if w.Code != 200 {
		t.Fatalf("初始化失败：%d %s", w.Code, w.Body.String())
	}
	if got := adminUsername(m.pg); got != "init_admin" {
		t.Fatalf("初始化用户名没落库：%q", got)
	}
	if code, tok := tryLogin(t, s, "init_admin", "init-password-123"); code != 200 || tok == "" {
		t.Fatalf("初始化后应能用新用户名登录：%d", code)
	}
}

func TestNormalizeAdminUsername(t *testing.T) {
	for _, ok := range []string{"BLADE", "ops_admin", "ops-admin", "运维"} {
		if _, err := normalizeAdminUsername(ok); err != nil {
			t.Errorf("%q 应合法：%v", ok, err)
		}
	}
	for _, bad := range []string{"", "  ", "a b", "a\nb", strings.Repeat("a", 65)} {
		if _, err := normalizeAdminUsername(bad); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
}
