package agent

// 原生 HTTP 执行器（v0.7.0）：http_request / session_login。
//
// ## 为什么需要它
//
// 现场诊断（v0.6.32）显示：worker 的每一次探测都是「领域工具组命令 → 模型再调
// Bash 执行」两次往返；curl 2750 次、python3 胶水 2105 次，且响应全文直接进对话。
// 更关键的是没有会话概念：auth_request 只能组 curl，登录态无法被后续请求复用，
// 「登录 → 越权 → 回读」这条最基本的链要模型自己搬 cookie，搬错一次就判死。
//
// 本工具直接在 Go 里发请求（走同一条记录代理 + 同一份任务级 cookie jar），
// 一次调用拿结构化结果：status/headers/正文片段/落盘指针。
//
// ## 管控边界
//
//   - 出口与 Bash 完全一致：Worker.SetProxy 注入的扫描代理（记录 MITM 或全局代理），
//     连带信任其 CA——流量照常进流量查看器，不绕开留痕。
//   - 属于「能动目标」的工具，已登记进 localExecToolNames：local_exec=false 的
//     agent 拿不到它（与 Bash/http_probe 同一道闸）。
//   - cookie jar 落在 tasks/<id>/shared/sessions/<name>.jar（curl 兼容的 Netscape
//     格式），与 auth_request/http_request 的 `-b/-c` 共用同一份文件——原生请求
//     与 curl 命令可以交替使用同一个登录态。

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// SetEgressProxy 注入出站代理（与 Bash 子进程的 HTTP_PROXY 同源）。
// caCert 是记录代理的 CA 证书路径——加载后经 MITM 重签的 HTTPS 才能正常校验。
func (t *ToolSet) SetEgressProxy(addr, caCert string) {
	t.egressProxy, t.egressCA, t.egressProxyFn = strings.TrimSpace(addr), strings.TrimSpace(caCert), nil
}

// SetEgressProxyFunc 让内置 HTTP 工具在长 run 中每次发请求时现取出口代理与 CA，
// 代理池切换后无需等待本轮结束或重建 agent 即可热切。
func (t *ToolSet) SetEgressProxyFunc(fn func() (string, string)) {
	t.egressProxyFn = fn
}

// SetProxyFailureHook 注册出站代理传输报错时的宿主回调（触发被动复查）。
func (t *ToolSet) SetProxyFailureHook(fn func(proxyURL string, err error)) {
	t.proxyFailureFn = fn
}

// SetDirectTLSInterceptor 为无出口代理的直连场景挂载自定义 TLS 拨号器（用于 1.A 扩展拟真）。
func (t *ToolSet) SetDirectTLSInterceptor(fn DialTLSContextFunc) {
	t.directTLSFn = fn
}

func (t *ToolSet) currentEgressProxy() (string, string) {
	if t != nil && t.egressProxyFn != nil {
		addr, ca := t.egressProxyFn()
		return strings.TrimSpace(addr), strings.TrimSpace(ca)
	}
	if t == nil {
		return "", ""
	}
	return t.egressProxy, t.egressCA
}

// ── 任务级 cookie jar（Netscape 格式，与 curl 互通）─────────────────────────

type jarEntry struct {
	Domain     string
	IncludeSub bool
	Path       string
	Secure     bool
	Expires    int64
	Name       string
	Value      string
	HTTPOnly   bool
}

func (e jarEntry) key() string { return e.Domain + "\x00" + e.Path + "\x00" + e.Name }

// jarStore 是一个可持久化的 http.CookieJar。跨 goroutine 安全；同一进程内
// 不同意图共用同一个文件时以路径为键共用一把锁（避免读改写相互覆盖）。
type jarStore struct {
	path    string
	entries map[string]jarEntry
	loaded  bool
}

var (
	jarLocksMu sync.Mutex
	jarLocks   = map[string]*sync.Mutex{}
)

func jarLockFor(path string) *sync.Mutex {
	jarLocksMu.Lock()
	defer jarLocksMu.Unlock()
	l, ok := jarLocks[path]
	if !ok {
		l = &sync.Mutex{}
		jarLocks[path] = l
	}
	return l
}

func newJarStore(path string) *jarStore {
	return &jarStore{path: path, entries: map[string]jarEntry{}}
}

// loadLocked 读 Netscape cookie 文件；文件不存在视为空 jar（首次登录）。
func (j *jarStore) loadLocked() {
	if j.loaded {
		return
	}
	j.loaded = true
	b, err := os.ReadFile(j.path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		httpOnly := false
		if strings.HasPrefix(line, "#HttpOnly_") {
			httpOnly = true
			line = strings.TrimPrefix(line, "#HttpOnly_")
		} else if strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 7 {
			continue
		}
		var exp int64
		fmt.Sscanf(f[4], "%d", &exp)
		e := jarEntry{
			Domain: f[0], IncludeSub: strings.EqualFold(f[1], "TRUE"),
			Path: f[2], Secure: strings.EqualFold(f[3], "TRUE"),
			Expires: exp, Name: f[5], Value: f[6], HTTPOnly: httpOnly,
		}
		if e.Name == "" {
			continue
		}
		j.entries[e.key()] = e
	}
}

