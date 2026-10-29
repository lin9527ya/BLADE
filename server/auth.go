package server

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lin9527ya/blade/db"
	"golang.org/x/crypto/bcrypt"
)

const (
	jwtKeyFilename = "jwt.key"
	authPassKey    = "auth.password_hash"
	// authUserKey 是管理员用户名。缺省/空 = 出厂默认 defaultAdminUsername（兼容老库）。
	authUserKey          = "auth.username"
	defaultAdminUsername = "BLADE"
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

// maxAuthBodyBytes 是认证接口的请求体上限：它们是免认证入口，不设限等于任人打大包。
// 正常的登录/初始化/改密码请求都不到 1KB，4KB 足够。
const maxAuthBodyBytes = 4 << 10

// decodeAuthBody 解析认证接口的小 JSON 体；超过上限回 413，格式错误回 400。
func decodeAuthBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	if err := decode(r, v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求体过大")
		} else {
			writeErr(w, http.StatusBadRequest, "请求格式错误")
		}
		return false
	}
	return true
}

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

// loginGuardConfig 是节流参数。零值（threshold==0）= 用内置默认；测试可注入小值。
type loginGuardConfig struct {
	threshold    int           // 同一来源连续失败几次开始锁
	baseLock     time.Duration // 第一次锁多久（之后每次失败翻倍）
	maxLock      time.Duration // 单次锁的上限
	decay        time.Duration // 这么久没失败，把该来源的计数清零（否则会永久停在"一次尝试/锁期"）
	globalWindow time.Duration // 全局失败预算的滑动窗口
	globalBudget int           // 窗口内全局失败上限（分布式换 IP 的兜底；<=0 = 关闭）
	maxEntries   int           // 跟踪的来源上限（换 IP 打爆内存时的止损）
	// 账户级连续失败锁：跨 IP 生效（同一管理员账户，换 IP 也绕不过）。<=0 = 关闭
	// （只保留按来源的节流；自定义配置的测试不受影响）。
	accountThreshold int
	accountLock      time.Duration
}

func (c loginGuardConfig) withDefaults() loginGuardConfig {
	if c.threshold > 0 {
		return c
	}
	return loginGuardConfig{
		threshold:        5,
		baseLock:         10 * time.Minute,
		maxLock:          10 * time.Minute,
		decay:            30 * time.Minute,
		globalWindow:     10 * time.Minute,
		globalBudget:     60,
		maxEntries:       4096,
		accountThreshold: 5,
		accountLock:      10 * time.Minute,
	}
}

type loginEntry struct {
	failures    int
	lockedUntil time.Time
	lastFail    time.Time
}

// loginGuard 是登录失败节流。本系统用户名固定、密码是唯一的门，而 bcrypt 只让每次尝试变慢、
// 不限制次数——没有节流就等于把门敞着让在线爆破随便试。状态在内存、重启归零：目的是抬高
// 在线爆破成本，不是持久化封禁（单机自用也不需要后者）。
//
// **按来源 IP 分开计数**——此前只有一个全局计数器：
// 任何人（甚至一次手滑）失败 5 次就把所有人锁 5 分钟，锁定期满后计数还不衰减，
// 于是"每 4 分钟发一次错误登录"就能把人永久锁在门外。
//
// 现在：同一来源指数退避、久不失败自动衰减；另有一层**全局失败预算**兜住"换着 IP
// 猜"的分布式爆破（超出预算时短暂 429，不是长期锁）。内存态、进程重启即清零。
type loginGuard struct {
	mu          sync.Mutex
	cfg         loginGuardConfig
	perIP       map[string]*loginEntry
	globalFails []time.Time // 滑动窗口内的失败时刻
	// 账户级连续失败（跨 IP）：成功后清零；达到阈值锁 accountLock（默认 10 分钟）。
	// 锁期结束后需要重新累计，不会"一次失败就续锁"。
	accountFails       int
	accountLockedUntil time.Time
}

func (g *loginGuard) config() loginGuardConfig { return g.cfg.withDefaults() }

// lockedFor 返回该来源剩余锁定时长。
func (g *loginGuard) lockedFor(ip string) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.perIP[ip]
	if e == nil {
		return 0, false
	}
	if left := time.Until(e.lockedUntil); left > 0 {
		return left, true
	}
	return 0, false
}

