package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	pgdb "github.com/lin9527ya/blade/db"
)

func TestBrowserMCPKindClassification(t *testing.T) {
	cases := []struct {
		srv  *pgdb.MCPServer
		want string
	}{
		{&pgdb.MCPServer{Name: "browser", Transport: "stdio", Command: "npx", Args: json.RawMessage(`["@playwright/mcp"]`)}, "playwright"},
		{&pgdb.MCPServer{Name: "playwright", Transport: "stdio", Command: "npx", Args: json.RawMessage(`["@playwright/mcp","--no-sandbox"]`)}, "playwright"},
		{&pgdb.MCPServer{Name: "custom-pw", Transport: "stdio", Command: "npx", Args: json.RawMessage(`["@playwright/mcp"]`)}, "playwright"},
		{&pgdb.MCPServer{Name: "chrome-devtools", Transport: "stdio", Command: "npx", Args: json.RawMessage(`["-y","chrome-devtools-mcp@latest"]`)}, "chrome-devtools"},
		{&pgdb.MCPServer{Name: "playwright", Transport: "http", URL: "http://127.0.0.1:8931/mcp"}, ""},
		{&pgdb.MCPServer{Name: "nuclei-mcp", Transport: "stdio", Command: "nuclei-mcp"}, ""},
	}
	for _, tc := range cases {
		if got := browserMCPKind(tc.srv); got != tc.want {
			t.Fatalf("browserMCPKind(%q/%s) = %q, want %q", tc.srv.Name, tc.srv.Transport, got, tc.want)
		}
	}
}

func TestReconcileBrowserMCPArgsEnvStealthAndProxy(t *testing.T) {
	dir := t.TempDir()
	rawArgs := json.RawMessage(`["@playwright/mcp","--no-sandbox","--proxy-server","http://old:1"]`)
	rawEnv := json.RawMessage(`{"FOO":"bar","NODE_EXTRA_CA_CERTS":"/old.pem"}`)

	nextArgs, nextEnv, changed := reconcileBrowserMCPArgsEnv("playwright", dir, "http://127.0.0.1:17890", "/ca.pem", rawArgs, rawEnv)
	if !changed {
		t.Fatal("expected initial reconcile to report changes")
	}
	args := decodeStrSlice(nextArgs)
	env := decodeStrMap(nextEnv)

	if env["NODE_EXTRA_CA_CERTS"] != "/ca.pem" || env["FOO"] != "bar" {
		t.Fatalf("unexpected env after reconcile: %+v", env)
	}
	if !slices.Contains(args, "--proxy-server") || !slices.Contains(args, "http://127.0.0.1:17890") {
		t.Fatalf("missing updated proxy args: %v", args)
	}
	if slices.Contains(args, "http://old:1") {
		t.Fatalf("old proxy arg survived: %v", args)
	}
	if !slices.Contains(args, "--ignore-https-errors") || !slices.Contains(args, "--user-agent") || !slices.Contains(args, "--viewport-size") {
		t.Fatalf("missing stealth/TLS args: %v", args)
	}
	for i, a := range args {
		if a == "--user-agent" && i+1 < len(args) {
			if strings.Contains(args[i+1], "HeadlessChrome") || strings.Contains(strings.ToLower(args[i+1]), "blade") {
				t.Fatalf("browser UA contains headless/platform signature: %q", args[i+1])
			}
			if args[i+1] != browserPlaywrightUserAgent() {
				t.Fatalf("browser UA = %q, want matched runtime Chromium UA %q", args[i+1], browserPlaywrightUserAgent())
			}
		}
	}

	initPath := filepath.Join(dir, "browser_profile", "init-stealth.js")
	cfgPath := filepath.Join(dir, "browser_profile", "mcp-config.json")
	if !slices.Contains(args, initPath) || !slices.Contains(args, cfgPath) {
		t.Fatalf("missing managed init-script/config paths in %v", args)
	}
	initBytes, err := os.ReadFile(initPath)
	if err != nil || !strings.Contains(string(initBytes), "webdriver") || strings.Contains(strings.ToLower(string(initBytes)), "blade") {
		t.Fatalf("unexpected init-stealth.js content err=%v", err)
	}
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil || !strings.Contains(string(cfgBytes), "AutomationControlled") {
		t.Fatalf("unexpected mcp-config.json content err=%v", err)
	}

	// Second call must be a clean idempotent no-op.
	if _, _, changed2 := reconcileBrowserMCPArgsEnv("playwright", dir, "http://127.0.0.1:17890", "/ca.pem", nextArgs, nextEnv); changed2 {
		t.Fatal("reconcile must be idempotent")
	}

	// Custom --user-agent and --device overrides must be preserved.
	customArgs := json.RawMessage(`["@playwright/mcp","--user-agent","CustomAgent/1.0","--device","iPhone 15"]`)
	cNextArgs, _, _ := reconcileBrowserMCPArgsEnv("playwright", dir, "", "", customArgs, json.RawMessage(`{}`))
	cArgs := decodeStrSlice(cNextArgs)
	if !slices.Contains(cArgs, "CustomAgent/1.0") || slices.Contains(cArgs, "--viewport-size") {
		t.Fatalf("custom UA / --device should not be overridden: %v", cArgs)
	}
}