// saveLocked 原子写回 Netscape 格式（curl -b/-c 可直接读取）。
func (j *jarStore) saveLocked() error {
	if j.path == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("# Netscape HTTP Cookie File\n")
	b.WriteString("# Shared Netscape cookie jar; compatible with curl -b/-c.\n")
	keys := make([]string, 0, len(j.entries))
	for k := range j.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := j.entries[k]
		prefix := ""
		if e.HTTPOnly {
			prefix = "#HttpOnly_"
		}
		sub := "FALSE"
		if e.IncludeSub {
			sub = "TRUE"
		}
		sec := "FALSE"
		if e.Secure {
			sec = "TRUE"
		}
		fmt.Fprintf(&b, "%s%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			prefix, e.Domain, sub, e.Path, sec, e.Expires, e.Name, e.Value)
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return err
	}
	tmp := j.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, j.path)
}

func domainMatch(host, domain string, sub bool) bool {
	host = strings.ToLower(host)
	domain = strings.ToLower(strings.TrimPrefix(domain, "."))
	if host == domain {
		return true
	}
	return sub && strings.HasSuffix(host, "."+domain)
}

func pathMatch(reqPath, cookiePath string) bool {
	if cookiePath == "" {
		return true
	}
	if reqPath == cookiePath {
		return true
	}
	if strings.HasPrefix(reqPath, cookiePath) {
		if strings.HasSuffix(cookiePath, "/") {
			return true
		}
		return len(reqPath) > len(cookiePath) && reqPath[len(cookiePath)] == '/'
	}
	return false
}

func (j *jarStore) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if u == nil || len(cookies) == 0 {
		return
	}
	l := jarLockFor(j.path)
	l.Lock()
	defer l.Unlock()
	j.loadLocked()
	for _, c := range cookies {
		domain := c.Domain
		if domain == "" {
			domain = u.Hostname()
		}
		path := c.Path
		if path == "" {
			path = "/"
		}
		e := jarEntry{
			Domain: domain, IncludeSub: strings.HasPrefix(domain, ".") || c.Domain != "",
			Path: path, Secure: c.Secure, Expires: c.Expires.Unix(),
			Name: c.Name, Value: c.Value, HTTPOnly: c.HttpOnly,
		}
		if e.Expires <= 0 {
			e.Expires = time.Now().Add(12 * time.Hour).Unix()
		}
		if c.MaxAge < 0 {
			delete(j.entries, e.key())
			continue
		}
		if c.MaxAge > 0 {
			e.Expires = time.Now().Add(time.Duration(c.MaxAge) * time.Second).Unix()
		}
		j.entries[e.key()] = e
	}
	_ = j.saveLocked()
}

func (j *jarStore) Cookies(u *url.URL) []*http.Cookie {
	if u == nil {
		return nil
	}
	l := jarLockFor(j.path)
	l.Lock()
	defer l.Unlock()
	j.loadLocked()
	now := time.Now().Unix()
	host := u.Hostname()
	out := []*http.Cookie{}
	for k, e := range j.entries {
		if e.Expires > 0 && e.Expires < now {
			delete(j.entries, k)
			continue
		}
		if !domainMatch(host, e.Domain, e.IncludeSub) {
			continue
		}
		if !pathMatch(u.Path, e.Path) {
			continue
		}
		if e.Secure && u.Scheme != "https" {
			continue
		}
		out = append(out, &http.Cookie{Name: e.Name, Value: e.Value, Domain: e.Domain, Path: e.Path})
	}
	return out
}

// ── 请求执行 ────────────────────────────────────────────────────────────────

type httpArgs struct {
	URL               string `json:"url"`
	Method            string `json:"method"`
	Headers           string `json:"headers"`
	Body              string `json:"body"`
	BodyJSON          string `json:"body_json"`
	Session           string `json:"session"`
	Follow            *bool  `json:"follow_redirects"`
	Insecure          *bool  `json:"insecure"`
	TimeoutSec        int    `json:"timeout"`
	MaxBodyChar       int    `json:"max_body_chars"`
	BasicAuth         string `json:"basic_auth"`
	MobileSignMethod  string `json:"mobile_sign_method"`
	MobileSignSession string `json:"mobile_sign_session"`
}

type httpOutcome struct {
	Status      int
	StatusText  string
	URL         string
	FinalURL    string
	ContentType string
	Bytes       int64
	Millis      int64
	Headers     map[string]string
	BodySnippet string
	BodyTrunc   bool
	BodyFile    string
	SHA256      string
	SetCookies  []string
	Err         error
}

func (t *ToolSet) buildHTTPClient(jar http.CookieJar, follow, insecure bool, timeout time.Duration) (*http.Client, error) {
	c, _, err := t.buildHTTPClientWithProxy(jar, follow, insecure, timeout)
	return c, err
}