// accountLockedFor 返回账户级剩余锁定时长（跨 IP）。未启用时恒为 false。
func (g *loginGuard) accountLockedFor() (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cfg := g.config()
	if cfg.accountThreshold <= 0 || cfg.accountLock <= 0 {
		return 0, false
	}
	if left := time.Until(g.accountLockedUntil); left > 0 {
		return left, true
	}
	return 0, false
}

// globalThrottledFor 报告全局失败预算是否已用尽，并给出还要等多久（最早那次失败滑出窗口）。
func (g *loginGuard) globalThrottledFor() (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cfg := g.config()
	if cfg.globalBudget <= 0 {
		return 0, false
	}
	g.pruneGlobalLocked(cfg, time.Now())
	if len(g.globalFails) < cfg.globalBudget {
		return 0, false
	}
	return cfg.globalWindow - time.Since(g.globalFails[0]), true
}

// fail 记一次失败：先做衰减，再按阈值指数退避，返回锁定时长（0 = 不锁）。
func (g *loginGuard) fail(ip string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	cfg := g.config()
	now := time.Now()
	g.pruneGlobalLocked(cfg, now)
	g.globalFails = append(g.globalFails, now)

	// 账户级连续失败：跨 IP 计数；达到阈值锁 10 分钟（默认），并清零——
	// 锁期过后需要重新累计 5 次，不会因为"再错一次"立刻续锁。
	accountLock := time.Duration(0)
	if cfg.accountThreshold > 0 && cfg.accountLock > 0 {
		g.accountFails++
		if g.accountFails >= cfg.accountThreshold {
			g.accountLockedUntil = now.Add(cfg.accountLock)
			g.accountFails = 0
		}
		accountLock = time.Until(g.accountLockedUntil)
		if accountLock < 0 {
			accountLock = 0
		}
	}

	if g.perIP == nil { // 零值可用：Server 不显式构造这个 guard
		g.perIP = make(map[string]*loginEntry)
	}
	e := g.perIP[ip]
	if e == nil {
		// 来源上限：满了就不再跟踪新来源（否则换 IP 能把内存打爆）。此时仍受全局预算约束。
		if len(g.perIP) >= cfg.maxEntries {
			return accountLock // 账户锁仍然要如实回给调用方
		}
		e = &loginEntry{}
		g.perIP[ip] = e
	}
	// 衰减：很久没失败就从零开始，避免永久停在"一次尝试/锁期"。
	if cfg.decay > 0 && !e.lastFail.IsZero() && now.Sub(e.lastFail) > cfg.decay {
		e.failures = 0
	}
	e.failures++
	e.lastFail = now
	if e.failures < cfg.threshold {
		return accountLock
	}
	lock := cfg.baseLock
	// failures = threshold + k → 翻倍 k 次（threshold 那一次是 baseLock）。
	for i := cfg.threshold; i < e.failures && lock < cfg.maxLock; i++ {
		lock *= 2
	}
	if lock > cfg.maxLock {
		lock = cfg.maxLock
	}
	e.lockedUntil = now.Add(lock)
	if left := time.Until(g.accountLockedUntil); left > lock {
		lock = left // 账户锁优先：跨 IP 的锁期要如实回给调用方
	}
	return lock
}

func (g *loginGuard) succeed(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.perIP, ip)
	g.accountFails = 0
	g.accountLockedUntil = time.Time{}
}

func (g *loginGuard) pruneGlobalLocked(cfg loginGuardConfig, now time.Time) {
	if cfg.globalWindow <= 0 || len(g.globalFails) == 0 {
		return
	}
	cut := now.Add(-cfg.globalWindow)
	keep := g.globalFails[:0]
	for _, t := range g.globalFails {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	g.globalFails = keep
}

// loginClientIP 取登录请求的真实来源。
//
// 反代形态（cloudflared / nginx 在本机）下 Go 看到的都是 127.0.0.1，真实来源在
// CF-Connecting-IP / X-Real-IP / X-Forwarded-For 里；**但只有直连方是 loopback 时才信这些头**
// ——否则把 9898 直接暴露到公网时，任何人伪造一个头就能绕过按来源的节流。
func loginClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		for _, h := range []string{"CF-Connecting-IP", "X-Real-IP", "X-Forwarded-For"} {
			v := strings.TrimSpace(r.Header.Get(h))
			if i := strings.IndexByte(v, ','); i >= 0 { // XFF: 取最左（客户端）
				v = strings.TrimSpace(v[:i])
			}
			if net.ParseIP(v) != nil {
				return v
			}
		}
	}
	return host
}

