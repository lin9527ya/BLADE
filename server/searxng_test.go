package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestNormalizeSearxngManagedConfig(t *testing.T) {
	cfg, err := normalizeSearxngManagedConfig(SearxngManagedConfig{})
	if err != nil {
		t.Fatalf("empty config should yield valid defaults: %v", err)
	}
	if cfg.Port != defaultSearxngPort || cfg.ProxyMode != "inherit" || cfg.EnginesPreset != "security" || cfg.RepoURL != defaultSearxngRepoURL {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	if _, err := normalizeSearxngManagedConfig(SearxngManagedConfig{Port: 80}); err == nil {
		t.Fatal("expected privileged port <1024 to be rejected")
	}
	if _, err := normalizeSearxngManagedConfig(SearxngManagedConfig{Port: 8888, ProxyMode: "custom"}); err == nil {
		t.Fatal("expected custom proxy_mode without URL to be rejected")
	}
	if _, err := normalizeSearxngManagedConfig(SearxngManagedConfig{Port: 8888, RepoURL: "--upload-pack=evil"}); err == nil {
		t.Fatal("expected leading '-' repo_url to be rejected")
	}
	if _, err := normalizeSearxngManagedConfig(SearxngManagedConfig{Port: 8888, RepoURL: "file:///etc/passwd"}); err == nil {
		t.Fatal("expected non-http(s) repo_url to be rejected")
	}
	if _, err := normalizeSearxngManagedConfig(SearxngManagedConfig{
		Port: 8899, ProxyMode: "custom", CustomProxy: "socks5h://127.0.0.1:7897",
		EnginesPreset: "cn_friendly", PyPIIndex: "https://pypi.tuna.tsinghua.edu.cn/simple",
	}); err != nil {
		t.Fatalf("expected valid full config to pass: %v", err)
	}
}

func TestRenderSearxngSettingsYAML(t *testing.T) {
	out := renderSearxngSettingsYAML("0123456789abcdef0123456789abcdef", "127.0.0.1", 8890, "socks5h://127.0.0.1:7897", "security")
	for _, want := range []string{
		"# managed-by: blade",
		`bind_address: "127.0.0.1"`,
		"port: 8890",
		"limiter: false",
		"- html",
		"- json",
		`all://:`,
		`"socks5h://127.0.0.1:7897"`,
		"- name: github",
		"- name: duckduckgo",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered YAML missing %q:\n%s", want, out)
		}
	}

	directOut := renderSearxngSettingsYAML("abcdef", "0.0.0.0", 8080, "", "cn_friendly")
	if strings.Contains(directOut, "proxies:") {
		t.Fatalf("direct render should not include outgoing proxies:\n%s", directOut)
	}
	if !strings.Contains(directOut, "- name: baidu") || !strings.Contains(directOut, `bind_address: "0.0.0.0"`) {
		t.Fatalf("unexpected cn_friendly compose YAML:\n%s", directOut)
	}
}

func TestSearxngChildEnvScrubsReservedSecretsAndSetsProxy(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"BLADE_PG_DSN=postgres://secret:pass@db/blade",
		"OPENAI_API_KEY=sk-secret-value",
		"HTTP_PROXY=http://stale:9999",
	}
	env := buildSearxngChildEnv(base, map[string]string{
		"SEARXNG_SETTINGS_PATH": "/tmp/settings.yml",
	}, "http://127.0.0.1:7897")

	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "BLADE_PG_DSN") || strings.Contains(joined, "OPENAI_API_KEY") || strings.Contains(joined, "http://stale:9999") {
		t.Fatalf("expected reserved secrets and stale proxy stripped, got:\n%s", joined)
	}
	if !strings.Contains(joined, "HTTPS_PROXY=http://127.0.0.1:7897") || !strings.Contains(joined, "SEARXNG_SETTINGS_PATH=/tmp/settings.yml") {
		t.Fatalf("expected explicit proxy & settings path set, got:\n%s", joined)
	}
}

