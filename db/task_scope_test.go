package db

import (
	"testing"
)

// TestStripHostPort covers the ip:port / [ipv6]:port stripping used by the
// ip/cidr scope path so a target like "10.0.188.136:3000" no longer 404s.
func TestStripHostPort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"10.0.188.136:3000", "10.0.188.136"},       // the reported IP case
		{"10.0.188.136", "10.0.188.136"},            // bare IPv4 unchanged
		{"[2001:db8::1]:8080", "2001:db8::1"},       // bracketed IPv6 + port
		{"2001:db8::1", "2001:db8::1"},              // bare IPv6 unchanged (has colons)
		{" 1.2.3.4:80 ", "1.2.3.4"},                 // trims surrounding space
		{"example.com:443", "example.com"},          // domain + port → bare host
		{"api.example.com:8080", "api.example.com"}, // subdomain + port
		{"example.com", "example.com"},              // bare domain unchanged
	}
	for _, c := range cases {
		if got := stripHostPort(c.in); got != c.want {
			t.Errorf("stripHostPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestAddAgentScopeCanonicalizesNetwork 钉住范围输入的网络规范化：
// 用户写 192.168.1.1/24 时，存进 PostgreSQL cidr 的必须是 192.168.1.0/24。
// 原实现只 ParseCIDR 校验、原样保存，带主机位的 CIDR 会被 PG 拒绝（表现为
// "网段加不进去，只能加 /32"）。
func TestAddAgentScopeCanonicalizesNetwork(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	task, err := d.CreateTask("scope canonicalize", "scope canonicalize", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID := task.ID
	defer d.DeleteTask(taskID)

	cases := []struct {
		kind, value string
		wantKind    string
		wantNet     string
	}{
		{"ip", "192.168.1.1", "ip", "192.168.1.1/32"},
		{"cidr", "192.168.1.0/24", "cidr", "192.168.1.0/24"},
		{"cidr", "192.168.1.1/24", "cidr", "192.168.1.0/24"},
		{"ip", "192.168.1.1/24", "cidr", "192.168.1.0/24"},
		{"ip", "10.0.0.1:3000", "ip", "10.0.0.1/32"},
	}
	for _, tc := range cases {
		got, err := d.Assets().AddAgentScope(taskID, tc.kind, tc.value, "test", "manual")
		if err != nil {
			t.Fatalf("AddAgentScope(%s,%q): %v", tc.kind, tc.value, err)
		}
		if got.Kind != tc.wantKind || got.Net != tc.wantNet {
			t.Errorf("AddAgentScope(%s,%q) = kind %q net %q, want %q %q",
				tc.kind, tc.value, got.Kind, got.Net, tc.wantKind, tc.wantNet)
		}
	}
	for _, bad := range []string{"192.168.1.999", "192.168.1.0/33"} {
		if _, err := d.Assets().AddAgentScope(taskID, "cidr", bad, "test", "manual"); err == nil {
			t.Errorf("AddAgentScope(cidr,%q) should fail", bad)
		}
	}
	// 明确选「网段」但不给掩码：不能静默变成 /32，要提示补 /24。
	if _, err := d.Assets().AddAgentScope(taskID, "cidr", "192.168.1.0", "test", "manual"); err == nil {
		t.Error("AddAgentScope(cidr, bare IP) should fail instead of silently using /32")
	}
}