// signJWT issues a 7-day HS256 token for the default admin account. Kept for
// tests; the login/init paths use signJWTFor with the configured username.
func signJWT(key []byte) (string, error) { return signJWTFor(key, defaultAdminUsername) }

// signJWTFor issues a token whose subject is the configured admin username.
func signJWTFor(key []byte, username string) (string, error) {
	if strings.TrimSpace(username) == "" {
		username = defaultAdminUsername
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   username,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(jwtTTL)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}).SignedString(key)
}

// adminUsername returns the configured admin username, falling back to the
// factory default for installs created before the setting existed.
func adminUsername(pg *db.DB) string {
	v, ok, err := pg.GetSetting(authUserKey)
	if err != nil || !ok {
		return defaultAdminUsername
	}
	if v = strings.TrimSpace(v); v == "" {
		return defaultAdminUsername
	}
	return v
}

// normalizeAdminUsername validates a user-supplied admin username. No spaces or
// control characters: it is a login principal, not a display name.
func normalizeAdminUsername(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", errors.New("用户名不能为空")
	}
	if utf8.RuneCountInString(u) > 64 {
		return "", errors.New("用户名最多 64 个字符")
	}
	if strings.IndexFunc(u, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", errors.New("用户名不能包含空白或控制字符")
	}
	return u, nil
}

// verifyJWT returns true when tokenStr is a valid, non-expired HS256 token.
// 只认 HS256 本身：接受任意 HMAC 变体会让同一把密钥下的算法选择权落在攻击者手里
// （alg 由 token 头决定），历史上这是 JWT 库的经典坑。签发端也只用 HS256。
func verifyJWT(tokenStr string, key []byte) bool {
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %q", t.Method.Alg())
		}
		return key, nil
	}, jwt.WithExpirationRequired())
	return err == nil && t.Valid
}

// extractToken reads the JWT from Authorization: Bearer header,
// blade_token cookie, or ?token= query param (SSE only).
//
// 查询串凭据只对 EventSource 生效：浏览器会自动给它带 Accept:
// text/event-stream。普通请求/页面导航拿不到这条通道，于是 ?token= 不会变成
// 通用认证方式，也不会因为用户把地址贴进聊天框/浏览器历史而多一份凭据副本。
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("blade_token"); err == nil && c.Value != "" {
		return c.Value
	}
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return r.URL.Query().Get("token")
	}
	return ""
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
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	if req.Password == "" {
		writeErr(w, 400, "密码不能为空")
		return
	}
	if msg := validatePassword(req.Password); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	username := defaultAdminUsername
	if strings.TrimSpace(req.Username) != "" {
		u, uerr := normalizeAdminUsername(req.Username)
		if uerr != nil {
			writeErr(w, 400, uerr.Error())
			return
		}
		username = u
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
	// 只有赢得密码插入的请求才能写用户名，避免并发初始化时后来者覆盖。
	if err := pg.SetSetting(authUserKey, username); err != nil {
		log.Printf("[auth] 保存管理员用户名失败（将使用默认 %s）: %v", defaultAdminUsername, err)
		username = adminUsername(pg)
	}
	tok, err := signJWTFor(s.auth.signKey.get(), username)
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok, "username": username})
}

// GET /api/auth/account — returns the configured admin username (auth required).
func (s *Server) authAccount(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if !verifyJWT(extractToken(r), s.auth.signKey.get()) {
		writeErr(w, 401, "未授权")
		return
	}
	writeJSON(w, 200, map[string]any{"username": adminUsername(pg)})
}

