package guard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/hook"
	actool "github.com/lin9527ya/Norma/tool"
)

func TestUnwrapAndWrapDeferredEventRoundTrip(t *testing.T) {
	ev := hook.Event{
		ToolName: actool.ExecuteExtraToolName,
		Input:    []byte(`{"tool_name":"  mcp__browser__navigate ","params":{"url":"https://example.com"}}`),
	}
	inner, ok := unwrapDeferredEvent(ev)
	if !ok {
		t.Fatal("expected ExecuteExtraTool to unwrap")
	}
	if inner.ToolName != "mcp__browser__navigate" {
		t.Fatalf("inner tool = %q, want mcp__browser__navigate", inner.ToolName)
	}
	if string(inner.Input) != `{"url":"https://example.com"}` {
		t.Fatalf("inner input = %s", inner.Input)
	}

	wrapped := wrapDeferredUpdatedInput(inner.ToolName, []byte(`{"url":"https://narrowed.example.com"}`))
	var got struct {
		ToolName string          `json:"tool_name"`
		Params   json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(wrapped, &got); err != nil {
		t.Fatalf("unmarshal wrapped: %v", err)
	}
	if got.ToolName != "mcp__browser__navigate" || !strings.Contains(string(got.Params), "narrowed.example.com") {
		t.Fatalf("unexpected wrapped payload: %s", wrapped)
	}

	// Empty params must normalize to {} instead of empty slice.
	emptyInner, ok := unwrapDeferredEvent(hook.Event{
		ToolName: actool.ExecuteExtraToolName,
		Input:    []byte(`{"tool_name":"mcp__browser__list_tabs"}`),
	})
	if !ok || string(emptyInner.Input) != `{}` {
		t.Fatalf("empty params unwrap = (%v, %q)", ok, emptyInner.Input)
	}
}

func TestPreToolUseUnwrapsExecuteExtraToolForProxyBypassAndAudit(t *testing.T) {
	g := newGuard(nil, 0)
	// Deferred proxy-bypass command wrapped in ExecuteExtraTool must still be blocked
	// and audited under the real inner tool name.
	in, _ := json.Marshal(map[string]any{
		"tool_name": "run_execution",
		"params":    map[string]any{"command": "curl --noproxy '*' https://target.example/admin"},
	})
	res := g.preToolUse(context.Background(), hook.Event{
		ToolName: actool.ExecuteExtraToolName,
		Input:    in,
	})
	if res.Decision != "block" {
		t.Fatalf("expected deferred proxy-bypass to block, got %+v", res)
	}
	audit := g.Audit()
	if len(audit) != 1 || audit[0].Tool != "run_execution" || audit[0].Action != "block" {
		t.Fatalf("expected inner tool audit entry, got %+v", audit)
	}
}

func TestPostToolUseUnwrapsExecuteExtraToolAndSkipsArbitraryMCPText(t *testing.T) {
	g := newGuard(nil, 0)
	// Deferred http_request hitting 429 must increment G5 blocked counter.
	g.postToolUse(context.Background(), hook.Event{
		ToolName: actool.ExecuteExtraToolName,
		Input:    []byte(`{"tool_name":"http_request","params":{"url":"https://target.example"}}`),
		Result:   []byte("429 rate limit"),
		IsError:  true,
	})
	// Arbitrary MCP output (e.g. page text mentioning "403 Forbidden") must NOT be misclassified.
	g.postToolUse(context.Background(), hook.Event{
		ToolName: actool.ExecuteExtraToolName,
		Input:    []byte(`{"tool_name":"mcp__browser__snapshot","params":{}}`),
		Result:   []byte("Page body: HTTP 403 Forbidden article"),
	})
	got := g.Attributions()
	if got["blocked"] != 1 || len(got) != 1 {
		t.Fatalf("unexpected attributions: %+v", got)
	}
}
