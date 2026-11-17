package db

import "testing"

func TestSanitizeHostname(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"www.calida.com，本地用了全局代理，你要学会通过doh等方式获取目标的真实ip", "www.calida.com"},
		{"www.mykastar.com，对该网站进行渗透测试，获取shell", "www.mykastar.com"},
		{"cosmossport.gr这个站点，参考下方文档", "cosmossport.gr"},
		{"ocoland.no，子域横向作为目标确实没有可探测方向时的备选", "ocoland.no"},
		{"Example.COM.", "example.com"},
		{"127.0.0.1", "127.0.0.1"},
		{"2001:db8::1", "2001:db8::1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"*.example.com", "*.example.com"},
		{"www.example.com:8443", "www.example.com"},
		{"www.example.com/path", "www.example.com"},
		{"bad..example.com", ""},
		{"-bad.example.com", ""},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := SanitizeHostname(c.in); got != c.want {
			t.Errorf("SanitizeHostname(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeURLDropsPastedInstructions(t *testing.T) {
	raw := "https://www.esm-computer.de，本地用了全局代理，严禁使用直连的方式暴露本机真实ip，禁止测试xss。"
	if got, want := normalizeURL(raw), "https://www.esm-computer.de"; got != want {
		t.Fatalf("normalizeURL(%q) = %q, want %q", raw, got, want)
	}
	domain, port, svc := parseURL(normalizeURL(raw))
	if domain != "www.esm-computer.de" || port != 443 || svc != "HTTPS" {
		t.Fatalf("parseURL => (%q,%d,%q), want (www.esm-computer.de,443,HTTPS)", domain, port, svc)
	}
}
