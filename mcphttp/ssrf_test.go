package mcphttp

import (
	"net"
	"os"
	"strings"
	"testing"
)

// withLocalBlocked 在「守卫生效」的前提下跑一段断言。
//
// 本包的 TestMain 把 BLADE_MCP_ALLOW_LOCAL 打开了（用例都连 127.0.0.1 的假
// server），所以验证"默认拒绝"必须显式关掉它——否则这里测的就不是默认行为了。
func withLocalBlocked(t *testing.T, fn func()) {
	t.Helper()
	prev, had := os.LookupEnv(AllowLocalEnv)
	os.Unsetenv(AllowLocalEnv)
	defer func() {
		if had {
			os.Setenv(AllowLocalEnv, prev)
		}
	}()
	fn()
}

// TestBlockedDialIPRanges 钉住守卫的地址段口径：环回、链路本地（含云元数据）、
// 未指定与组播一律拒绝；RFC1918 私网放行（内网自建 MCP 是常见部署）。
func TestBlockedDialIPRanges(t *testing.T) {
	blocked := []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "0.0.0.0", "224.0.0.1"}
	for _, s := range blocked {
		if !isBlockedDialIP(net.ParseIP(s)) {
			t.Fatalf("%s 应被拒绝", s)
		}
	}
	allowed := []string{"10.1.2.3", "192.168.1.10", "172.16.0.5", "8.8.8.8", "2001:4860:4860::8888"}
	for _, s := range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Fatalf("%s 不该被拒绝（私网 MCP 是常见部署）", s)
		}
	}
	// IPv4-mapped IPv6 必须还原后再判，否则 ::ffff:127.0.0.1 能绕过。
	if !isBlockedDialIP(net.ParseIP("::ffff:127.0.0.1")) {
		t.Fatal("IPv4-mapped IPv6 环回地址绕过了守卫")
	}
}

// TestCheckEndpointURL 钉住握手前的静态检查：非 http(s) scheme、缺主机、
// 字面量本机/链路本地地址都在建立连接之前被拒。
func TestCheckEndpointURL(t *testing.T) {
	withLocalBlocked(t, func() {
		bad := []string{
			"file:///etc/passwd",
			"ftp://example.com/x",
			"http://",
			"http://127.0.0.1:8080/mcp",
			"http://169.254.169.254/latest/meta-data/",
			"http://[::1]:8080/mcp",
		}
		for _, u := range bad {
			if err := checkEndpointURL(u); err == nil {
				t.Fatalf("%s 应被拒绝", u)
			}
		}
		if err := checkEndpointURL("https://mcp.example.com/mcp"); err != nil {
			t.Fatalf("公网地址被误拒：%v", err)
		}
		if err := checkEndpointURL("http://10.0.0.5:8080/mcp"); err != nil {
			t.Fatalf("私网地址被误拒：%v", err)
		}
	})
}

// TestEndpointPolicyRejectsCrossHost 钉住 SSE 的核心风险：服务器下发的 message
// endpoint 与配置的 sseURL **不同主机**时必须拒绝——配置的 Authorization 头会
// 跟着那个 POST 过去，等于把凭据交给对方。
func TestEndpointPolicyRejectsCrossHost(t *testing.T) {
	withLocalBlocked(t, func() {
		p, err := newEndpointPolicy("https://mcp.example.com/sse")
		if err != nil {
			t.Fatal(err)
		}
		// 同主机不同路径（补斜杠、带 session id）必须放行。
		for _, ok := range []string{"/message?sessionId=abc", "https://mcp.example.com/message"} {
			if _, err := p.check(ok); err != nil {
				t.Fatalf("%s 应放行：%v", ok, err)
			}
		}
		// 跨主机、降级到 http、指向元数据地址，全部拒绝。
		bad := []string{
			"https://evil.example.net/message",
			"http://mcp.example.com/message", // scheme 降级也是跨源
			"http://169.254.169.254/latest/meta-data/",
		}
		for _, b := range bad {
			if _, err := p.check(b); err == nil {
				t.Fatalf("%s 应被拒绝", b)
			} else if !strings.Contains(err.Error(), "不同主机") && !strings.Contains(err.Error(), "拒绝连接") {
				t.Fatalf("%s 的拒绝理由不可读：%v", b, err)
			}
		}
	})
}

// TestNewRejectsInternalEndpoint 是端到端版本：New/NewSSE 本身必须拒绝本机地址，
// 而不是只有内部的 checkEndpointURL 会拒。这是"守卫接进了构造函数"的判据——
// 少了 client.go 里的那次调用，这条会红。
func TestNewRejectsInternalEndpoint(t *testing.T) {
	withLocalBlocked(t, func() {
		ctx := t.Context()
		if _, err := New(ctx, "probe", "http://127.0.0.1:9/mcp", nil, false); err == nil {
			t.Fatal("New 没有拒绝本机地址（守卫未接进构造函数）")
		} else if !strings.Contains(err.Error(), "拒绝连接") {
			t.Fatalf("拒绝理由不可读：%v", err)
		}
		if _, err := NewSSE(ctx, "probe", "http://169.254.169.254/sse", nil, false); err == nil {
			t.Fatal("NewSSE 没有拒绝链路本地地址")
		}
	})
}
