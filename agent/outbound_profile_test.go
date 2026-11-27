package agent

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestOutboundProfileSelectionAndHeaders(t *testing.T) {
	// Different tasks should diversify profiles while keeping one task deterministic.
	p1a := OutboundProfileForTask(1)
	p1b := OutboundProfileForTask(1)
	if p1a.ID != p1b.ID || p1a.UserAgent == "" {
		t.Fatalf("expected deterministic non-empty profile for task 1, got %+v vs %+v", p1a, p1b)
	}
	seenIDs := map[string]bool{}
	for id := int64(1); id <= 15; id++ {
		p := OutboundProfileForTask(id)
		if strings.Contains(p.UserAgent, "Chrome/131.0.0.0") || strings.Contains(p.UserAgent, "Chrome/124") {
			t.Fatalf("outdated hardcoded Chrome UA still present in pool: %s", p.UserAgent)
		}
		if strings.Contains(strings.ToLower(p.UserAgent), "blade") || strings.Contains(strings.ToLower(p.UserAgent), "artex") {
			t.Fatalf("platform identifier in outbound UA: %s", p.UserAgent)
		}
		seenIDs[p.ID] = true
	}
	if len(seenIDs) < 3 {
		t.Fatalf("expected multi-profile rotation across tasks, saw only %d: %v", len(seenIDs), seenIDs)
	}

	// Custom User-Agent override must suppress mismatched Sec-CH-UA* headers.
	req, _ := http.NewRequest(http.MethodGet, "https://example.test/index.html", nil)
	ApplyRequestProfile(req, OutboundProfileForTask(1), false, map[string]bool{"user-agent": true})
	if got := req.Header.Get("Sec-CH-UA"); got != "" {
		t.Fatalf("expected no Sec-CH-UA when User-Agent is overridden, got %q", got)
	}
	if req.Header.Get("Accept-Language") == "" || !strings.Contains(req.Header.Get("Accept-Encoding"), "br") {
		t.Fatalf("expected coherent Accept-Language and br encoding, got %+v", req.Header)
	}
}

func TestHTTPRequestProfileHeadersAndDecompression(t *testing.T) {
	var mu sync.Mutex
	var lastHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastHeaders = r.Header.Clone()
		mu.Unlock()
		switch r.URL.Path {
		case "/gzip":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			_, _ = gz.Write([]byte(`{"enc":"gzip","msg":"ok"}`))
			_ = gz.Close()
		case "/br":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "br")
			bw := brotli.NewWriter(w)
			_, _ = bw.Write([]byte("brotli-decoded-payload"))
			_ = bw.Close()
		case "/deflate-zlib":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "deflate")
			zw := zlib.NewWriter(w)
			_, _ = zw.Write([]byte("zlib-deflate-ok"))
			_ = zw.Close()
		case "/deflate-raw":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "deflate")
			fw, _ := flate.NewWriter(w, flate.DefaultCompression)
			_, _ = fw.Write([]byte("raw-deflate-ok"))
			_ = fw.Close()
		case "/bad-gzip":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write([]byte("not-actually-gzipped"))
		default:
			_, _ = w.Write([]byte("plain"))
		}
	}))
	defer srv.Close()

	runDir := t.TempDir()
	ts := NewToolSet(nil, "work#1")
	ts.SetRunDir(runDir)
	ts.SetTaskID(42)

	// Force deterministic Chromium profile for Sec-CH-UA verification.
	t.Setenv("BLADE_OUTBOUND_UA", "")
	prof := OutboundProfileForTask(42)

	for _, tc := range []struct {
		path string
		want string
	}{
		{"/gzip", `"msg":"ok"`},
		{"/br", "brotli-decoded-payload"},
		{"/deflate-zlib", "zlib-deflate-ok"},
		{"/deflate-raw", "raw-deflate-ok"},
		{"/bad-gzip", "not-actually-gzipped"},
	} {
		out := srcCall(t, ts, "http_request", `{"url":`+quoteJSON(srv.URL+tc.path)+`}`)
		var parsed struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Fatalf("%s invalid json result: %v (%s)", tc.path, err, out)
		}
		if !strings.Contains(parsed.Body, tc.want) {
			t.Fatalf("%s expected decoded body %q, got: %q", tc.path, tc.want, parsed.Body)
		}
		mu.Lock()
		hdr := lastHeaders.Clone()
		mu.Unlock()
		if gotUA := hdr.Get("User-Agent"); gotUA != prof.UserAgent {
			t.Fatalf("%s UA = %q, want %q", tc.path, gotUA, prof.UserAgent)
		}
		if hdr.Get("Accept-Language") == "" {
			t.Fatalf("%s missing Accept-Language", tc.path)
		}
		if gotEnc := hdr.Get("Accept-Encoding"); !strings.Contains(gotEnc, "br") || !strings.Contains(gotEnc, "gzip") {
			t.Fatalf("%s missing browser Accept-Encoding, got %q", tc.path, gotEnc)
		}
		if prof.Family == "chromium" && hdr.Get("Sec-CH-UA") == "" {
			t.Fatalf("%s chromium profile missing Sec-CH-UA", tc.path)
		}
		if hdr.Get("Accept") == "*/*" {
			t.Fatalf("%s should send coherent browser Accept instead of bare */*", tc.path)
		}
	}

	// API path should prefer AcceptAPI, and custom User-Agent must suppress Sec-CH-UA.
	_ = srcCall(t, ts, "http_request", `{"url":`+quoteJSON(srv.URL+"/api/v1/users")+`,"headers":"User-Agent: CustomBot/1.0"}`)
	mu.Lock()
	apiHdr := lastHeaders.Clone()
	mu.Unlock()
	if apiHdr.Get("User-Agent") != "CustomBot/1.0" {
		t.Fatalf("custom UA overridden: %q", apiHdr.Get("User-Agent"))
	}
	if apiHdr.Get("Sec-CH-UA") != "" {
		t.Fatalf("Sec-CH-UA leaked alongside custom UA: %q", apiHdr.Get("Sec-CH-UA"))
	}
	if !strings.Contains(apiHdr.Get("Accept"), "application/json") {
		t.Fatalf("API endpoint expected JSON-preferring Accept, got %q", apiHdr.Get("Accept"))
	}
}

