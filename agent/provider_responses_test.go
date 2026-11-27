package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lin9527ya/Norma/llm"
)

// TestConfigFromOpenAIResponses locks the openai-responses format wiring:
// ConfigFrom resolves the Responses format, strips a full /responses endpoint
// back to the API base, and Provider() round-trips the short name. NewProvider
// must build a working provider for it.
func TestConfigFromOpenAIResponses(t *testing.T) {
	c := ConfigFrom("openai-responses", "gpt-5", "https://gw.example/v1/responses", "sk-x", "")
	if c.Format != llm.FormatOpenAIResponses {
		t.Fatalf("format=%v, want FormatOpenAIResponses", c.Format)
	}
	if c.BaseURL != "https://gw.example/v1" {
		t.Fatalf("base_url=%q, want the /responses suffix stripped", c.BaseURL)
	}
	if c.Provider() != "openai-responses" {
		t.Fatalf("Provider()=%q", c.Provider())
	}
	if !c.Stream { // default streaming preserved
		t.Fatal("Stream should default true")
	}
	if _, err := c.NewProvider(); err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
}

func TestNormalizeBaseURLAndAnthropicV1Tolerance(t *testing.T) {
	cases := []struct {
		provider string
		input    string
		want     string
	}{
		{"anthropic", "", ""},
		{"anthropic", "https://amr-link.open-design.ai", "https://amr-link.open-design.ai"},
		{"anthropic", "https://amr-link.open-design.ai/v1", "https://amr-link.open-design.ai"},
		{"anthropic", "https://amr-link.open-design.ai/v1/", "https://amr-link.open-design.ai"},
		{"anthropic", "https://amr-link.open-design.ai/v1/messages", "https://amr-link.open-design.ai"},
		{"anthropic", "https://api.commandcode.ai/provider/v1", "https://api.commandcode.ai/provider"},
		{"anthropic", "https://api.deepseek.com/anthropic", "https://api.deepseek.com/anthropic"},
		{"anthropic", "https://opencode.ai/zen/go", "https://opencode.ai/zen/go"},
		{"anthropic", "https://example.com/v1beta", "https://example.com/v1beta"},
		{"openai", "https://amr-link.open-design.ai/v1/chat/completions", "https://amr-link.open-design.ai/v1"},
		{"openai-responses", "https://amr-link.open-design.ai/v1/responses/", "https://amr-link.open-design.ai/v1"},
	}
	for _, tc := range cases {
		if got := NormalizeBaseURL(tc.provider, tc.input); got != tc.want {
			t.Fatalf("NormalizeBaseURL(%q, %q)=%q, want %q", tc.provider, tc.input, got, tc.want)
		}
	}

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	cfg := ConfigFrom("anthropic", "claude-opus-4-8", srv.URL+"/v1", "sk-ant-test", "")
	prov, err := cfg.NewProvider()
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	_, _, _, err = prov.Complete(context.Background(), llm.CompletionRequest{
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("hi")}}},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("anthropic request path=%q, want /v1/messages (must not double /v1)", gotPath)
	}

	t.Setenv("BLADE_LLM_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	t.Setenv("BLADE_LLM_BASE_URL", "https://amr-link.open-design.ai/v1")
	envCfg, ok := FromEnv()
	if !ok || envCfg.BaseURL != "https://amr-link.open-design.ai" {
		t.Fatalf("FromEnv BaseURL=%q ok=%v, want https://amr-link.open-design.ai", envCfg.BaseURL, ok)
	}
}
