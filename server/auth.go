package server

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lin9527ya/blade/db"
	"golang.org/x/crypto/bcrypt"
)

const (
	jwtKeyFilename = "jwt.key"
	authPassKey    = "auth.password_hash"
	// jwtKeySetting 是签名密钥在库里的存放位置。和密码哈希（authPassKey）同库：
	// **同一个库 = 同一个登录域**。
	//
	// 此前密钥只落在 keyDir（可执行文件所在目录），于是同一份部署换个目录、换个
	// checkout 起来就会静默生成新 key——所有人被踢回登录页，而日志上看不出原因
	// （两份 checkout 共用一个库时，"起了 B 就把 A 的登录态清了"）。放进库之后，
	// 指向同一个库的实例（多开、双 checkout、搬目录）共用一把 key。
	jwtKeySetting = "auth.jwt_key"
	jwtTTL        = 7 * 24 * time.Hour
	keyChars      = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// 下限与 setup 页的前端校验一致——校验只放在前端等于没放，直接打 API 就能
	// 绕过。上限是 bcrypt 的硬限制：超过 72 字节 GenerateFromPassword 会返回
	// ErrPasswordTooLong，提前挡掉好过让用户收到一句含义不明的「密码加密失败」。
	minPasswordRunes = 8
	maxPasswordBytes = 72
)

// errDataSourceUnavailable 是密码相关读操作失败时统一的回复。这些 handler 绝不能
// 把"读不到"当成"没有设置"：authInit 曾因此在数据库报错时放行，让未认证请求覆盖
// 掉已有的管理员密码。
const errDataSourceUnavailable = "数据源暂时不可用，请稍后重试"

// validatePassword 返回空串表示通过，否则返回可直接展示给用户的中文原因。
func validatePassword(pw string) string {
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return fmt.Sprintf("密码长度至少 %d 位", minPasswordRunes)
	}
	if len(pw) > maxPasswordBytes {
		return fmt.Sprintf("密码长度不能超过 %d 字节", maxPasswordBytes)
	}
	return ""
}

// newJWTKey 生成一把 32 字符的随机签名密钥。
func newJWTKey() ([]byte, error) {
	buf := make([]byte, 32)
	for i := range buf {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(keyChars))))
		if err != nil {
			return nil, fmt.Errorf("generate jwt key: %w", err)
		}
		buf[i] = keyChars[n.Int64()]
	}
	return buf, nil
}

// rotateJWTKey 换一把新密钥并返回它，用于吊销既有 token（改密码时调用）。
// 换密钥即等于吊销**全部**已签发 token——本系统单用户，所以"全部"就是你自己那几个
// 会话。比引入 jti/黑名单简单，也不需要额外的存储。
//
// 库是权威存储：先写库、再刷新本地镜像。库写失败就当没轮换过（调用方会如实回
// "没有吊销"）；镜像写失败不影响"轮换已生效"这一事实，只留痕。
func rotateJWTKey(pg *db.DB, path string) ([]byte, error) {
	key, err := newJWTKey()
	if err != nil {
		return nil, err
	}
	if err := pg.SetSetting(jwtKeySetting, string(key)); err != nil {
		return nil, fmt.Errorf("write jwt key to db: %w", err)
	}
	writeJWTKeyFile(path, key)
	return key, nil
}

// loadOrCreateJWTKey 取签名密钥，**以库为准**：同一个库 = 同一个登录域。
//
// keyDir 仍是可执行文件所在目录（**不是**可浏览的 dataDir——签名密钥不能被文件
// 管理器列到/下走），但那里的 jwt.key 现在只是本地镜像，服务于两种场景：回滚到
// 只认文件的旧版本、以及库暂时读不到。老安装把密钥放在 dataDir/jwt.key，首次启动
// 会被迁进库并从工作区删除。
func loadOrCreateJWTKey(pg *db.DB, keyDir, dataDir string) ([]byte, error) {
	path := filepath.Join(keyDir, jwtKeyFilename)

	// 1) 库里有 → 以库为准，顺手把本地镜像刷成同一把。
	if key, ok, err := storedJWTKey(pg); err != nil {
		return nil, err
	} else if ok {
		writeJWTKeyFile(path, key)
		return key, nil
	}

	// 2) 库里还没有 → 把本地已有的 key 迁进库，既有 token 继续有效（不把人踢下线）。
	//    keyDir 优先，其次是老安装的 dataDir 位置。
	for _, candidate := range []string{path, filepath.Join(dataDir, jwtKeyFilename)} {
		key, ok := readJWTKeyFile(candidate)
		if !ok {
			continue
		}
		winner, err := storeJWTKey(pg, key)
		if err != nil {
			return nil, err
		}
		if candidate != path {
			// 老位置在可浏览的工作区里：迁完即删，并在新位置补一份镜像。
			_ = os.Remove(candidate)
			writeJWTKeyFile(path, winner)
		}
		log.Printf("[auth] JWT key 已从 %s 迁入库：此后指向同一个库的实例共用一把 key，换目录启动不会再把人踢下线", candidate)
		return winner, nil
	}

	// 3) 都没有 → 新生成一把，写库 + 落盘镜像。
	key, err := newJWTKey()
	if err != nil {
		return nil, err
	}
	winner, err := storeJWTKey(pg, key)
	if err != nil {
		return nil, err
	}
	writeJWTKeyFile(path, winner)
	log.Printf("[auth] 新 JWT key 已写入库与 %s", path)
	return winner, nil
}