// POST /api/auth/change-username — changes the admin username. Requires a valid
// token and the current password. Returns a fresh token whose subject is the new
// username so the UI/sidebar can update without a logout.
func (s *Server) authChangeUsername(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if !verifyJWT(extractToken(r), s.auth.signKey.get()) {
		writeErr(w, 401, "未授权")
		return
	}
	ip := loginClientIP(r)
	if left, locked := s.auth.login.accountLockedFor(); locked {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(left.Seconds())+1))
		writeErr(w, 429, fmt.Sprintf("登录失败次数过多，账户已锁定，请 %s 后重试", left.Round(time.Second)))
		return
	}
	var req struct {
		Password    string `json:"password"`
		NewUsername string `json:"new_username"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	newName, err := normalizeAdminUsername(req.NewUsername)
	if err != nil {
		writeErr(w, 400, err.Error())
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
		if lockFor := s.auth.login.fail(ip); lockFor > 0 {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(lockFor.Seconds())+1))
			writeErr(w, 429, fmt.Sprintf("登录失败次数过多，已锁定 %s", lockFor.Round(time.Second)))
			return
		}
		writeErr(w, 401, "当前密码错误")
		return
	}
	s.auth.login.succeed(ip)
	if err := pg.SetSetting(authUserKey, newName); err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	tok, err := signJWTFor(s.auth.signKey.get(), newName)
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	log.Printf("[auth] 管理员用户名已更新")
	writeJSON(w, 200, map[string]any{"ok": true, "username": newName, "token": tok})
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
	ip := loginClientIP(r)
	if left, locked := s.auth.login.accountLockedFor(); locked {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(left.Seconds())+1))
		writeErr(w, 429, fmt.Sprintf("登录失败次数过多，账户已锁定，请 %s 后重试", left.Round(time.Second)))
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decodeAuthBody(w, r, &req) {
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
		// 持有效 token 也可能被拿来在线猜当前密码；与登录共用同一套失败计数与锁。
		if lockFor := s.auth.login.fail(ip); lockFor > 0 {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(lockFor.Seconds())+1))
			writeErr(w, 429, fmt.Sprintf("登录失败次数过多，已锁定 %s", lockFor.Round(time.Second)))
			return
		}
		writeErr(w, 401, "当前密码错误")
		return
	}
	s.auth.login.succeed(ip)
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
	// 请求的时序/回包差异继续试探。计数按来源 IP 分开（见 loginGuard）。
	ip := loginClientIP(r)
	// 账户级锁优先：跨 IP 连续失败 5 次后，同一管理员账户 10 分钟内一律拒绝——
	// 换 IP 也不能继续猜。（代价是知道用户名的人可以故意锁住管理员；这是
	// "5 次即锁"的固有取舍，见 deploy/README 的信任模型/安全说明。）
	if left, locked := s.auth.login.accountLockedFor(); locked {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(left.Seconds())+1))
		writeErr(w, 429, fmt.Sprintf("登录失败次数过多，账户已锁定，请 %s 后重试", left.Round(time.Second)))
		return
	}
	if left, locked := s.auth.login.lockedFor(ip); locked {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(left.Seconds())+1))
		writeErr(w, 429, fmt.Sprintf("登录失败次数过多，请 %s 后重试", left.Round(time.Second)))
		return
	}
	// 全局预算：防"换着 IP 猜"。这不是长期锁，窗口滑过就恢复。
	if left, throttled := s.auth.login.globalThrottledFor(); throttled {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(left.Seconds())+1))
		writeErr(w, 429, "登录尝试过于频繁，请稍后重试")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeAuthBody(w, r, &req) {
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
	// 用户名与密码一起校验；即使用户名错也照常做一次 bcrypt，避免用响应时间
	// 探测"用户名是否存在/是否正确"。
	username := adminUsername(pg)
	userOK := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.Username)), []byte(username)) == 1
	passErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password))
	if !userOK || passErr != nil {
		if lockFor := s.auth.login.fail(ip); lockFor > 0 {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(lockFor.Seconds())+1))
			writeErr(w, 429, fmt.Sprintf("登录失败次数过多，已锁定 %s", lockFor.Round(time.Second)))
			return
		}
		writeErr(w, 401, "用户名或密码错误")
		return
	}
	s.auth.login.succeed(ip)
	tok, err := signJWTFor(s.auth.signKey.get(), username)
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}
