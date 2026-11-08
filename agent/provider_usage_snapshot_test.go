package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lin9527ya/Norma/llm"
)

// 通过 BLADE 的完整 provider 构造路径验证 SDK 迟报缓存合并，而不请求真实模型。
func TestAnthropicLateCacheThroughBLADEProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":20,\"cache_read_input_tokens\":0}}}\n\n"+
			"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n"+
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":20,\"output_tokens\":8,\"cache_read_input_tokens\":80}}\n\n"+
			"data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	prov, err := (Config{Format: llm.FormatAnthropic, BaseURL: srv.URL, Model: "fake"}).NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	acc := llm.NewAccumulator()
	for ev, err := range prov.Stream(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("probe")}, MaxTokens: 8}) {
		if err != nil {
			t.Fatal(err)
		}
		acc.Add(ev)
	}
	want := llm.Usage{InputTokens: 100, OutputTokens: 8, CacheReadTokens: 80}
	if acc.Usage != want {
		t.Fatalf("usage=%+v want %+v", acc.Usage, want)
	}
}
