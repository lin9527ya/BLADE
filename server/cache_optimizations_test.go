package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

func TestPGSaveProfileCacheDefaultsAndOptOut(t *testing.T) {
	m := openLifecycleTestManager(t)
	s := newAdmissionTestServer(m, nil)
	s.providers.chatAgents = map[int64]*agent.ChatAgent{}
	s.providers.byProfile = map[int64]*provEntry{}
	s.agents.bundles = map[string]*taskAgentBundle{}

	save := func(payload map[string]any) int64 {
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.pgSaveProfile(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("pgSaveProfile failed (%d): %s", rec.Code, rec.Body.String())
		}
		var out struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ID <= 0 {
			t.Fatalf("invalid save response: %s (%v)", rec.Body.String(), err)
		}
		return out.ID
	}

	// 1. Anthropic with omitted cache_breakpoints defaults to "rolling" + keeps cache_ttl="1h".
	anthID := save(map[string]any{
		"name":       "t-anth-default-rolling",
		"format":     "anthropic",
		"model":      "claude-sonnet-4-5",
		"api_key":    "sk-ant-test",
		"cache_ttl":  "1h",
		"streaming":  true,
		"max_tokens": 4096,
	})
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, anthID) })

	p, err := m.pg.ProfileByID(anthID)
	if err != nil || p == nil {
		t.Fatalf("ProfileByID(%d): %v", anthID, err)
	}
	if p.CacheBreakpoints != "rolling" || p.CacheTTL != "1h" || p.PromptCacheRetention != "" {
		t.Fatalf("unexpected persisted anthropic profile cache fields: %+v", p)
	}
	if cfg := s.profileToAgentConfig(p); cfg.CacheBreakpoints != "rolling" || cfg.CacheTTL != "1h" {
		t.Fatalf("unexpected agent config: %+v", cfg)
	}

	// 2. Explicit opt-out cache_breakpoints="system" persists "system" and maps to "" for Norma SDK.
	_ = save(map[string]any{
		"id":                anthID,
		"name":              "t-anth-default-rolling",
		"format":            "anthropic",
		"model":             "claude-sonnet-4-5",
		"cache_breakpoints": "system",
		"cache_ttl":         "5m",
	})
	optOut, err := m.pg.ProfileByID(anthID)
	if err != nil || optOut == nil {
		t.Fatalf("ProfileByID(%d) after opt-out: %v", anthID, err)
	}
	if optOut.CacheBreakpoints != "system" || optOut.CacheTTL != "5m" {
		t.Fatalf("expected persisted opt-out system / 5m, got %q / %q", optOut.CacheBreakpoints, optOut.CacheTTL)
	}
	if cfg := s.profileToAgentConfig(optOut); cfg.CacheBreakpoints != "" || cfg.CacheTTL != "5m" {
		t.Fatalf("expected system opt-out to map to SDK CacheBreakpoints=\"\", got %q", cfg.CacheBreakpoints)
	}

	// 3. OpenAI profile clears Anthropic-only knobs and persists prompt_cache_retention="24h".
	oaiID := save(map[string]any{
		"name":                   "t-oai-retention",
		"format":                 "openai",
		"model":                  "gpt-5",
		"api_key":                "sk-oai-test",
		"cache_breakpoints":      "rolling",
		"cache_ttl":              "1h",
		"prompt_cache_retention": "24h",
	})
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, oaiID) })

	oai, err := m.pg.ProfileByID(oaiID)
	if err != nil || oai == nil {
		t.Fatalf("ProfileByID(%d): %v", oaiID, err)
	}
	if oai.CacheBreakpoints != "" || oai.CacheTTL != "" || oai.PromptCacheRetention != "24h" {
		t.Fatalf("unexpected openai persisted cache fields: %+v", oai)
	}
	if cfg := s.profileToAgentConfig(oai); cfg.Format != llm.FormatOpenAI || cfg.PromptCacheRetention != "24h" {
		t.Fatalf("unexpected openai agent config: %+v", cfg)
	}

	// Legacy empty Anthropic row in DB also resolves to "rolling".
	legacy := &db.LLMProfile{Format: "anthropic", Model: "claude-sonnet-4-5", CacheBreakpoints: ""}
	if cfg := s.profileToAgentConfig(legacy); cfg.CacheBreakpoints != "rolling" {
		t.Fatalf("legacy empty anthropic profile must default to rolling, got %q", cfg.CacheBreakpoints)
	}
}

func TestPlannerCacheSafeTimer(t *testing.T) {
	if got := plannerCacheSafeTimer(300 * time.Second); got != 255*time.Second {
		t.Fatalf("plannerCacheSafeTimer(300s) = %v, want 255s", got)
	}
	if got := plannerCacheSafeTimer(600 * time.Second); got != 600*time.Second {
		t.Fatalf("plannerCacheSafeTimer(600s) = %v, want 600s", got)
	}
}
