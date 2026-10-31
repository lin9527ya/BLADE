package mcphttp

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// AllowLocalEnv 是「允许 MCP 连到本机/链路本地地址」的显式开关。
//
// 与通知渠道的 BLADE_NOTIFY_ALLOW_LOCAL 同一语义：默认拒绝，需要连本机 MCP
// （本地起的 http MCP server）时显式打开。
const AllowLocalEnv = "BLADE_MCP_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP 报告目标 IP 是否属于「默认不允许连接」的地址段。
//
// 与 notify/http.go 的同名判定保持一致：只拒绝环回、链路本地（含云元数据
// 169.254.169.254）、未指定与组播；**不拒绝** RFC1918 私网——内网自建的 MCP
// server 是常见部署，一刀切会让功能不可用。
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial 是 http.Transport 拨号器的 Control 钩子，在**连接建立时**检查
// 目标地址。
//
// 为什么放在拨号阶段而不是只在保存配置时校验：这里才是最终生效点，同时覆盖
// DNS 重绑定（校验时解析到公网、真正连接时解析到内网）。
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("无法解析目标地址 %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("拒绝连接本机/链路本地地址 %s（如确需连接本机 MCP，设置 %s=1）", ip, AllowLocalEnv)
	}
	return nil
}

func guardedDialer() *net.Dialer {
	return &net.Dialer{Timeout: 30 * time.Second, Control: blockInternalDial}
}

// guardedTransport 返回一个带拨号守卫的 Transport，保留默认的全部调优。
func guardedTransport(insecure bool) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = guardedDialer().DialContext
	if insecure {
		base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return base
}

// sseTransport 是 legacy SSE 用的 Transport：流式连接不能设整体超时，只约束握手。
func sseTransport(insecure bool) *http.Transport {
	tr := &http.Transport{ResponseHeaderTimeout: 30 * time.Second}
	tr.DialContext = guardedDialer().DialContext
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return tr
}

// checkEndpointURL 在**握手前**先做一次静态检查（scheme + 字面量 IP），
// 让明显不该连的地址在建立任何连接之前就被拒掉，错误信息也更可读。
// 真正的强制点在拨号钩子上（覆盖 DNS 解析结果）。
func checkEndpointURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("MCP 地址不可解析")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("MCP 地址只支持 http/https，得到 %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("MCP 地址缺少主机")
	}
	if allowLocalTargets() {
		return nil
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) {
		return fmt.Errorf("拒绝连接本机/链路本地地址 %s（如确需连接本机 MCP，设置 %s=1）", ip, AllowLocalEnv)
	}
	return nil
}

// endpointPolicy 是 legacy SSE 的**会话级**出站策略：服务器在流里给出的
// message endpoint 也必须遵守同一套目标约束。
//
// 为什么需要：SSE 的 POST 地址是**服务器下发**的（data: <url>），与用户配置的
// sseURL 可能不同主机。不校验的话，一个被攻陷或恶意指向的 MCP server 只要回
// `data: http://169.254.169.254/...`，BLADE 就会带着配置的 Authorization 头去
// 请求那个地址——既是 SSRF，也是凭据外泄。
type endpointPolicy struct {
	base *url.URL // 用户配置的 sseURL
}

func newEndpointPolicy(raw string) (*endpointPolicy, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("MCP SSE 地址不可解析")
	}
	return &endpointPolicy{base: u}, nil
}

// check 校验一个（可能是相对的）endpoint，返回绝对 URL。
func (p *endpointPolicy) check(raw string) (string, error) {
	if p == nil {
		return raw, nil
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("MCP SSE endpoint 不可解析")
	}
	if !u.IsAbs() {
		u = p.base.ResolveReference(u)
	}
	abs := u.String()
	if err := checkEndpointURL(abs); err != nil {
		return "", err
	}
	// 跨主机跳转必须拒绝：配置的 Authorization 头会跟着过去。
	// 同主机的路径变化（补斜杠、带 session id 的 /message?sessionId=…）允许。
	if !sameOrigin(u, p.base) {
		return "", fmt.Errorf("MCP SSE endpoint 指向了不同主机（%s → %s），拒绝携带凭据跳转", p.base.Host, u.Host)
	}
	return abs, nil
}

func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
