package traffic

import (
	"strings"
	"testing"
)

func TestValidateProxyURL(t *testing.T) {
	ok := []string{
		"http://127.0.0.1:8080",
		"https://proxy.example.com:3128",
		"socks5://10.0.0.1:1080",
		"socks5://user:pass@10.0.0.1:1080",
		"socks5h://127.0.0.1:7891", // Clash 常见写法：域名叫代理解析
	}
	for _, raw := range ok {
		u, err := ValidateProxyURL(raw)
		if err != nil {
			t.Errorf("ValidateProxyURL(%q) unexpected error: %v", raw, err)
			continue
		}
		if raw == "socks5h://127.0.0.1:7891" {
			if u.Scheme != "socks5h" {
				t.Errorf("ValidateProxyURL(%q) 应保留 socks5h 给外部工具，实际 %q", raw, u.Scheme)
			}
			tu, err := TransportProxyURL(raw)
			if err != nil || tu.Scheme != "socks5" {
				t.Errorf("TransportProxyURL(%q) = %v, %v; want socks5", raw, tu, err)
			}
		}
	}
	bad := []string{
		"127.0.0.1:8080",         // no scheme
		"ftp://host:21",          // unsupported scheme
		"http://",                // no host
		"socks4://10.0.0.1:1080", // unsupported scheme
	}
	for _, raw := range bad {
		if _, err := ValidateProxyURL(raw); err == nil {
			t.Errorf("ValidateProxyURL(%q) expected error, got nil", raw)
		}
	}
}

func TestSetUpstreamProxyStoreClear(t *testing.T) {
	tr := &Traffic{}
	if got := tr.upstream.Load(); got != nil {
		t.Fatalf("initial upstream = %v, want nil", got)
	}
	if err := tr.SetUpstreamProxy("socks5://user:pass@10.0.0.1:1080"); err != nil {
		t.Fatalf("SetUpstreamProxy: %v", err)
	}
	u := tr.upstream.Load()
	if u == nil || u.Scheme != "socks5" || u.Host != "10.0.0.1:1080" {
		t.Fatalf("stored upstream = %v, want socks5://10.0.0.1:1080", u)
	}
	if pw, _ := u.User.Password(); u.User.Username() != "user" || pw != "pass" {
		t.Fatalf("stored upstream lost credentials: %v", u)
	}
	// Empty clears back to direct.
	if err := tr.SetUpstreamProxy("  "); err != nil {
		t.Fatalf("SetUpstreamProxy(clear): %v", err)
	}
	if got := tr.upstream.Load(); got != nil {
		t.Fatalf("after clear upstream = %v, want nil", got)
	}
	// Invalid value is rejected and does not mutate current state.
	if err := tr.SetUpstreamProxy("nope://x"); err == nil {
		t.Fatal("SetUpstreamProxy(invalid) expected error")
	}
	if got := tr.upstream.Load(); got != nil {
		t.Fatalf("invalid set mutated upstream to %v, want nil", got)
	}
}

func TestProxyAddr(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		// Bare :port means "bind all interfaces" — the legacy default. The URL
		// agents consume must still point at loopback so they reach the local proxy.
		{":8788", "http://127.0.0.1:8788"},
		// Explicit loopback — the current default since #129 (open proxy exposure).
		{"127.0.0.1:8788", "http://127.0.0.1:8788"},
		// Explicit all-interface bind is still supported (remote capture via SSH).
		{"0.0.0.0:8788", "http://0.0.0.0:8788"},
	}
	for _, c := range cases {
		got := (&Traffic{addr: c.addr}).ProxyAddr()
		if got != c.want {
			t.Errorf("ProxyAddr(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}

func TestProxyRedactionAndUpstreamErrorHook(t *testing.T) {
	raw := "socks5h://alice:s3cr3t@10.0.0.1:1080"
	if got := RedactProxyURL(raw); got != "socks5h://10.0.0.1:1080" {
		t.Fatalf("RedactProxyURL = %q", got)
	}
	scrubbed := SanitizeProxyError(raw, contextError("proxyconnect tcp: dial tcp "+raw+": connection refused"))
	if strings.Contains(scrubbed, "s3cr3t") || strings.Contains(scrubbed, "alice") {
		t.Fatalf("SanitizeProxyError leaked credentials: %q", scrubbed)
	}

	tr := &Traffic{}
	if err := tr.SetUpstreamProxy(raw); err != nil {
		t.Fatalf("SetUpstreamProxy(socks5h): %v", err)
	}
	if got := tr.upstream.Load(); got == nil || got.Scheme != "socks5" {
		t.Fatalf("upstream should map socks5h to socks5 for go-mitmproxy, got %v", got)
	}

	var hookProxy string
	var hookErr error
	tr.SetUpstreamErrorHook(func(p string, err error) {
		hookProxy, hookErr = p, err
	})
	tr.notifyUpstreamError(contextError("proxyconnect tcp: dial tcp 10.0.0.1:1080: connection refused"))
	if hookProxy == "" || hookErr == nil {
		t.Fatalf("expected upstream error hook to fire, got proxy=%q err=%v", hookProxy, hookErr)
	}
}

type contextError string

func (e contextError) Error() string { return string(e) }