func (t *ToolSet) buildHTTPClientWithProxy(jar http.CookieJar, follow, insecure bool, timeout time.Duration) (*http.Client, string, error) {
	proxyAddr, caCert := t.currentEgressProxy()
	var dialTLS DialTLSContextFunc
	if proxyAddr == "" && t != nil && t.directTLSFn != nil {
		dialTLS = t.directTLSFn
	}
	key := proxyAddr + "|" + caCert + "|" + strconv.FormatBool(insecure) + "|" + strconv.FormatBool(dialTLS != nil)
	if t.httpPool == nil {
		t.httpPool = &httpTransportPool{transports: map[string]*http.Transport{}}
	}
	pool := t.httpPool
	pool.mu.Lock()
	tr := pool.transports[key]
	pool.mu.Unlock()
	if tr == nil {
		tr = newHTTPTransportFor(proxyAddr, caCert, insecure, dialTLS)
		if tr == nil {
			return nil, proxyAddr, fmt.Errorf("出口代理解析失败")
		}
		pool.mu.Lock()
		if existing := pool.transports[key]; existing != nil {
			tr.CloseIdleConnections()
			tr = existing
		} else {
			if len(pool.order) >= 8 {
				evict := pool.order[0]
				pool.order = pool.order[1:]
				if old := pool.transports[evict]; old != nil {
					old.CloseIdleConnections()
					delete(pool.transports, evict)
				}
			}
			pool.transports[key] = tr
			pool.order = append(pool.order, key)
		}
		pool.mu.Unlock()
	}
	c := &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		CheckRedirect: t.buildRedirectCheck(context.Background(), follow, nil, nil),
	}
	if jar != nil {
		c.Jar = jar
	}
	return c, proxyAddr, nil
}

var (
	_, cgnatNet, _ = net.ParseCIDR("100.64.0.0/10")
	awsIPv6MetaIP  = net.ParseIP("fd00:ec2::254")
)

func effectivePort(u *url.URL) string {
	if u == nil {
		return ""
	}
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return ""
}

func sameRedirectOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func isMetadataOrLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	switch h {
	case "localhost", "localhost.localdomain", "metadata", "metadata.google.internal", "instance-data":
		return true
	}
	return strings.HasSuffix(h, ".localhost")
}

func isAlwaysBlockedRedirectIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		(awsIPv6MetaIP != nil && ip.Equal(awsIPv6MetaIP))
}

func isInternalRedirectIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsPrivate() || (cgnatNet != nil && cgnatNet.Contains(ip))
}

func looksSensitiveAuthHeader(name string) bool {
	low := strings.ToLower(strings.TrimSpace(name))
	if low == "" {
		return false
	}
	switch low {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key":
		return true
	}
	for _, frag := range []string{"auth", "token", "key", "secret", "cookie", "session", "bearer", "pass", "cred", "jwt"} {
		if strings.Contains(low, frag) {
			return true
		}
	}
	return false
}

func stripSensitiveHeadersOnRedirect(req *http.Request, customHeaders map[string]bool) {
	if req == nil {
		return
	}
	// 清空 Host 覆写，避免初始请求手工指定的 Host 粘滞到域外重定向目标。
	req.Host = ""
	for _, k := range []string{
		"Authorization", "Proxy-Authorization", "Cookie",
		"X-API-Key", "X-Auth-Token", "X-Access-Token", "X-Session-Token",
		"X-CSRF-Token", "X-XSRF-Token", "X-Token", "Api-Key", "X-Api-Token",
	} {
		req.Header.Del(k)
	}
	for k := range req.Header {
		if customHeaders[strings.ToLower(k)] || looksSensitiveAuthHeader(k) {
			req.Header.Del(k)
		}
	}
}

func (t *ToolSet) isInternalRedirectScoped(host string, ips []net.IP) bool {
	if t.as == nil || t.taskID <= 0 {
		return false
	}
	scopes, err := t.as.ListTaskScope(t.taskID)
	if err != nil {
		return false
	}
	h := strings.ToLower(strings.TrimSpace(host))
	for _, sc := range scopes {
		dom := strings.ToLower(strings.TrimSpace(sc.Domain))
		if dom != "" && (h == dom || strings.HasSuffix(h, "."+dom)) {
			return true
		}
		val := strings.ToLower(strings.TrimSpace(sc.Value))
		if val != "" && (h == val || strings.HasSuffix(h, "."+val)) {
			return true
		}
		netStr := strings.TrimSpace(sc.Net)
		if netStr == "" && (sc.Kind == "ip" || sc.Kind == "cidr") {
			netStr = strings.TrimSpace(sc.Value)
		}
		if netStr != "" {
			if _, cidr, err := net.ParseCIDR(netStr); err == nil {
				for _, ip := range ips {
					if cidr.Contains(ip) {
						return true
					}
				}
			} else if sip := net.ParseIP(strings.Split(netStr, "/")[0]); sip != nil {
				for _, ip := range ips {
					if sip.Equal(ip) {
						return true
					}
				}
			}
		}
	}
	return false
}

