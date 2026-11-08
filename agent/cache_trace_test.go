package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
)

func TestCacheSignatureStablePrefixAndChangingTail(t *testing.T) {
	a, err := requestCacheSignature(`{"model":"m","system":[{"type":"text","text":"SYSTEM_SECRET","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"Bash","input_schema":{}}],"messages":[{"role":"user","content":"TARGET_SECRET"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	b, err := requestCacheSignature(`{"tools":[{"input_schema":{},"name":"Bash"}],"model":"m","system":[{"cache_control":{"type":"ephemeral"},"text":"SYSTEM_SECRET","type":"text"}],"messages":[{"content":"changed","role":"user"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if a.System != b.System || a.Tools != b.Tools || a.Messages[0] == b.Messages[0] {
		t.Fatalf("section signature not isolated: %+v / %+v", a, b)
	}
	if len(a.Breakpoints) != 1 || a.Breakpoints[0] != "system[0]" {
		t.Fatalf("cache marker: %v", a.Breakpoints)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "SYSTEM_SECRET") || strings.Contains(string(raw), "TARGET_SECRET") {
		t.Fatal("diagnostic leaks request content")
	}
}

func TestCacheTraceUsageFieldsNoContent(t *testing.T) {
	start := traceUsageFields([]byte(`{"type":"message_start","message":{"content":[{"text":"SECRET"}],"usage":{"input_tokens":12,"cache_read_input_tokens":0}}}`))
	delta := traceUsageFields([]byte(`{"type":"message_delta","usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":100,"credential":"SECRET"}}`))
	if start["input_tokens"] == nil || delta["cache_read_input_tokens"] == nil {
		t.Fatalf("late cache field lost: %v / %v", start, delta)
	}
	raw, _ := json.Marshal(delta)
	if strings.Contains(string(raw), "SECRET") || len(delta) != 3 {
		t.Fatalf("usage whitelist: %s", raw)
	}
	if fields := traceUsageFields([]byte(`{"type":"content_block_delta","delta":{"text":"SECRET"}}`)); fields != nil {
		t.Fatalf("text should be ignored: %v", fields)
	}
}

func TestCacheTraceBodyPreservesStreamAndIsBounded(t *testing.T) {
	raw := "data: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":12,\"output_tokens\":7}}\n\n" + strings.Repeat("x", cacheTraceLineLimit+1) + "\n"
	b := &cacheTraceBody{ReadCloser: io.NopCloser(strings.NewReader(raw))}
	var out strings.Builder
	if _, err := io.Copy(&out, b); err != nil {
		t.Fatal(err)
	}
	if out.String() != raw {
		t.Fatal("diagnostic altered stream")
	}
	if len(b.buffer) != 0 || !b.finished {
		t.Fatal("diagnostic retained response buffer")
	}
	_ = b.Close()
}

func TestCacheTraceDefaultOffAndSessionScoped(t *testing.T) {
	t.Setenv(CacheTraceEnv, "")
	req, _ := http.NewRequest("POST", "https://example.invalid/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.GetBody = func() (io.ReadCloser, error) { t.Fatal("disabled diagnostics read request body"); return nil, nil }
	if traceModelRequest(req) != 0 {
		t.Fatal("diagnostics should default off")
	}
	t.Setenv(CacheTraceEnv, "1")
	raw := `{"model":"m","system":[{"type":"text","text":"stable"}],"messages":[]}`
	req, _ = http.NewRequest("POST", "https://example.invalid/v1/messages", strings.NewReader(raw))
	req = req.WithContext(transcript.WithSessionID(context.Background(), "diagnostic-test"))
	if traceModelRequest(req) == 0 {
		t.Fatal("enabled diagnostics not captured")
	}
}

// 同一 fake 网关连续请求：会话头与 system 缓存断点都必须原样经过 BLADE provider。
func TestCacheTraceProviderPreservesBoundaryAndUsage(t *testing.T) {
	t.Setenv(CacheTraceEnv, "1")
	var signatures []cacheSignature
	var sessions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		signature, err := requestCacheSignature(string(raw))
		if err != nil {
			t.Error(err)
			return
		}
		signatures = append(signatures, signature)
		sessions = append(sessions, r.Header.Get("x-opencode-session"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n"+
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":8,\"output_tokens\":5,\"cache_read_input_tokens\":120}}\n\n"+
			"data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	prov, err := (Config{Format: llm.FormatAnthropic, BaseURL: srv.URL, Model: "fake", SessionHeaderKey: "x-opencode-session"}).NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	ctx := transcript.WithSessionID(context.Background(), "exp-offline-planner")
	req := llm.CompletionRequest{System: []string{"stable prefix"}, DynamicBoundary: 1,
		Tools:    []llm.ToolSchema{{Name: "read_fact", Description: "read only", InputSchema: map[string]any{"type": "object"}}},
		Messages: []llm.Message{llm.UserText("initial")}, MaxTokens: 8}
	for round := 0; round < 2; round++ {
		acc := llm.NewAccumulator()
		for event, err := range prov.Stream(ctx, req) {
			if err != nil {
				t.Fatal(err)
			}
			acc.Add(event)
		}
		if acc.Usage.InputTokens != 128 || acc.Usage.CacheReadTokens != 120 || acc.Usage.OutputTokens != 5 {
			t.Fatalf("usage lost through provider: %+v", acc.Usage)
		}
		req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("last reply")}}, llm.UserText("new tail"))
	}
	if len(signatures) != 2 || signatures[0].System != signatures[1].System || signatures[0].Tools != signatures[1].Tools {
		t.Fatal("stable prefix changed between requests")
	}
	if len(signatures[0].Breakpoints) != 1 || signatures[0].Breakpoints[0] != "system[0]" {
		t.Fatal("cache breakpoint was lost")
	}
	for _, session := range sessions {
		if session != "exp-offline-planner" {
			t.Fatalf("session header lost: %q", session)
		}
	}
}

func TestCacheTraceGatewayUsageCanExposeTopLevelMismatch(t *testing.T) {
	fields := traceUsageFields([]byte(`{"type":"message_delta","usage":{"input_tokens":20,"cache_read_input_tokens":0,"opencode":{"tokens":{"input":20,"cache_read":80,"credential":"SECRET"}}}}`))
	if fields["opencode.tokens.cache_read"] == nil || fields["cache_read_input_tokens"] == nil {
		t.Fatalf("raw field discrepancy lost: %v", fields)
	}
	raw, _ := json.Marshal(fields)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatal("gateway metadata leaked")
	}
}