func TestSearxngMockSidecarProcess(t *testing.T) {
	if os.Getenv("BLADE_SEARXNG_TEST_HELPER") != "1" {
		return
	}
	port := "8888"
	for i := 0; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--port" {
			port = os.Args[i+1]
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			http.Error(w, "format denied", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]string{
				{"title": "Mock SearXNG Hit", "url": "https://example.org/searx", "content": "ok"},
			},
		})
	})
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen failed: %v\n", err)
		os.Exit(2)
	}
	srv := &http.Server{Handler: mux}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		<-ch
		_ = srv.Close()
	}()
	_ = srv.Serve(ln)
	os.Exit(0)
}

func TestSearxngHostVenvLifecycleAndStatus(t *testing.T) {
	tmp := t.TempDir()
	m := &Manager{dir: tmp}
	sx := m.searxng()

	// Find free loopback port for mock sidecar.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	// Seed synthetic installed venv/src pointing venv/bin/granian to our test helper binary.
	if err := os.MkdirAll(filepath.Join(sx.srcDir(), "searx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sx.srcDir(), "searx", "webapp.py"), []byte("# mock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(sx.venvDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "python"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	granianWrapper := fmt.Sprintf("#!/bin/sh\nexport BLADE_SEARXNG_TEST_HELPER=1\nexec %q -test.run=^TestSearxngMockSidecarProcess$ -- \"$@\"\n", exe)
	if err := os.WriteFile(filepath.Join(binDir, "granian"), []byte(granianWrapper), 0o755); err != nil {
		t.Fatal(err)
	}

	if !sx.isInstalled() {
		t.Fatal("expected synthetic venv installation check to pass")
	}

	cfg := defaultSearxngManagedConfig()
	cfg.Port = port
	cfg.AutoSwitch = true
	if err := sx.saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	if err := sx.startSidecarSync(context.Background(), cfg); err != nil {
		t.Fatalf("startSidecarSync failed: %v", err)
	}
	t.Cleanup(func() {
		_ = sx.stopProcessOnly()
	})

	// Check PID file exists and Status reports running + healthy + active_in_search.
	st := sx.Status(context.Background())
	if !st.Installed || !st.Running || !st.Healthy || st.State != "running" || st.PID <= 1 {
		t.Fatalf("unexpected running status: %+v", st)
	}
	wantURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if st.URL != wantURL || !st.ActiveInSearch || m.SearxngURL() != wantURL {
		t.Fatalf("expected auto-switched web search to %q: status=%+v mgrURL=%q", wantURL, st, m.SearxngURL())
	}

	// Verify Stop cleanly terminates the Setsid sidecar and removes PID file.
	stopped, err := sx.Stop()
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if stopped.Running || stopped.PID != 0 || stopped.State != "stopped" {
		t.Fatalf("expected stopped status after Stop(): %+v", stopped)
	}
}

func TestSearxngComposeBootPreparesSettingsAndPreservesManualEdit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" && r.URL.Query().Get("format") == "json" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"title":"Compose Hit","url":"https://compose.test"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	t.Setenv("BLADE_SEARXNG_MANAGED_MODE", "compose")
	t.Setenv("BLADE_SEARXNG_COMPANION_URL", srv.URL)

	tmp := t.TempDir()
	m := &Manager{dir: tmp}
	sx := m.searxng()

	sx.EnsureComposeBootConfig()
	raw, err := os.ReadFile(sx.settingsPath())
	if err != nil {
		t.Fatalf("expected boot settings.yml created: %v", err)
	}
	if !strings.Contains(string(raw), "# managed-by: blade") || !strings.Contains(string(raw), "- json") {
		t.Fatalf("unexpected compose boot settings.yml:\n%s", string(raw))
	}
	if m.SearxngURL() != srv.URL {
		t.Fatalf("expected default compose SearxngURL=%q, got %q", srv.URL, m.SearxngURL())
	}

	// 手动移除 managed 头后，启动准备不得覆盖手工修改的配置。
	customYAML := "use_default_settings: true\n# user custom config\n"
	if err := os.WriteFile(sx.settingsPath(), []byte(customYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	sx.EnsureComposeBootConfig()
	after, _ := os.ReadFile(sx.settingsPath())
	if string(after) != customYAML {
		t.Fatalf("expected manual settings.yml preserved, got:\n%s", string(after))
	}

	st := sx.Status(context.Background())
	if st.Mode != "compose" || !st.Running || !st.Healthy || st.State != "running" {
		t.Fatalf("expected healthy compose status, got %+v", st)
	}
}