func TestBLADEWebFetchAndCommandCodeNeutralUA(t *testing.T) {
	var gotWFHeaders, gotCCHeaders http.Header
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/alpha/web-search" {
			gotCCHeaders = r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"title":"t","url":"https://example.test","snippet":"s"}]}`))
			return
		}
		gotWFHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><h1>hi</h1></body></html>"))
	}))
	defer srv.Close()

	wf := newBLADEWebFetch(nil, "", 17)
	in, _ := json.Marshal(map[string]any{"url": srv.URL + "/page"})
	if _, err := wf.Call(context.Background(), in, nil); err != nil {
		t.Fatalf("WebFetch call: %v", err)
	}
	mu.Lock()
	wfUA := gotWFHeaders.Get("User-Agent")
	mu.Unlock()
	wantUA := OutboundProfileForTask(17).UserAgent
	if wfUA != wantUA {
		t.Fatalf("WebFetch UA = %q, want %q", wfUA, wantUA)
	}
	if strings.Contains(wfUA, "Chrome/131.0.0.0") {
		t.Fatalf("WebFetch still using outdated Chrome/131 UA: %q", wfUA)
	}

	results, err := commandCodeSearch(context.Background(), srv.URL, "k-test", "test", 3, "")
	if err != nil || len(results) != 1 {
		t.Fatalf("commandCodeSearch failed: results=%v err=%v", results, err)
	}
	mu.Lock()
	ccUA := gotCCHeaders.Get("User-Agent")
	mu.Unlock()
	if strings.Contains(strings.ToLower(ccUA), "artex") || ccUA == "" {
		t.Fatalf("commandCodeSearch UA leaked platform codename or empty: %q", ccUA)
	}

	var _ bytes.Buffer
}

func TestSubprocessCurlEnvAndToolWrappersWireHeaders(t *testing.T) {
	var mu sync.Mutex
	reqs := map[string]http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs[r.URL.Path] = r.Header.Clone()
		mu.Unlock()
		switch r.URL.Path {
		case "/app.js":
			w.Header().Set("Content-Type", "application/javascript")
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			_, _ = gz.Write([]byte(`fetch("/api/v1/internal_users");`))
			_ = gz.Close()
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><title>Portal</title></html>`))
		}
	}))
	defer srv.Close()

	workDir := t.TempDir()
	runDir := filepath.Join(workDir, "tasks", "99", "i1")
	taskRoot := filepath.Join(workDir, "tasks", "99")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLADE_OUTBOUND_UA", "")
	prof := OutboundProfileForTask(99)
	subEnv := TaskSubprocessEnv(taskRoot, 99)
	cmdEnv := append(os.Environ(), subEnv...)

	// 1. Bare curl must pick up CURL_HOME/.curlrc UA; explicit -A overrides cleanly.
	if curlBin, err := exec.LookPath("curl"); err == nil {
		c1 := exec.Command(curlBin, "-sS", srv.URL+"/bare-curl")
		c1.Env = cmdEnv
		if out, err := c1.CombinedOutput(); err != nil {
			t.Fatalf("bare curl failed: %v (%s)", err, out)
		}
		c2 := exec.Command(curlBin, "-sS", "-A", "ExplicitTest/9.9", srv.URL+"/override-curl")
		c2.Env = cmdEnv
		if out, err := c2.CombinedOutput(); err != nil {
			t.Fatalf("override curl failed: %v (%s)", err, out)
		}
		mu.Lock()
		bareUA := reqs["/bare-curl"].Get("User-Agent")
		overUA := reqs["/override-curl"].Values("User-Agent")
		mu.Unlock()
		if bareUA != prof.UserAgent || strings.HasPrefix(strings.ToLower(bareUA), "curl/") {
			t.Fatalf("bare curl exposed default curl UA or mismatched task UA: %q (want %q)", bareUA, prof.UserAgent)
		}
		if len(overUA) != 1 || overUA[0] != "ExplicitTest/9.9" {
			t.Fatalf("curl -A override failed or duplicated header: %v", overUA)
		}
	}

	// 2. http_probe & js_audit staged scripts send task UA + decode gzip cleanly.
	ts := NewToolSet(nil, "work#1")
	ts.SetRunDir(runDir)
	ts.SetTaskID(99)

	probeRes := srcCall(t, ts, "http_probe", `{"urls":[`+quoteJSON(srv.URL+"/probe")+`]}`)
	var probeJSON struct {
		Cmd string `json:"run_with_bash"`
	}
	_ = json.Unmarshal([]byte(probeRes), &probeJSON)
	pCmd := exec.Command("bash", "-c", probeJSON.Cmd)
	pCmd.Dir = runDir
	pCmd.Env = cmdEnv
	pOut, err := pCmd.CombinedOutput()
	if err != nil || !strings.Contains(string(pOut), `"title": "Portal"`) && !strings.Contains(string(pOut), `"title":"Portal"`) {
		t.Fatalf("http_probe failed: err=%v out=%s", err, pOut)
	}

	jsRes := srcCall(t, ts, "js_audit", `{"urls":[`+quoteJSON(srv.URL+"/app.js")+`]}`)
	var jsJSON struct {
		Cmd string `json:"run_with_bash"`
	}
	_ = json.Unmarshal([]byte(jsRes), &jsJSON)
	jCmd := exec.Command("bash", "-c", jsJSON.Cmd)
	jCmd.Dir = runDir
	jCmd.Env = cmdEnv
	jOut, err := jCmd.CombinedOutput()
	if err != nil || !strings.Contains(string(jOut), "/api/v1/internal_users") {
		t.Fatalf("js_audit gzip extract failed: err=%v out=%s", err, jOut)
	}

	mu.Lock()
	probeHdr := reqs["/probe"]
	jsHdr := reqs["/app.js"]
	mu.Unlock()
	if got := probeHdr.Get("User-Agent"); got != prof.UserAgent || probeHdr.Get("Accept-Language") == "" {
		t.Fatalf("http_probe wire headers bad: UA=%q Lang=%q", got, probeHdr.Get("Accept-Language"))
	}
	if got := jsHdr.Get("User-Agent"); got != prof.UserAgent || strings.Contains(got, "Chrome/124") || strings.Contains(jsHdr.Get("Accept-Encoding"), "identity") {
		t.Fatalf("js_audit wire headers bad: UA=%q Enc=%q", got, jsHdr.Get("Accept-Encoding"))
	}

	// 3. auth / diff / contract / poc builders never leave bare curl or legacy codenames.
	authCmd, err := buildAuthCurl("https://example.test/api", "GET", "", "", authEntry{Token: "tok"}, prof)
	if err != nil || !strings.Contains(authCmd, prof.UserAgent) || !strings.Contains(authCmd, "Accept-Language:") {
		t.Fatalf("buildAuthCurl missing coherent headers: %s (%v)", authCmd, err)
	}
	diffCmd := buildDiffScript(diffArgs{URLA: "https://example.test/a", URLB: "https://example.test/b"}, "/tmp/d.py", runDir, prof)
	if !strings.Contains(diffCmd, prof.UserAgent) || strings.Contains(diffScriptSource, "POJUN") {
		t.Fatalf("diff command/script has missing UA or legacy codename: %s", diffCmd)
	}
	contractCmd := buildContractCmd("https://example.test", []string{"/openapi.json"}, "/tmp/c.py", runDir, prof)
	if !strings.Contains(contractCmd, prof.UserAgent) || strings.Contains(contractScriptSource, "POJUN") {
		t.Fatalf("contract command/script has missing UA or legacy codename: %s", contractCmd)
	}
	if strings.Contains(strings.ToLower(pocCommon), "blade") || strings.Contains(strings.ToLower(pocTemplates["ssrf"]), "blade") {
		t.Fatal("poc_scaffold still contains blade marker")
	}

	// 4. Neutrality prompt covers textbook offensive indicators even in normal mode.
	oldNoise := NoiseLevel
	NoiseLevel = nil
	t.Cleanup(func() { NoiseLevel = oldNoise })
	if !strings.Contains(traceNeutralityBlock(), "evil.com") || !strings.Contains(traceNeutralityBlock(), "TARGET_USER_AGENT") {
		t.Fatal("traceNeutralityBlock missing expanded anti-fingerprint discipline")
	}
}
