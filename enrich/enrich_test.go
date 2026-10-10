package enrich

import (
	"testing"
	"time"
)

// TestExtractTitle pins the <title> extraction: tags with attributes, embedded
// entities (decoded) and surrounding whitespace (trimmed); a missing title yields
// "" rather than a partial or stale match.
func TestExtractTitle(t *testing.T) {
	cases := []struct{ body, want string }{
		{`<html><head><title>Acme &amp; Co</title></head></html>`, "Acme & Co"},
		{"<title>\n  多行 标题 \n</title>", "多行 标题"},
		{`<TITLE class="x" data-y="1">大写标签</TITLE>`, "大写标签"},
		{`<html><body>no title here</body></html>`, ""},
		{``, ""},
	}
	for _, c := range cases {
		if got := extractTitle([]byte(c.body)); got != c.want {
			t.Fatalf("extractTitle(%q)=%q want %q", c.body, got, c.want)
		}
	}
}

// TestHostOf pins hostname extraction for probe results: ports are dropped, and
// inputs the URL parser cannot resolve (including scheme-less hosts) yield "".
func TestHostOf(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"https://api.example.com:8443/path?q=1", "api.example.com"},
		{"http://127.0.0.1/", "127.0.0.1"},
		{"http://[2001:db8::1]:8080/x", "2001:db8::1"},
		{"example.com", ""},    // 无 scheme → 解析不出 Hostname
		{"http://%zz bad", ""}, // 非法转义 → 解析失败
	}
	for _, c := range cases {
		if got := hostOf(c.raw); got != c.want {
			t.Fatalf("hostOf(%q)=%q want %q", c.raw, got, c.want)
		}
	}
}

// TestIsBlank pins the "nothing to record" predicate used before writing asset
// attributes: nil, empty strings and numeric zeros are blank; everything else is not.
func TestIsBlank(t *testing.T) {
	for _, v := range []any{nil, "", float64(0), 0} {
		if !isBlank(v) {
			t.Fatalf("isBlank(%#v) must be true", v)
		}
	}
	for _, v := range []any{"x", float64(1), 7, false} {
		if isBlank(v) {
			t.Fatalf("isBlank(%#v) must be false", v)
		}
	}
}

// TestUniqPreservesOrder pins the in-place dedupe used to collapse resolver
// answers: first occurrence wins, order is kept.
func TestUniqPreservesOrder(t *testing.T) {
	got := uniq([]string{"a", "b", "a", "c", "b"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("uniq=%v want [a b c]", got)
	}
	if empty := uniq(nil); len(empty) != 0 {
		t.Fatalf("uniq(nil)=%v", empty)
	}
}

// TestOnCooldownWindow pins the per-(kind,id) cooldown: the first run is admitted
// and stamped, a rerun inside the window is skipped, different kinds do not share
// the window, and an expired stamp lets the job through again.
func TestOnCooldownWindow(t *testing.T) {
	e := &Engine{}
	j := job{kind: jobHTTP, id: 7, arg: "http://example.com"}
	if e.onCooldown(j) {
		t.Fatal("first run must be admitted")
	}
	if !e.onCooldown(j) {
		t.Fatal("rerun inside the window must be skipped")
	}
	if e.onCooldown(job{kind: jobDNS, id: 7}) {
		t.Fatal("a different kind must not share the window")
	}
	e.cool.Store(string(rune(jobHTTP))+":"+itoa(7), time.Now().Add(-cooldown-time.Minute))
	if e.onCooldown(j) {
		t.Fatal("an expired stamp must admit the job again")
	}
}