// storedJWTKey 读库里的签名密钥。长度不足 32 一律当没有（半截值不该被当成密钥用）。
func storedJWTKey(pg *db.DB) ([]byte, bool, error) {
	if pg == nil {
		return nil, false, nil
	}
	v, ok, err := pg.GetSetting(jwtKeySetting)
	if err != nil {
		return nil, false, fmt.Errorf("read jwt key from db: %w", err)
	}
	if !ok || len(strings.TrimSpace(v)) < 32 {
		return nil, false, nil
	}
	return []byte(strings.TrimSpace(v)), true, nil
}

// storeJWTKey 只在库里还没有密钥时写入，并返回最终生效的那把：两个实例同时冷启动
// 时，后到的那个用先到者写下的 key，而不是各拿各的——各拿各的等于又把登录域劈成两半。
func storeJWTKey(pg *db.DB, key []byte) ([]byte, error) {
	if pg == nil {
		return key, nil
	}
	inserted, err := pg.InsertSettingIfAbsent(jwtKeySetting, string(key))
	if err != nil {
		return nil, fmt.Errorf("write jwt key to db: %w", err)
	}
	if inserted {
		return key, nil
	}
	// 并发下别人先写了：以先到者那把为准。若那一行存在却读不出可用密钥（空串、半截
	// 值），就用本次这把覆盖——一行脏数据不该让服务再也起不来。
	existing, ok, err := storedJWTKey(pg)
	if err != nil {
		return nil, err
	}
	if ok {
		return existing, nil
	}
	if err := pg.SetSetting(jwtKeySetting, string(key)); err != nil {
		return nil, fmt.Errorf("write jwt key to db: %w", err)
	}
	return key, nil
}

// readJWTKeyFile 读本地密钥文件；读不到或长度不足 32 都返回 ok=false。
func readJWTKeyFile(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	key := strings.TrimSpace(string(data))
	if len(key) < 32 {
		return nil, false
	}
	return []byte(key), true
}

// writeJWTKeyFile 刷新本地镜像。写不进去不该拦住启动——库里那把才是权威——但必须留痕，
// 否则"回滚到旧版本仍能沿用登录态"这条会在无声中失效。内容已经一致时不写，省得每次
// 启动都动 mtime。
func writeJWTKeyFile(path string, key []byte) {
	if cur, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(cur)) == string(key) {
		return
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		log.Printf("[auth] 本地 jwt.key 镜像写入 %s 失败（不影响本次启动，但回滚到旧版本时会另生成新 key）: %v", path, err)
	}
}

// signingKey 持有当前的 HS256 签名密钥。用 atomic 而不是裸 []byte 是因为改密码会**轮换**
// 它（见 authChangePassword），而 requireAuth 每个请求都要读——轮换与并发读必须不打架。
// 包成结构体、只经方法访问，是为了让"绕过锁直接读字段"在编译期就不成立。
type signingKey struct{ v atomic.Value } // []byte

func (k *signingKey) get() []byte {
	b, _ := k.v.Load().([]byte)
	return b
}

func (k *signingKey) set(b []byte) { k.v.Store(b) }

// loginGuard 是登录失败节流。本系统用户名固定、密码是唯一的门，而 bcrypt 只让每次尝试变慢、
// 不限制次数——没有节流就等于把门敞着让在线爆破随便试。失败累计到阈值后锁一段时间，成功后清零。
// 状态在内存、重启归零：目的是抬高在线爆破成本，不是持久化封禁（单机自用也不需要后者）。
type loginGuard struct {
	mu          sync.Mutex
	failures    int
	lockedUntil time.Time
}

const (
	loginMaxFailures = 5
	loginLockoutFor  = 5 * time.Minute
)

// lockedFor 在锁定期内返回剩余时长与 true。
func (g *loginGuard) lockedFor() (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if left := time.Until(g.lockedUntil); left > 0 {
		return left, true
	}
	return 0, false
}

// fail 记一次失败；达到阈值就开启（或延长）锁定期并返回它。
func (g *loginGuard) fail() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures++
	if g.failures >= loginMaxFailures {
		g.lockedUntil = time.Now().Add(loginLockoutFor)
		return loginLockoutFor
	}
	return 0
}

func (g *loginGuard) succeed() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures = 0
	g.lockedUntil = time.Time{}
}

// signJWT issues a 7-day HS256 token for user BLADE.
func signJWT(key []byte) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "BLADE",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(jwtTTL)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}).SignedString(key)
}

