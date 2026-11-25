package agent

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyEnvEmptyIsNil(t *testing.T) {
	if env := proxyEnv("", ""); env != nil {
		t.Fatalf("proxyEnv(\"\", \"\") = %v, want nil (direct)", env)
	}
}

func TestProxyEnvSetsAllProxyForSocks5(t *testing.T) {
	// Capture-off egress path: a socks5 proxy, no MITM CA. ALL_PROXY must be set
	// (curl reads socks5 only from there), and no CA vars should appear.
	env := proxyEnv("socks5://10.0.0.1:1080", "")
	has := func(prefix string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "all_proxy="} {
		if !has(want) {
			t.Errorf("proxyEnv missing %s: %v", want, env)
		}
	}
	if has("SSL_CERT_FILE=") || has("CURL_CA_BUNDLE=") {
		t.Errorf("proxyEnv without CA must not inject CA vars: %v", env)
	}
}

func TestProxyEnvInjectsCAWhenRecording(t *testing.T) {
	env := proxyEnv("http://127.0.0.1:8788", "/data/ca.pem")
	has := func(prefix string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"SSL_CERT_FILE=", "CURL_CA_BUNDLE=", "REQUESTS_CA_BUNDLE=", "NODE_EXTRA_CA_CERTS="} {
		if !has(want) {
			t.Errorf("proxyEnv with CA missing %s: %v", want, env)
		}
	}
}

func TestProxyEnvPreservesSocks5h(t *testing.T) {
	env := proxyEnv("socks5h://user:pass@10.0.0.1:1080", "")
	foundAll := false
	for _, e := range env {
		if e == "ALL_PROXY=socks5h://user:pass@10.0.0.1:1080" {
			foundAll = true
		}
	}
	if !foundAll {
		t.Fatalf("proxyEnv must preserve socks5h:// for CLI remote DNS, got %v", env)
	}
}

func TestToolSetDynamicEgressProxyFuncAndFailureHook(t *testing.T) {
	var proxyHits int
	pSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits++
		_, _ = w.Write([]byte("proxied"))
	}))
	defer pSrv.Close()

	active := "http://127.0.0.1:1"
	ts := NewToolSet(nil, "test")
	defer ts.CloseHTTP()
	ts.SetEgressProxyFunc(func() (string, string) { return active, "" })
	var hookProxy string
	var hookErr error
	ts.SetProxyFailureHook(func(proxyURL string, err error) {
		hookProxy, hookErr = proxyURL, err
	})

	// First request hits dead proxy and triggers failure hook.
	res1 := ts.doHTTP(context.Background(), httpArgs{Method: "GET", URL: "http://example.com"}, nil)
	if res1.Err == nil || hookProxy != "http://127.0.0.1:1" || hookErr == nil {
		t.Fatalf("expected failure hook on dead proxy, err=%v hook=(%q,%v)", res1.Err, hookProxy, hookErr)
	}

	// Switching active proxy takes effect immediately on the next call.
	active = pSrv.URL
	res2 := ts.doHTTP(context.Background(), httpArgs{Method: "GET", URL: "http://example.com"}, nil)
	if res2.Err != nil || res2.Status != 200 || proxyHits != 1 {
		t.Fatalf("expected dynamic proxy switch, err=%v status=%d hits=%d", res2.Err, res2.Status, proxyHits)
	}
}

func TestSearchHTTPClientSOCKS5AuthAndSocks5h(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	authMethodSeen := make(chan byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		hdr := make([]byte, 2)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		methods := make([]byte, int(hdr[1]))
		if _, err := io.ReadFull(conn, methods); err != nil {
			return
		}
		var sawUserPass byte
		for _, m := range methods {
			if m == 0x02 {
				sawUserPass = m
				break
			}
		}
		select {
		case authMethodSeen <- sawUserPass:
		default:
		}
		// Reject so client terminates cleanly right after method negotiation.
		_, _ = conn.Write([]byte{0x05, 0xFF})
	}()

	cl, err := searchHTTPClient("socks5h://alice:s3cr3t@" + ln.Addr().String())
	if err != nil {
		t.Fatalf("searchHTTPClient: %v", err)
	}
	cl.Timeout = 2 * time.Second
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
	_, _ = cl.Do(req)

	var seen byte
	select {
	case seen = <-authMethodSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("socks5 handshake timed out")
	}
	if seen != 0x02 {
		t.Fatalf("expected SOCKS5 username/password auth method (0x02), got %#x", seen)
	}
	_ = binary.BigEndian
}
