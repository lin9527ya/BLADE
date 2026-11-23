package traffic

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

func TestHostOnly(t *testing.T) {
	cases := map[string]string{
		"example.com:443": "example.com",
		"example.com":     "example.com",
		"10.0.0.1:8080":   "10.0.0.1",
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q)=%q want %q", in, got, want)
		}
	}
}

func TestProxyCausedErr(t *testing.T) {
	proxy := []string{
		"protocol error: received DATA on a HEAD request",
		"http2: server sent GOAWAY",
		"malformed HTTP response",
	}
	target := []string{ // target-side failures must NOT trigger passthrough
		"dial tcp 1.2.3.4:443: connect: connection refused",
		"read: connection reset by peer",
		"context deadline exceeded",
	}
	for _, s := range proxy {
		if !proxyCausedErr(errors.New(s)) {
			t.Errorf("expected proxy-caused: %q", s)
		}
	}
	for _, s := range target {
		if proxyCausedErr(errors.New(s)) {
			t.Errorf("expected NOT proxy-caused: %q", s)
		}
	}
}

func TestMaybePassthroughFlagsHostOnce(t *testing.T) {
	tr := &Traffic{}
	f := &mproxy.Flow{Request: &mproxy.Request{URL: &url.URL{Host: "target.test:443"}}}

	// Target-caused error → do NOT flag (keep MITM + recording).
	tr.maybePassthrough(f, errors.New("connection refused"))
	if tr.isPassthrough("target.test") {
		t.Fatal("target-caused error must not flag passthrough")
	}

	// Proxy-caused error → flag the host for temporary passthrough.
	tr.maybePassthrough(f, errors.New("protocol error: received DATA on a HEAD request"))
	if !tr.isPassthrough("target.test") {
		t.Fatal("proxy-caused error must flag passthrough")
	}

	// The shouldIntercept rule uses hostOnly(req.Host); the CONNECT host carries a
	// port, so it must resolve to the same flagged key → intercept=false (tunnel).
	if !tr.isPassthrough(hostOnly("target.test:443")) {
		t.Fatal("flagged host must be recognized for the CONNECT form with port")
	}
}

func TestMaybePassthroughExpiresTTLAndCapsSize(t *testing.T) {
	tr := &Traffic{
		pass: map[string]time.Time{
			"expired.test": time.Now().Add(-time.Second),
		},
	}
	if tr.isPassthrough("expired.test") {
		t.Fatal("expired passthrough entry must recover MITM")
	}

	for i := 0; i < maxPassthroughHost+32; i++ {
		f := &mproxy.Flow{Request: &mproxy.Request{URL: &url.URL{Host: fmt.Sprintf("h%d.test:443", i)}}}
		tr.maybePassthrough(f, errors.New("http2: server sent GOAWAY"))
	}
	tr.passMu.Lock()
	n := len(tr.pass)
	tr.passMu.Unlock()
	if n > maxPassthroughHost {
		t.Fatalf("passthrough map exceeded cap %d: got %d", maxPassthroughHost, n)
	}
}

func TestPublicCACertSeparatedFromPrivateCA(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	caPath := tr.CACertPath()
	if !strings.Contains(caPath, string(filepath.Separator)+"_public_ca"+string(filepath.Separator)) {
		t.Fatalf("CACertPath should point to _public_ca, got %q", caPath)
	}
	if _, err := os.Stat(caPath); err != nil {
		t.Fatalf("public CA cert missing: %v", err)
	}
	privDir := filepath.Join(dir, "_ca")
	fi, err := os.Stat(privDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("_ca directory permissions too loose: %o", fi.Mode().Perm())
	}
}
