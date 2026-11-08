package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lin9527ya/Norma/llm"
)

func TestBLADEProviderCacheCompatibility(t *testing.T) {
	cases := []struct {
		name   string
		format llm.Format
		usage  string
		want   llm.Usage
	}{
		{"openai native", llm.FormatOpenAI, `{"prompt_tokens":100,"completion_tokens":8,"prompt_cache_hit_tokens":80}`, llm.Usage{InputTokens: 100, OutputTokens: 8, CacheReadTokens: 80}},
		{"openai gateway", llm.FormatOpenAI, `{"prompt_tokens":100,"completion_tokens":8,"opencode":{"tokens":{"cache_read":80}}}`, llm.Usage{InputTokens: 100, OutputTokens: 8, CacheReadTokens: 80}},
		{"openai canonical zero", llm.FormatOpenAI, `{"prompt_tokens":100,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":80}`, llm.Usage{InputTokens: 100, OutputTokens: 8}},
		{"anthropic gateway", llm.FormatAnthropic, `{"input_tokens":20,"output_tokens":8,"opencode":{"tokens":{"cache_read":80}}}`, llm.Usage{InputTokens: 100, OutputTokens: 8, CacheReadTokens: 80}},
		{"anthropic canonical zero", llm.FormatAnthropic, `{"input_tokens":20,"output_tokens":8,"cache_read_input_tokens":0,"opencode":{"tokens":{"cache_read":80}}}`, llm.Usage{InputTokens: 20, OutputTokens: 8}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "bad fixture", 400)
					return
				}
				if req.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					if c.format == llm.FormatOpenAI {
						fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}],\"usage\":%s}\n\n", c.usage)
						fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":8}}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":20}}}\n\n")
						fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"usage\":%s}\n\n", c.usage)
						fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":8}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
					}
				} else {
					w.Header().Set("Content-Type", "application/json")
					if c.format == llm.FormatOpenAI {
						fmt.Fprintf(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":%s}`, c.usage)
					} else {
						fmt.Fprintf(w, `{"type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":%s}`, c.usage)
					}
				}
			}))
			defer srv.Close()
			p, err := (Config{Format: c.format, BaseURL: srv.URL, Model: "fake"}).NewProvider()
			if err != nil {
				t.Fatal(err)
			}
			req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("fixture")}, MaxTokens: 8}
			acc := llm.NewAccumulator()
			for ev, err := range p.Stream(context.Background(), req) {
				if err != nil {
					t.Fatal(err)
				}
				acc.Add(ev)
			}
			if acc.Usage != c.want {
				t.Fatalf("stream=%+v want=%+v", acc.Usage, c.want)
			}
			_, _, usage, err := p.Complete(context.Background(), req)
			if err != nil || usage != c.want {
				t.Fatalf("complete=%+v want=%+v err=%v", usage, c.want, err)
			}
		})
	}
}
