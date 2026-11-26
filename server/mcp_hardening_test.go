package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/mcphttp"
)

func TestValidateMCPServerAndDiffAndHints(t *testing.T) {
	badNames := []string{"", "has space", "foo__bar", "a/b", `a\b`, "a,b", strings.Repeat("x", 49)}
	for _, name := range badNames {
		err := validateMCPServer(&db.MCPServer{Name: name, Transport: "stdio", Command: "npx"})
		if err == nil {
			t.Fatalf("expected invalid name %q to be rejected", name)
		}
	}
	if err := validateMCPServer(&db.MCPServer{Name: "chrome-devtools", Transport: "http", URL: "ftp://x"}); err == nil {
		t.Fatal("expected non-http scheme to fail")
	}
	m := &db.MCPServer{Name: "  ScopeSentry  ", Transport: " HTTP ", URL: " https://mcp.example.com/mcp ", Command: "ignored"}
	if err := validateMCPServer(m); err != nil {
		t.Fatalf("valid server rejected: %v", err)
	}
	if m.Name != "ScopeSentry" || m.Transport != "http" || m.Command != "" {
		t.Fatalf("normalized unexpected fields: %+v", m)
	}

	// JSON formatting differences in env/args must NOT trigger false endpointChanged.
	a := &db.MCPServer{
		Transport: "http",
		URL:       "https://mcp.example.com/mcp",
		Args:      json.RawMessage(`[]`),
		Env:       json.RawMessage(`{"B": "2", "A": "1"}`),
	}
	b := &db.MCPServer{
		Transport: "http",
		URL:       "https://mcp.example.com/mcp",
		Env:       json.RawMessage(`{"A":"1","B":"2"}`),
	}
	if mcpEndpointChanged(a, b) {
		t.Fatal("equal maps with different JSON key order/whitespace must not mark endpointChanged")
	}
	b.URL = "https://mcp.example.com/v2"
	if !mcpEndpointChanged(a, b) {
		t.Fatal("URL change must mark endpointChanged")
	}

	if h := mcpDiagnosticHint(fmt.Errorf("设置 %s=1", mcphttp.AllowLocalEnv)); !strings.Contains(h, mcphttp.AllowLocalEnv) {
		t.Fatalf("expected allow-local hint, got %q", h)
	}
	if h := mcpDiagnosticHint(errors.New("x509: certificate signed by unknown authority")); !strings.Contains(h, "TLS") {
		t.Fatalf("expected TLS hint, got %q", h)
	}
}

func TestBuildHardenedStdioCmdPreservesExplicitEnvWhileScrubbingHost(t *testing.T) {
	oldIso := agentIso
	agentIso = agentIsoState{enabled: true, user: "agent", home: "/tmp/agent-home"}
	actool.SetExecHook(func(cmd *exec.Cmd) {
		cmd.Env = scrubAgentEnv([]string{
			"PATH=/usr/bin",
			"BLADE_JWT_SECRET=topsecret",
			"OPENAI_API_KEY=sk-host",
			"GITHUB_TOKEN=gh_host",
			"SHODAN_API_KEY=host_shodan",
		})
	})
	t.Cleanup(func() {
		agentIso = oldIso
		actool.SetExecHook(hardenAgentCmd)
	})

	cmd := buildHardenedStdioCmd("/bin/echo", []string{"hi"}, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "user_pat_123",
		"SHODAN_API_KEY":               "user_shodan_456",
		"BLADE_JWT_SECRET":             "attacker_override",
		"HOME":                         "/evil",
	}, nil)

	envMap := map[string]string{}
	for _, kv := range cmd.Env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			envMap[kv[:i]] = kv[i+1:]
		}
	}
	if envMap["GITHUB_PERSONAL_ACCESS_TOKEN"] != "user_pat_123" || envMap["SHODAN_API_KEY"] != "user_shodan_456" {
		t.Fatalf("explicit per-MCP tokens must survive isolation scrubbing: %+v", envMap)
	}
	if _, leaked := envMap["OPENAI_API_KEY"]; leaked {
		t.Fatal("host OPENAI_API_KEY leaked into stdio cmd")
	}
	if _, leaked := envMap["GITHUB_TOKEN"]; leaked {
		t.Fatal("host GITHUB_TOKEN leaked into stdio cmd")
	}
	if envMap["BLADE_JWT_SECRET"] != "" {
		t.Fatal("reserved BLADE_* key must never be overridden or inherited")
	}
	if envMap["HOME"] != "/tmp/agent-home" {
		t.Fatalf("HOME must stay isolated, got %q", envMap["HOME"])
	}
}