// verifyJWT returns true when tokenStr is a valid, non-expired HS256 token.
func verifyJWT(tokenStr string, key []byte) bool {
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	})
	return err == nil && t.Valid
}

// extractToken reads the JWT from Authorization: Bearer header,
// blade_token cookie, or ?token= query param (for SSE connections).
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("blade_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// requireAuth wraps h with JWT validation.
// /api/auth/* and /api/health are exempt.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/auth/") || p == "/api/health" {
			h.ServeHTTP(w, r)
			return
		}
		tok := extractToken(r)
		if tok == "" {
			writeErr(w, 401, "未授权")
			return
		}
		if !verifyJWT(tok, s.auth.signKey.get()) {
			writeErr(w, 401, "token 无效或已过期")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// GET /api/auth/status — reports whether the admin password has been initialised.
// 读失败必须回 503 而不是 initialized:false：前端在 initialized:false 时会把用户
// 送到 /setup 去设置密码（login/page.tsx），把数据库故障包装成 200 等于把用户往
// 覆盖已有密码的路上推。
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	hash, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, map[string]any{"initialized": hash != ""})
}

// POST /api/auth/init — sets the password for the first time; rejected if already set.
func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	existing, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if existing != "" {
		writeErr(w, 403, "密码已设置")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil || req.Password == "" {
		writeErr(w, 400, "密码不能为空")
		return
	}
	if msg := validatePassword(req.Password); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "密码加密失败")
		return
	}
	// 用 INSERT ... ON CONFLICT DO NOTHING 而不是 upsert：上面那次 GetSetting 只是
	// 快速失败路径，真正"仅首次可设"的保证落在主键约束上。bcrypt 要跑几十毫秒，
	// 这期间别的请求完全可能先把密码设好，而读检查本身也可能因故障而失效。
	inserted, err := pg.InsertSettingIfAbsent(authPassKey, string(hash))
	if err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	if !inserted {
		writeErr(w, 403, "密码已设置")
		return
	}
	tok, err := signJWT(s.auth.signKey.get())
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}

// POST /api/auth/change-password — changes the admin password. Requires a valid
// token (this route is under /api/auth/* which requireAuth exempts, so the token
// is validated here) AND the current password.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if !verifyJWT(extractToken(r), s.auth.signKey.get()) {
		writeErr(w, 401, "未授权")
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if req.NewPassword == "" {
		writeErr(w, 400, "新密码不能为空")
		return
	}
	if msg := validatePassword(req.NewPassword); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, "密码未初始化，请先设置密码")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err != nil {
		writeErr(w, 401, "当前密码错误")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "密码加密失败")
		return
	}
	if err := pg.SetSetting(authPassKey, string(newHash)); err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	// 改密码即轮换签名密钥 = 立刻吊销此前签发的**所有** token。不这么做的话，被偷走的那个
	// 7 天 token 会一直有效到自然过期——改密码就失去了「止损」的意义。（本系统单用户，
	// 所以「所有 token」就是你自己那几个会话；轮换后本次请求所用的 token 也随失效，
	// 前端会退回登录页重新登录，这是预期行为。）
	newKey, rotErr := rotateJWTKey(pg, s.auth.jwtKeyPath)
	if rotErr != nil {
		// 密码已经改成功了，但旧 token 没失效——如实说，别报个 ok 让人以为已经止损。
		log.Printf("[auth] 改密码后轮换 JWT key 失败: %v", rotErr)
		writeJSON(w, 200, map[string]any{
			"ok": true, "tokens_revoked": false,
			"warning": "密码已更新，但签名密钥轮换失败：此前签发的 token 仍然有效。请重试改密码或重启服务。",
		})
		return
	}
	s.auth.signKey.set(newKey)
	log.Printf("[auth] 密码已更新，JWT key 已轮换（既有 token 全部失效）")
	writeJSON(w, 200, map[string]any{"ok": true, "tokens_revoked": true})
}

// POST /api/auth/login — validates username/password and returns a JWT.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	// 节流先于一切：锁定期内连用户名/密码都不看，直接拒——否则爆破者仍能用每次
	// 请求的时序/回包差异继续试探。
	if left, locked := s.auth.login.lockedFor(); locked {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(left.Seconds())+1))
		writeErr(w, 429, fmt.Sprintf("登录失败次数过多，请 %s 后重试", left.Round(time.Second)))
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if req.Username != "BLADE" {
		s.auth.login.fail()
		writeErr(w, 401, "用户名或密码错误")
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, "密码未初始化，请先设置密码")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		if lockFor := s.auth.login.fail(); lockFor > 0 {
			writeErr(w, 429, fmt.Sprintf("登录失败次数过多，已锁定 %s", lockFor))
			return
		}
		writeErr(w, 401, "用户名或密码错误")
		return
	}
	s.auth.login.succeed()
	tok, err := signJWT(s.auth.signKey.get())
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}
