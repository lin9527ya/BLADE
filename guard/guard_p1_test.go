package guard

import (
	"context"
	"testing"

	"github.com/lin9527ya/Norma/hook"
)

func TestRunExecutionUsesCommandAuditSurface(t *testing.T) {
	input := []byte(`{"command":"curl --noproxy '*' https://target.example/admin"}`)
	if got := extractCommandFromToolInput("run_execution", input); got != "curl --noproxy '*' https://target.example/admin" {
		t.Fatalf("run_execution command extraction = %q", got)
	}
	g := newGuard(nil, 0)
	res := g.preToolUse(context.Background(), hook.Event{ToolName: "run_execution", Input: input})
	if res.Decision != "block" {
		t.Fatal("run_execution direct-proxy bypass must not bypass the pre-tool guard")
	}
}

func TestFailureAttributionIncludesHostedAndHTTP(t *testing.T) {
	g := newGuard(nil, 0)
	for _, tool := range []string{"run_execution", "http_request", "session_login"} {
		g.postToolUse(context.Background(), hook.Event{ToolName: tool, Result: []byte("429 rate limit"), IsError: true})
	}
	g.postToolUse(context.Background(), hook.Event{ToolName: "run_execution", Result: []byte("ok")})
	if got := g.Attributions(); got["blocked"] != 3 || got["ok"] != 1 {
		t.Fatalf("attribution = %+v", got)
	}
}