func TestLazyMCPReconnectsOnExpiredHTTPSession(t *testing.T) {
	t.Setenv(mcphttp.AllowLocalEnv, "1")
	var sessionSeq int32
	var toolCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "initialize":
			n := atomic.AddInt32(&sessionSeq, 1)
			w.Header().Set("Mcp-Session-Id", fmt.Sprintf("sess-%d", n))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, req.ID)
			return
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"ping","description":"ping tool","inputSchema":{"type":"object"}}]}}`, req.ID)
			return
		case "tools/call":
			callNum := atomic.AddInt32(&toolCalls, 1)
			// Fail first call on session 1 with 404 (session expired) -> session layer should reconnect to sess-2.
			if r.Header.Get("Mcp-Session-Id") == "sess-1" && callNum == 1 {
				http.Error(w, "unknown session", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"pong-ok"}]}}`, req.ID)
			return
		}
	}))
	defer srv.Close()

	const name = "reconnect-http-test"
	t.Cleanup(func() { mcphttp.DeleteGuard(name) })
	sess := &lazyMCPServerSession{
		runCtx: context.Background(),
		srv:    &db.MCPServer{Name: name, Transport: "http", URL: srv.URL},
	}
	defer sess.Close()

	res, err := sess.call(context.Background(), "mcp__reconnect-http-test__ping", json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.IsError || res.Flatten() != "pong-ok" {
		t.Fatalf("expected seamless reconnect result pong-ok, got isErr=%v text=%q", res.IsError, res.Flatten())
	}
	if got := atomic.LoadInt32(&sessionSeq); got != 2 {
		t.Fatalf("expected 2 initialize handshakes across reconnect, got %d", got)
	}
}

func TestHelperStdioMCPProcess(t *testing.T) {
	if os.Getenv("GO_WANT_STDIO_MCP_HELPER") != "1" {
		return
	}
	mode := os.Getenv("MCP_HELPER_MODE")
	if mode == "fail-boot" {
		fmt.Fprintln(os.Stderr, "browser binary missing: run playwright install")
		os.Exit(1)
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05"}}`+"\n", req.ID)
		case "tools/list":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"echo","description":"echo arg","inputSchema":{"type":"object"}}]}}`+"\n", req.ID)
		case "tools/call":
			if mode == "crash-on-call" {
				fmt.Fprintln(os.Stderr, "fatal worker crash")
				os.Exit(2)
			}
			var p struct {
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"echo:%v"}]}}`+"\n", req.ID, p.Arguments["msg"])
		}
	}
	os.Exit(0)
}

func TestStdioMCPClientGuardStderrCaptureAndReconnect(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Failing stdio process surfaces stderr AND trips shared mcphttp Guard breaker.
	const badSrv = "stdio-fail-guard"
	t.Cleanup(func() {
		mcphttp.SetGuardConfigResolver(nil)
		mcphttp.DeleteGuard(badSrv)
	})
	mcphttp.SetGuardConfigResolver(func(string) mcphttp.GuardConfig {
		return mcphttp.GuardConfig{MaxConcurrent: 2, BreakerThreshold: 2, BreakerCooldown: 5 * time.Second}
	})

	for i := 0; i < 2; i++ {
		_, derr := dialStdioMCP(context.Background(), badSrv, exe, map[string]string{
			"GO_WANT_STDIO_MCP_HELPER": "1",
			"MCP_HELPER_MODE":          "fail-boot",
		}, "-test.run=^TestHelperStdioMCPProcess$")
		if derr == nil || !strings.Contains(derr.Error(), "browser binary missing") {
			t.Fatalf("attempt %d should surface stderr from failing stdio MCP, got %v", i+1, derr)
		}
	}
	_, openErr := dialStdioMCP(context.Background(), badSrv, exe, map[string]string{
		"GO_WANT_STDIO_MCP_HELPER": "1",
		"MCP_HELPER_MODE":          "ok",
	}, "-test.run=^TestHelperStdioMCPProcess$")
	if openErr == nil || !strings.Contains(openErr.Error(), "熔断冷却") {
		t.Fatalf("stdio failures must open shared mcphttp circuit breaker, got %v", openErr)
	}

	// 2. Working stdio MCP client executes initialize, tools/list, and tools/call end-to-end.
	const okSrv = "stdio-ok-helper"
	t.Cleanup(func() { mcphttp.DeleteGuard(okSrv) })
	cl, err := dialStdioMCP(context.Background(), okSrv, exe, map[string]string{
		"GO_WANT_STDIO_MCP_HELPER": "1",
		"MCP_HELPER_MODE":          "ok",
	}, "-test.run=^TestHelperStdioMCPProcess$")
	if err != nil {
		t.Fatalf("dial ok stdio helper: %v", err)
	}
	defer cl.Close()
	if !cl.Alive() {
		t.Fatal("expected connected stdio client to report Alive() == true")
	}
	tools, err := cl.Tools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name() != "mcp__stdio-ok-helper__echo" {
		t.Fatalf("unexpected tools: %+v err=%v", tools, err)
	}
	res, callErr := tools[0].Call(context.Background(), json.RawMessage(`{"msg":"hello"}`), &actool.ToolContext{MaxOutputChars: 1000})
	if callErr != nil || res.IsError || res.Flatten() != "echo:hello" {
		t.Fatalf("unexpected stdio Call result: res=%+v err=%v", res, callErr)
	}

	// Dummy check that permission package import stays valid on tool wrapper.
	if dec := tools[0].CheckPermissions(context.Background(), nil, permission.Context{}); dec.Behavior != permission.Ask {
		t.Fatalf("expected MCP tool permission default Ask, got %+v", dec)
	}
	var _ llm.Message
}
