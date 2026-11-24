package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/db"
)

func TestPGListModelsPreservesCustomHeadersAndRejectsBadProxy(t *testing.T) {
	s := &Server{m: &Manager{}}

	var seenUA, seenCustom, seenAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA = r.Header.Get("User-Agent")
		seenCustom = r.Header.Get("X-Relay-Tag")
		seenAuth = r.Header.Get("Authorization")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}]}`))
	}))
	defer upstream.Close()

	body, _ := json.Marshal(map[string]any{
		"provider":       "openai",
		"base_url":       upstream.URL,
		"api_key":        "sk-test-models",
		"custom_headers": map[string]string{"User-Agent": "claude-cli/2.1.0", "X-Relay-Tag": "alpha"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/llm/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.pgListModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pgListModels status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res struct {
		OK     bool     `json:"ok"`
		Models []string `json:"models"`
		Error  string   `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.OK || len(res.Models) != 1 || res.Models[0] != "gpt-5" {
		t.Fatalf("unexpected pgListModels response: %+v", res)
	}
	if seenUA != "claude-cli/2.1.0" || seenCustom != "alpha" || seenAuth != "Bearer sk-test-models" {
		t.Fatalf("headers lost/overwritten: UA=%q custom=%q auth=%q", seenUA, seenCustom, seenAuth)
	}

	// Malformed proxy must fail closed rather than falling back to direct.
	badBody, _ := json.Marshal(map[string]any{
		"provider": "openai",
		"base_url": upstream.URL,
		"api_key":  "sk-test-models",
		"proxy":    "not-a-valid-proxy",
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/llm/models", bytes.NewReader(badBody))
	rec2 := httptest.NewRecorder()
	s.pgListModels(rec2, req2)
	_ = json.Unmarshal(rec2.Body.Bytes(), &res)
	if res.OK || res.Error == "" {
		t.Fatalf("expected invalid proxy error, got %+v", res)
	}
}

func TestTestLLMForwardsMaxTokensFieldAndCustomMaxTokens(t *testing.T) {
	s := &Server{m: &Manager{}}

	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	body, _ := json.Marshal(map[string]any{
		"provider":         "openai",
		"model":            "o3",
		"base_url":         upstream.URL,
		"api_key":          "sk-test",
		"max_tokens":       4096,
		"max_tokens_field": "max_completion_tokens",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/llm/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.testLLM(rec, req)
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.OK {
		t.Fatalf("testLLM failed: %+v", res)
	}
	if gotBody["max_completion_tokens"] != float64(4096) || gotBody["max_tokens"] != nil {
		t.Fatalf("expected max_completion_tokens=4096 without max_tokens, got %v", gotBody)
	}
}

func TestPGSaveAndActivateProfileValidation(t *testing.T) {
	m := openLifecycleTestManager(t)
	s := newAdmissionTestServer(m, nil)

	saveRaw := func(payload map[string]any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.pgSaveProfile(rec, req)
		return rec
	}

	badPayloads := []struct {
		name    string
		payload map[string]any
		want    int
	}{
		{"empty model", map[string]any{"name": "bad-1", "format": "openai", "model": "  "}, 400},
		{"bad format", map[string]any{"name": "bad-2", "format": "gemini", "model": "m"}, 400},
		{"bad proxy", map[string]any{"name": "bad-3", "format": "openai", "model": "m", "proxy": "bad://proxy"}, 400},
		{"cw too large", map[string]any{"name": "bad-4", "format": "openai", "model": "m", "context_window_k": 384000}, 400},
		{"adaptive on openai", map[string]any{"name": "bad-5", "format": "openai", "model": "m", "thinking_type": "adaptive"}, 400},
		{"budget too small", map[string]any{"name": "bad-6", "format": "anthropic", "model": "m", "thinking_type": "enabled", "thinking_budget_tokens": 512}, 400},
		{"max_tokens <= budget", map[string]any{"name": "bad-7", "format": "anthropic", "model": "m", "thinking_type": "enabled", "thinking_budget_tokens": 4096, "max_tokens": 4096}, 400},
		{"bad effort", map[string]any{"name": "bad-8", "format": "openai", "model": "m", "reasoning_effort": "extreme"}, 400},
	}
	for _, tc := range badPayloads {
		rec := saveRaw(tc.payload)
		if rec.Code != tc.want {
			t.Fatalf("%s: status=%d want=%d body=%s", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}

	// Duplicate name -> 409; activate keyless profile -> 400; missing profile -> 404.
	r1 := saveRaw(map[string]any{"name": "t-dup-profile", "format": "openai", "model": "m"})
	if r1.Code != http.StatusOK {
		t.Fatalf("create failed (%d): %s", r1.Code, r1.Body.String())
	}
	var out struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r1.Body.Bytes(), &out)
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, out.ID) })

	if rDup := saveRaw(map[string]any{"name": "t-dup-profile", "format": "openai", "model": "m"}); rDup.Code != http.StatusConflict {
		t.Fatalf("duplicate name status=%d want 409 body=%s", rDup.Code, rDup.Body.String())
	}

	actReq := func(id int64) int {
		b, _ := json.Marshal(map[string]any{"id": id})
		req := httptest.NewRequest(http.MethodPost, "/api/llm/profiles/active", bytes.NewReader(b))
		rec := httptest.NewRecorder()
		s.pgActivateProfile(rec, req)
		return rec.Code
	}
	if code := actReq(out.ID); code != http.StatusBadRequest {
		t.Fatalf("activating keyless profile status=%d want 400", code)
	}
	if code := actReq(99999999); code != http.StatusNotFound {
		t.Fatalf("activating missing profile status=%d want 404", code)
	}

	if err := s.validateCompactorProfile(out.ID); err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Fatalf("expected keyless compactor validation error, got %v", err)
	}
}

func TestCollectOneShotTextHonorsNonStreaming(t *testing.T) {
	p := &nonStreamingAssertProv{}
	out, err := collectOneShotText(context.Background(), p, "sys", "user", 512, true, "1h", "")
	if err != nil || out != "completed" || !p.completeCalled || p.streamCalled {
		t.Fatalf("out=%q err=%v complete=%v stream=%v", out, err, p.completeCalled, p.streamCalled)
	}
	if p.lastReq.CacheTTL != "1h" {
		t.Fatalf("expected CacheTTL forwarded, got %+v", p.lastReq)
	}
	_ = db.LLMProfile{}
}

type nonStreamingAssertProv struct {
	completeCalled bool
	streamCalled   bool
	lastReq        llm.CompletionRequest
}

func (p *nonStreamingAssertProv) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.streamCalled = true
	return func(yield func(llm.StreamEvent, error) bool) {}
}

func (p *nonStreamingAssertProv) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.completeCalled = true
	p.lastReq = req
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("completed")}}, "end_turn", llm.Usage{}, nil
}