func (t *ToolSet) validateRedirectTarget(ctx context.Context, initURL, target *url.URL) error {
	if target == nil {
		return fmt.Errorf("重定向目标为空")
	}
	scheme := strings.ToLower(strings.TrimSpace(target.Scheme))
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("重定向协议 %q 被拦截（仅允许 http/https）", target.Scheme)
	}
	host := strings.TrimSpace(target.Hostname())
	if host == "" {
		return fmt.Errorf("重定向缺少主机名")
	}
	if initURL == nil || !sameRedirectOrigin(initURL, target) {
		if isMetadataOrLoopbackHost(host) {
			return fmt.Errorf("重定向到本机/元数据主机 %q 被拦截", host)
		}
	}
	var resolved []net.IP
	if ip := net.ParseIP(host); ip != nil {
		resolved = []net.IP{ip}
	} else {
		lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		addrs, err := net.DefaultResolver.LookupIPAddr(lctx, host)
		cancel()
		if err == nil {
			for _, a := range addrs {
				resolved = append(resolved, a.IP)
			}
		}
	}
	// 黑名单规则校验（全局 asset_intercept_rules + 任务级 task_intercept_rules，allow 优先覆盖）
	if t.as != nil {
		ipStrs := make([]string, 0, len(resolved))
		for _, ip := range resolved {
			ipStrs = append(ipStrs, ip.String())
		}
		if reason, blocked, err := t.as.EvaluateInterceptGate(t.taskID, host, ipStrs, []string{target.String()}); err == nil && blocked {
			return fmt.Errorf("重定向命中拦截规则 %s", reason)
		}
	}
	// 同源重定向允许测试靶机自身路径跳转；跨源跳转阻断回环、元数据及未授权私网 IP。
	if initURL == nil || !sameRedirectOrigin(initURL, target) {
		for _, ip := range resolved {
			if isAlwaysBlockedRedirectIP(ip) {
				return fmt.Errorf("重定向目标 %q 解析到受限地址 %s（回环/链路本地/元数据）", target.String(), ip)
			}
			if isInternalRedirectIP(ip) && !t.isInternalRedirectScoped(host, resolved) {
				return fmt.Errorf("重定向目标 %q 解析到未在任务范围内的内网地址 %s", target.String(), ip)
			}
		}
	}
	return nil
}

func (t *ToolSet) buildRedirectCheck(ctx context.Context, follow bool, initURL *url.URL, customHeaders map[string]bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if !follow {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return fmt.Errorf("重定向超过最大跳数上限")
		}
		base := initURL
		if base == nil && len(via) > 0 {
			base = via[0].URL
		}
		if err := t.validateRedirectTarget(ctx, base, req.URL); err != nil {
			return err
		}
		prev := base
		if len(via) > 0 {
			prev = via[len(via)-1].URL
		}
		if !sameRedirectOrigin(base, req.URL) || !sameRedirectOrigin(prev, req.URL) {
			stripSensitiveHeadersOnRedirect(req, customHeaders)
		}
		return nil
	}
}

func newHTTPTransportFor(proxyAddr, caCert string, insecure bool, dialTLS DialTLSContextFunc) *http.Transport {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          32,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if dialTLS != nil && proxyAddr == "" {
		tr.DialTLSContext = dialTLS
	}
	if proxyAddr != "" {
		pu := proxyAddr
		if !strings.Contains(pu, "://") {
			pu = "http://" + pu
		}
		u, err := url.Parse(pu)
		if err != nil {
			return nil
		}
		if strings.EqualFold(u.Scheme, "socks5h") {
			u.Scheme = "socks5"
		}
		tr.Proxy = http.ProxyURL(u)
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure}
	if caCert != "" {
		if pem, err := os.ReadFile(caCert); err == nil {
			pool, _ := x509.SystemCertPool()
			if pool == nil {
				pool = x509.NewCertPool()
			}
			if pool.AppendCertsFromPEM(pem) {
				tlsCfg.RootCAs = pool
			}
		}
	}
	tr.TLSClientConfig = tlsCfg
	return tr
}

func parseHeaderLines(s string) [][2]string {
	var out [][2]string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out = append(out, [2]string{k, strings.TrimSpace(v)})
	}
	return out
}

func looksTextual(ct string, head []byte) bool {
	ct = strings.ToLower(ct)
	for _, s := range []string{"text/", "json", "xml", "javascript", "x-www-form-urlencoded", "graphql", "yaml", "csv"} {
		if strings.Contains(ct, s) {
			return true
		}
	}
	if ct != "" {
		return false
	}
	for _, b := range head {
		if b == 0 {
			return false
		}
	}
	return true
}

