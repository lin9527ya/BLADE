package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// GetSetting 对"键不存在"和"读取出错"的返回值只差一个 error：两种情况 value 都是
// 空串。密码相关的 handler 一旦把 error 当成"还没设置密码"，就会在数据库抖动期间
// 敞开初始化入口——authInit 会放行一个未认证请求去覆盖已有的管理员密码，
// authStatus 则会把前端直接送到 /setup 去照着做这件事。
//
// 这两个测试把 handler 的连接池关掉来制造读取失败，断言两处都 fail closed。
func TestAuthStatusFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	// 关掉池子，让后续 GetSetting 返回 error 而不是 sql.ErrNoRows。
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	w := httptest.NewRecorder()
	s.authStatus(w, httptest.NewRequest("GET", "/api/auth/status", nil))

	if w.Code != 503 {
		t.Fatalf("status=%d want 503 (读失败被当成未初始化会把用户送去 /setup 覆盖密码); body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Initialized *bool `json:"initialized"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err == nil && payload.Initialized != nil {
		t.Fatalf("读失败时不应回答 initialized，得到 %v", *payload.Initialized)
	}
}

func TestAuthInitFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"password":"correct horse battery"}`)
	s.authInit(w, httptest.NewRequest("POST", "/api/auth/init", body))

	if w.Code != 503 {
		t.Fatalf("status=%d want 503 (读失败时放行会让未认证请求覆盖已有密码); body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "token") {
		t.Fatalf("读失败时不应签发 token: %s", w.Body.String())
	}
}

func TestValidatePassword(t *testing.T) {
	for _, tc := range []struct {
		name, pw string
		wantErr  bool
	}{
		{"空", "", true},
		{"七位", "1234567", true},
		{"八位", "12345678", false},
		{"八个汉字按字符数而非字节数计", "密码密码密码密码", false},
		{"三个汉字够 9 字节但只有 3 个字符", "密码强", true},
		{"72 字节", strings.Repeat("a", 72), false},
		{"73 字节超出 bcrypt 上限", strings.Repeat("a", 73), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validatePassword(tc.pw); (got != "") != tc.wantErr {
				t.Fatalf("validatePassword(%q)=%q, wantErr=%v", tc.pw, got, tc.wantErr)
			}
		})
	}
}

// seedPassword 直接写 password_hash setting，跳过 /setup 流程建一个可登录的账号，并返回一个
// **还原**函数。共享测试库里还回去是必要的：泄漏的 setting 正是不该留的东西（server 包的
// TestMain 专门清 llm_profiles 就是被这类残留坑过）。
//
// 调用方要写成 `defer m.Close()` 在前、`defer restore()` 在后——defer 后进先出，还原才会
// 在连接池关闭之前跑；用 t.Cleanup 不行，它在 defer 之后才执行、那时池子已经关了。
func seedPassword(t *testing.T, m *Manager, pw string) (restore func()) {
	t.Helper()
	prev, had, err := m.pg.GetSetting(authPassKey)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting(authPassKey, string(hash)); err != nil {
		t.Fatal(err)
	}
	return func() {
		if had {
			_ = m.pg.SetSetting(authPassKey, prev)
			return
		}
		_ = m.pg.SetSetting(authPassKey, "")
	}
}

// TestJWTKeyFollowsTheDatabase 钉住这条：签名密钥跟**库**走，不跟可执行文件所在目录走。
//
// 密钥只落在 keyDir 时，同一份部署换个目录/换个 checkout 起来就会静默生成新 key，
// 把所有人踢回登录页——两份 checkout 共用一个库的场景下，这等于「起了 B 就把 A 的
// 登录态清了」，而日志上只有一行"新 JWT key 已写入"。
func TestJWTKeyFollowsTheDatabase(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	defer clearJWTKeySetting(t, m)()

	dirA, dirB := t.TempDir(), t.TempDir()
	keyA, err := loadOrCreateJWTKey(m.pg, dirA, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := loadOrCreateJWTKey(m.pg, dirB, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if string(keyA) != string(keyB) {
		t.Fatal("换个目录起同一份部署拿到了不同的签名密钥——此前签发的 token 会被判无效，所有人被踢回登录页")
	}
	// 第二个目录也要留下本地镜像：回滚到只认文件的旧版本时仍沿用同一把。
	mirrored, ok := readJWTKeyFile(filepath.Join(dirB, jwtKeyFilename))
	if !ok || string(mirrored) != string(keyB) {
		t.Fatalf("keyDir 里没有留下镜像: ok=%v got=%q", ok, mirrored)
	}
}

// TestJWTKeyMigratesLegacyWorkspaceFile 老安装把密钥放在可浏览的 dataDir 里：首次启动
// 要迁进库、并**从工作区删掉**（签名密钥不该能被文件管理器下走），同时既有 token 不能失效。
func TestJWTKeyMigratesLegacyWorkspaceFile(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	defer clearJWTKeySetting(t, m)()

	dataDir, keyDir := t.TempDir(), t.TempDir()
	legacy, err := newJWTKey()
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(dataDir, jwtKeyFilename)
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadOrCreateJWTKey(m.pg, keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(legacy) {
		t.Fatal("迁移没有保留原有密钥——老安装的 token 会全部失效")
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatal("老位置的 jwt.key 没有被删掉：它还躺在可浏览的工作区里")
	}
	if mirrored, ok := readJWTKeyFile(filepath.Join(keyDir, jwtKeyFilename)); !ok || string(mirrored) != string(legacy) {
		t.Fatalf("迁移后没在 keyDir 留下镜像: ok=%v", ok)
	}
}

// clearJWTKeySetting 清掉库里的签名密钥并返回还原函数。测试与真实部署可能共用同一个
// 库（本地跑 `go test ./server/` 就是），别把别人的登录态带走。
func clearJWTKeySetting(t *testing.T, m *Manager) (restore func()) {
	t.Helper()
	prev, had, err := m.pg.GetSetting(jwtKeySetting)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`DELETE FROM settings WHERE key=$1`, jwtKeySetting); err != nil {
		t.Fatal(err)
	}
	return func() {
		if had {
			_ = m.pg.SetSetting(jwtKeySetting, prev)
			return
		}
		_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, jwtKeySetting)
	}
}

// TestChangePasswordRevokesExistingTokens 钉住改密码的「止损」语义：改完密码，此前签发的
// token 必须立刻失效。不轮换签名密钥的话，被偷走的那个 7 天 token 会一直有效到自然过期——
// 那这个功能就只是个摆设，用户以为改了密码就安全了。
func TestChangePasswordRevokesExistingTokens(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	defer seedPassword(t, m, "old-password-123")()

	s := &Server{m: m, auth: authState{jwtKeyPath: filepath.Join(t.TempDir(), jwtKeyFilename)}}
	oldKey, err := newJWTKey()
	if err != nil {
		t.Fatal(err)
	}
	s.auth.signKey.set(oldKey)
	oldToken, err := signJWT(s.auth.signKey.get())
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/change-password",
		strings.NewReader(`{"old_password":"old-password-123","new_password":"new-password-456"}`))
	req.Header.Set("Authorization", "Bearer "+oldToken)
	s.authChangePassword(w, req)
	if w.Code != 200 {
		t.Fatalf("改密码应 200，得到 %d：%s", w.Code, w.Body.String())
	}

	if verifyJWT(oldToken, s.auth.signKey.get()) {
		t.Fatal("改密码后旧 token 仍然有效——被偷走的凭据改密码也救不回来，这条修复就白做了")
	}
	if !verifyJWT(mustSign(t, s), s.auth.signKey.get()) {
		t.Fatal("轮换后的新密钥签不出可验证的 token")
	}
}

// mustSign 用当前密钥签一个 token，用于断言轮换后的密钥是**可用**的（而不只是"不等于旧的"）。
func mustSign(t *testing.T, s *Server) string {
	t.Helper()
	tok, err := signJWT(s.auth.signKey.get())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestLoginThrottleLocksAfterRepeatedFailures 钉住登录节流。bcrypt 只让每次尝试变慢、不限制
// 次数；没有节流就等于把门敞着让在线爆破随便试。两个关键点：达阈值后**先拒**（连密码都不看，
// 否则猜对的那一次会直接放行），以及成功登录会清零。
func TestLoginThrottleLocksAfterRepeatedFailures(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	defer seedPassword(t, m, "right-password")()

	s := &Server{m: m}
	key, err := newJWTKey()
	if err != nil {
		t.Fatal(err)
	}
	s.auth.signKey.set(key)

	login := func(pw string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/login",
			strings.NewReader(`{"username":"BLADE","password":"`+pw+`"}`))
		s.authLogin(w, req)
		return w.Code
	}

	threshold := loginGuardConfig{}.withDefaults().threshold
	for i := 1; i <= threshold; i++ {
		if code := login("wrong-password"); code != 401 && code != 429 {
			t.Fatalf("第 %d 次失败应 401（达阈值那次为 429），得到 %d", i, code)
		}
	}
	if code := login("right-password"); code != 429 {
		t.Fatalf("锁定期内**即使密码正确**也应 429（节流先于校验），得到 %d", code)
	}

	s.auth.login.succeed("192.0.2.1") // httptest 的默认 RemoteAddr；等价于成功登录后的复位
	if code := login("right-password"); code != 200 {
		t.Fatalf("复位后正确密码应 200，得到 %d", code)
	}
}