// doHTTP 执行一次请求并把响应体落到工作目录（超长时返回文件指针）。
func (t *ToolSet) doHTTP(ctx context.Context, a httpArgs, jar http.CookieJar) httpOutcome {
	out := httpOutcome{}
	u, err := url.Parse(strings.TrimSpace(a.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		out.Err = fmt.Errorf("url 必须是绝对 http(s) 地址")
		return out
	}
	method := strings.ToUpper(strings.TrimSpace(a.Method))
	if method == "" {
		method = "GET"
	}
	body := a.Body
	if strings.TrimSpace(a.BodyJSON) != "" {
		body = a.BodyJSON
	}
	// Optional Frida RPC request auto-signing middleware when testing mobile APIs with signatures.
	if signMethod := strings.TrimSpace(a.MobileSignMethod); signMethod != "" {
		if Mobile == nil {
			out.Err = fmt.Errorf("mobile_sign_method=%q 需要已连接移动测试机与活跃 Frida 会话", signMethod)
			return out
		}
		tid := RunInfoFrom(ctx).TaskID
		if tid <= 0 {
			tid = t.taskID
		}
		hdrMap := map[string]string{}
		for _, kv := range parseHeaderLines(a.Headers) {
			hdrMap[kv[0]] = kv[1]
		}
		reqSpec := map[string]any{
			"method":  method,
			"url":     u.String(),
			"headers": hdrMap,
			"body":    body,
		}
		rawRes, signErr := Mobile.RPCSign(ctx, tid, strings.TrimSpace(a.MobileSignSession), signMethod, []any{reqSpec})
		if signErr != nil {
			out.Err = fmt.Errorf("Frida RPC 签名 (%s) 失败: %w", signMethod, signErr)
			return out
		}
		if err := applyMobileRPCSignResult(rawRes, &u, &a.Headers, &body); err != nil {
			out.Err = fmt.Errorf("解析 Frida RPC 签名输出失败: %w", err)
			return out
		}
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	timeout := a.TimeoutSec
	if timeout <= 0 {
		timeout = 20
	}
	if timeout > 120 {
		timeout = 120
	}
	follow := true
	if a.Follow != nil {
		follow = *a.Follow
	}
	insecure := true // 授权测试默认对齐 curl -k（目标自签证书是常态）
	if a.Insecure != nil {
		insecure = *a.Insecure
	}
	client, usedProxy, err := t.buildHTTPClientWithProxy(jar, follow, insecure, time.Duration(timeout)*time.Second)
	if err != nil {
		out.Err = err
		return out
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		out.Err = err
		return out
	}
	parsedHdrs := parseHeaderLines(a.Headers)
	customHeaders := map[string]bool{}
	for _, kv := range parsedHdrs {
		if strings.EqualFold(kv[0], "Host") {
			req.Host = kv[1]
			continue
		}
		customHeaders[strings.ToLower(kv[0])] = true
	}
	lowPath := strings.ToLower(u.Path)
	apiStyle := a.BodyJSON != "" ||
		strings.EqualFold(method, http.MethodPost) ||
		strings.EqualFold(method, http.MethodPut) ||
		strings.EqualFold(method, http.MethodPatch) ||
		strings.Contains(lowPath, "/api/") ||
		strings.Contains(lowPath, "graphql")
	ApplyRequestProfile(req, OutboundProfileForTask(t.taskID), apiStyle, customHeaders)
	if body != "" && a.BodyJSON != "" && !customHeaders["content-type"] {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, kv := range parsedHdrs {
		if strings.EqualFold(kv[0], "Host") {
			continue
		}
		req.Header.Set(kv[0], kv[1])
	}
	client.CheckRedirect = t.buildRedirectCheck(ctx, follow, u, customHeaders)
	if a.BasicAuth != "" {
		user, pass, _ := strings.Cut(a.BasicAuth, ":")
		req.SetBasicAuth(user, pass)
	}
	start := time.Now()
	resp, err := client.Do(req)
	out.Millis = time.Since(start).Milliseconds()
	if err != nil {
		if usedProxy != "" && t.proxyFailureFn != nil {
			t.proxyFailureFn(usedProxy, err)
		}
		out.Err = err
		return out
	}
	defer resp.Body.Close()
	out.Status = resp.StatusCode
	out.StatusText = strings.TrimSpace(strings.TrimPrefix(resp.Status, fmt.Sprint(resp.StatusCode)))
	out.URL = u.String()
	out.FinalURL = resp.Request.URL.String()
	out.ContentType = resp.Header.Get("Content-Type")
	out.Headers = map[string]string{}
	for k, vals := range resp.Header {
		out.Headers[k] = strings.Join(vals, ", ")
	}
	for _, c := range resp.Cookies() {
		out.SetCookies = append(out.SetCookies, c.Name)
	}
	// 响应体：边读边落盘，避免大文件占内存；读完再决定是否内联片段。
	dir := filepath.Join(t.runDir, "http")
	_ = os.MkdirAll(dir, 0o755)
	f, ferr := os.CreateTemp(dir, "resp-*.bin")
	var filePath string
	var h = sha256.New()
	var n int64
	var memBody []byte
	bodyRdr, closeDecomp := wrapDecompressedBody(resp)
	defer closeDecomp()
	if ferr == nil {
		filePath = f.Name()
		n, _ = io.Copy(io.MultiWriter(f, h), io.LimitReader(bodyRdr, 64<<20))
		_ = f.Close()
	} else {
		memBody, _ = io.ReadAll(io.LimitReader(bodyRdr, 64<<20))
		n = int64(len(memBody))
		h.Write(memBody)
		filePath = ""
	}
	out.Bytes = n
	out.SHA256 = hex.EncodeToString(h.Sum(nil))
	maxChar := a.MaxBodyChar
	if maxChar <= 0 {
		maxChar = 6000
	}
	if maxChar > 32000 {
		maxChar = 32000
	}
	var head []byte
	if filePath != "" {
		if fh, err := os.Open(filePath); err == nil {
			buf := make([]byte, maxChar+1)
			m, _ := io.ReadFull(fh, buf)
			_ = fh.Close()
			head = buf[:m]
		}
	} else {
		head = memBody
		if len(head) > maxChar+1 {
			head = head[:maxChar+1]
		}
	}
	if len(head) > maxChar {
		head = head[:maxChar]
		out.BodyTrunc = true
	}
	if looksTextual(out.ContentType, head) {
		out.BodySnippet = string(head)
	} else {
		out.BodyTrunc = true
		out.BodySnippet = fmt.Sprintf("[非文本响应 %s，%d 字节；完整内容见 body_file]", out.ContentType, n)
	}
	// 小于 64KB 且无截断的响应不保留文件，减少垃圾。
	if filePath != "" && (n > 64<<10 || out.BodyTrunc) {
		out.BodyFile = filePath
	} else if filePath != "" {
		_ = os.Remove(filePath)
	}
	return out
}

func (t *ToolSet) httpRequest() actool.CoreTool {
	return readTool(
		"http_request",
		"直接发一条 HTTP 请求并返回结构化结果（status/headers/正文片段/耗时/落盘指针）。"+
			"这是**执行**工具，不是组命令：一次调用就拿到响应，不需要再调 Bash。走与 Bash 相同的记录代理，"+
			"流量照常进流量查看器。\n"+
			"会话：传 session=名字可复用任务级 cookie jar——先用 session_login 或 auth_request(session=…) 登录，"+
			"后续 http_request(session=同名) 自动带登录态（多身份越权对照就用不同 session）。\n"+
			"大响应：正文超过 max_body_chars 时只回片段，完整内容落盘并在 body_file 给出路径（可用 Bash grep/读取）。\n"+
			"适合：接口探测、参数 fuzz、回显验证、改价/越权回读；批量探活仍用 http_probe。",
		obj(map[string]any{
			"url":                 str("绝对 URL（http/https）"),
			"method":              str("HTTP 方法，默认 GET"),
			"headers":             str("请求头，每行一条 'Key: Value'（可选）"),
			"body":                str("请求体原文（可选）"),
			"body_json":           str("请求体 JSON（可选，设置后自动带 Content-Type: application/json；与 body 二选一）"),
			"session":             str("会话名：复用任务级 cookie jar（可选）"),
			"follow_redirects":    map[string]any{"type": "boolean", "description": "是否跟随重定向，默认 true"},
			"insecure":            map[string]any{"type": "boolean", "description": "跳过 TLS 校验，默认 true（对齐 curl -k）"},
			"timeout":             intp("超时秒数，默认 20，最大 120"),
			"max_body_chars":      intp("正文内联片段上限（字符），默认 6000，最大 32000"),
			"basic_auth":          str("Basic 认证 user:pass（可选）"),
			"mobile_sign_method":  str("可选：发包前调用活跃 Frida 会话 rpc.exports.<method> 对 {method,url,headers,body} 自动加签/加密"),
			"mobile_sign_session": str("可选：指定 Frida 会话 ID（省略取任务最近会话）"),
		}, "url"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a httpArgs
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			if strings.TrimSpace(a.URL) == "" {
				return actool.Errorf("url 必填"), nil
			}
			var jar http.CookieJar
			if strings.TrimSpace(a.Session) != "" {
				p := t.sessionJarPath(a.Session)
				if p == "" {
					return actool.Errorf("session 名非法或任务工作目录不可用"), nil
				}
				jar = newJarStore(p)
			}
			out := t.doHTTP(ctx, a, jar)
			if out.Err != nil {
				return actool.Errorf(fmt.Sprintf("请求失败：%v（若为连接错误，先检查出口代理/网络状态（必要时用 rotate_egress），不要直接判目标封板）", out.Err)), nil
			}
			res := map[string]any{
				"status":         out.Status,
				"status_text":    out.StatusText,
				"url":            out.URL,
				"final_url":      out.FinalURL,
				"content_type":   out.ContentType,
				"bytes":          out.Bytes,
				"time_ms":        out.Millis,
				"headers":        out.Headers,
				"body":           out.BodySnippet,
				"body_truncated": out.BodyTrunc,
				"body_sha256":    out.SHA256,
			}
			if out.BodyFile != "" {
				res["body_file"] = out.BodyFile
			}
			if len(out.SetCookies) > 0 {
				res["set_cookie_names"] = out.SetCookies
			}
			if a.Session != "" {
				res["session"] = a.Session
			}
			return jsonResult(res)
		},
	)
}

// sessionLogin 一次完成登录并把会话存进任务级 cookie jar。
//
// 凭据来源优先级：asset_id / credential_id（后台读取，模型不接触明文）> 显式 username/password/token。
// 登录成功的判定交给调用方看 status/是否拿到 cookie——本工具不猜“成功/失败”。
func (t *ToolSet) sessionLogin() actool.CoreTool {
	return readTool(
		"session_login",
		"登录接口并把会话写进任务级 cookie jar（后续 http_request 用同名 session 复用）。\n"+
			"凭据优先从资产 auth（asset_id + auth_index）或凭据节点（credential_id，report_credential 返回）读取，"+
			"避免明文在对话里搬运；也可显式给 username/password 或 token。\n"+
			"支持 auth_mode：form（默认，application/x-www-form-urlencoded）、json、basic、bearer、cookie。\n"+
			"返回 status、是否拿到 Set-Cookie（cookie_names）与正文片段——2xx 且 cookie_names 非空基本可判定登录成功；"+
			"若返回登录页/401，换 auth_mode 或换资产上的另一条凭据（auth_index）。",
		obj(map[string]any{
			"session":       str("会话名（字母数字-_），后续请求用它复用登录态"),
			"url":           str("登录接口绝对 URL"),
			"method":        str("HTTP 方法，默认 POST"),
			"asset_id":      intp("凭据登记在哪个资产上（可选；与 credential_id 二选一）"),
			"credential_id": intp("本任务凭据节点 id（report_credential 返回；后台取密文注入，不明文回传模型）"),
			"auth_index":    intp("用该资产上的第几条凭据（从 0 开始）"),
			"username":      str("用户名（可选，asset_id/credential_id 为空时用）"),
			"password":      str("密码（可选）"),
			"token":         str("token/会话值（可选）"),
			"auth_mode":     str("form|json|basic|bearer|cookie，默认 form"),
			"user_field":    str("表单用户名字段名，默认 username"),
			"pass_field":    str("表单密码字段名，默认 password"),
			"extra_fields":  str("额外字段，'k=v&k2=v2' 形式（可选）"),
			"header":        str("附加请求头，每行 'Key: Value'（可选，如 X-Requested-With: XMLHttpRequest）"),
		}, "session", "url"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Session      string `json:"session"`
				URL          string `json:"url"`
				Method       string `json:"method"`
				AssetID      int64  `json:"asset_id"`
				CredentialID int64  `json:"credential_id"`
				AuthIndex    int    `json:"auth_index"`
				Username     string `json:"username"`
				Password     string `json:"password"`
				Token        string `json:"token"`
				AuthMode     string `json:"auth_mode"`
				UserField    string `json:"user_field"`
				PassField    string `json:"pass_field"`
				ExtraFields  string `json:"extra_fields"`
				Header       string `json:"header"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Session) == "" || strings.TrimSpace(a.URL) == "" {
				return actool.Errorf("session 与 url 必填"), nil
			}
			jarPath := t.sessionJarPath(a.Session)
			if jarPath == "" {
				return actool.Errorf("session 名非法或任务工作目录不可用"), nil
			}
			user, pass, token := strings.TrimSpace(a.Username), a.Password, strings.TrimSpace(a.Token)
			authKindLabel := "explicit"
			if a.AssetID > 0 && a.CredentialID > 0 {
				return actool.Errorf("asset_id 与 credential_id 请二选一"), nil
			}
			if a.AssetID > 0 {
				if t.as == nil {
					return actool.Errorf("asset_id 需要 AssetStore（当前环境未启用）"), nil
				}
				assets, err := t.as.GetByIDsInScope(t.taskID, []int64{a.AssetID})
				if err != nil {
					return actool.Errorf("读取资产失败: " + err.Error()), nil
				}
				if len(assets) == 0 {
					return actool.Errorf(fmt.Sprintf("资产 %d 不在本任务范围内（或不存在）", a.AssetID)), nil
				}
				entries := parseAuthEntries(assets[0].Auth)
				if len(entries) == 0 {
					return actool.Errorf(fmt.Sprintf("资产 %d 上没有登记凭据", a.AssetID)), nil
				}
				idx := a.AuthIndex
				if idx < 0 {
					idx = 0
				}
				if idx >= len(entries) {
					return actool.Errorf(fmt.Sprintf("资产 %d 只有 %d 条凭据，auth_index 越界", a.AssetID, len(entries))), nil
				}
				e := entries[idx]
				user, pass, token = e.Username, e.Password, e.Token
				authKindLabel = fmt.Sprintf("资产#%d 第 %d 条(%s)", a.AssetID, idx, authKind(e))
			} else if a.CredentialID > 0 {
				if t.ts == nil {
					return actool.Errorf("credential_id 需要 ExplorationStore（当前环境未启用）"), nil
				}
				cred, err := t.ts.GetNode(a.CredentialID)
				if err != nil || cred == nil || cred.Kind != db.KindCredential {
					return actool.Errorf(fmt.Sprintf("credential_id %d 不是本任务的凭据节点", a.CredentialID)), nil
				}
				if cred.State != "" && cred.State != db.StateLootActive {
					return actool.Errorf(fmt.Sprintf("凭据 %d 当前状态是 %q（已失效），不能用于登录", a.CredentialID, cred.State)), nil
				}
				cv := db.ParseCredential(cred)
				if strings.TrimSpace(cv.Secret) == "" {
					return actool.Errorf(fmt.Sprintf("凭据 %d 没有可用的口令/令牌内容", a.CredentialID)), nil
				}
				ck := strings.ToLower(strings.TrimSpace(cv.Kind))
				switch {
				case strings.Contains(ck, "cookie") || strings.Contains(ck, "session"):
					user, token = strings.TrimSpace(cv.Username), strings.TrimSpace(cv.Secret)
					if strings.TrimSpace(a.AuthMode) == "" {
						a.AuthMode = "cookie"
					}
				case strings.Contains(ck, "token") || strings.Contains(ck, "bearer") || strings.Contains(ck, "jwt") || strings.Contains(ck, "api"):
					user, token = strings.TrimSpace(cv.Username), strings.TrimSpace(cv.Secret)
					if strings.TrimSpace(a.AuthMode) == "" {
						a.AuthMode = "bearer"
					}
				default:
					user, pass = strings.TrimSpace(cv.Username), cv.Secret
					if ck == "" {
						ck = "password"
					}
				}
				authKindLabel = fmt.Sprintf("凭据#%d(%s)", a.CredentialID, ck)
			}
			mode := strings.ToLower(strings.TrimSpace(a.AuthMode))
			if mode == "" {
				mode = "form"
			}
			uf, pf := strings.TrimSpace(a.UserField), strings.TrimSpace(a.PassField)
			if uf == "" {
				uf = "username"
			}
			if pf == "" {
				pf = "password"
			}
			args := httpArgs{URL: a.URL, Method: a.Method, Headers: a.Header, Session: a.Session, TimeoutSec: 20}
			switch mode {
			case "json":
				obj := map[string]string{}
				if user != "" {
					obj[uf] = user
				}
				if pass != "" {
					obj[pf] = pass
				}
				if token != "" {
					obj["token"] = token
				}
				for _, kv := range strings.Split(a.ExtraFields, "&") {
					k, v, ok := strings.Cut(kv, "=")
					if ok && strings.TrimSpace(k) != "" {
						obj[strings.TrimSpace(k)] = v
					}
				}
				b, _ := json.Marshal(obj)
				args.BodyJSON = string(b)
			case "basic":
				if user == "" && token == "" {
					return actool.Errorf("basic 模式需要 username/password 或 token"), nil
				}
				if token != "" && user == "" {
					args.BasicAuth = token
				} else {
					args.BasicAuth = user + ":" + pass
				}
			case "bearer":
				if token == "" {
					return actool.Errorf("bearer 模式需要 token"), nil
				}
				args.Headers = strings.TrimSpace(a.Header + "\nAuthorization: Bearer " + token)
			case "cookie":
				if token == "" && user == "" {
					return actool.Errorf("cookie 模式需要 token 或 'name=value'"), nil
				}
				val := token
				if val == "" {
					val = user
				}
				if !strings.Contains(val, "=") {
					if user != "" {
						val = user + "=" + val
					} else {
						val = "session=" + val
					}
				}
				args.Headers = strings.TrimSpace(a.Header + "\nCookie: " + val)
			default: // form
				vals := url.Values{}
				if user != "" {
					vals.Set(uf, user)
				}
				if pass != "" {
					vals.Set(pf, pass)
				}
				if token != "" {
					vals.Set("token", token)
				}
				for _, kv := range strings.Split(a.ExtraFields, "&") {
					k, v, ok := strings.Cut(kv, "=")
					if ok && strings.TrimSpace(k) != "" {
						vals.Set(strings.TrimSpace(k), v)
					}
				}
				if args.Headers == "" {
					args.Headers = "Content-Type: application/x-www-form-urlencoded"
				} else {
					args.Headers += "\nContent-Type: application/x-www-form-urlencoded"
				}
				args.Body = vals.Encode()
			}
			if strings.TrimSpace(args.Method) == "" {
				args.Method = "POST"
			}
			out := t.doHTTP(ctx, args, newJarStore(jarPath))
			if out.Err != nil {
				return actool.Errorf(fmt.Sprintf("登录请求失败：%v（连接类错误先检查出口代理/网络状态，不要判目标封板）", out.Err)), nil
			}
			res := map[string]any{
				"session":      a.Session,
				"jar":          jarPath,
				"status":       out.Status,
				"cookie_names": out.SetCookies,
				"body":         out.BodySnippet,
				"body_sha256":  out.SHA256,
				"auth_source":  authKindLabel,
				"auth_mode":    mode,
			}
			if len(out.SetCookies) == 0 {
				res["note"] = "响应没有 Set-Cookie：可能是 token 走响应体的系统（从 body 里取 token，再 http_request 用 Authorization 头），也可能登录失败（看 status/body）。"
			} else {
				res["note"] = "会话已写入任务级 cookie jar；后续 http_request(session=同名) 自动带登录态。"
			}
			return jsonResult(res)
		},
	)
}
