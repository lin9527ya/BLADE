package db

import (
	"context"
	"strconv"
	"testing"
)

// TestProfileMaxTokensRoundTrip pins the output-cap columns through the whole
// read/write surface: both column lists (list vs keyed loads) must carry them,
// and an update must not drop them. A miscounted column list silently shifts
// every later Scan target, so this is the test that catches it.
func TestProfileMaxTokensRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// Close via Cleanup, registered FIRST so it runs LAST: cleanups are LIFO, and a
	// plain `t.Cleanup(func() { d.Close() })` would fire before them — the row-deleting cleanups
	// would then run against a closed pool and silently leave test rows behind.
	t.Cleanup(func() { d.Close() })

	id, err := d.SaveProfile(&LLMProfile{
		Name: "t-maxtok", Format: "openai", Model: "m", APIKey: "k",
		ThinkingType: "enabled", ThinkingBudgetTokens: 2048, ReasoningEffort: "high",
		MaxTokens: 8192, MaxTokensField: "max_completion_tokens",
		InputTokensIncludesCache: true, PreserveThinkingBlocks: true, CacheBreakpoints: "rolling",
		CacheTTL: "1h", PromptCacheRetention: "24h",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })

	// Keyed single-row load (profileColsKey).
	p, err := d.ProfileByID(id)
	if err != nil || p == nil {
		t.Fatalf("ProfileByID: %v, p=%v", err, p)
	}
	if p.MaxTokens != 8192 || p.MaxTokensField != "max_completion_tokens" ||
		p.ThinkingType != "enabled" || p.ThinkingBudgetTokens != 2048 || p.ReasoningEffort != "high" ||
		!p.InputTokensIncludesCache || !p.PreserveThinkingBlocks || p.CacheBreakpoints != "rolling" ||
		p.CacheTTL != "1h" || p.PromptCacheRetention != "24h" {
		t.Fatalf("keyed load mismatch: %+v", p)
	}

	// List load (profileCols — the hint variant, a separate column list).
	ps, err := d.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	var found *LLMProfile
	for _, x := range ps {
		if x.ID == id {
			found = x
		}
	}
	if found == nil {
		t.Fatal("profile missing from ListProfiles")
	}
	if found.MaxTokens != 8192 || found.MaxTokensField != "max_completion_tokens" ||
		found.ThinkingType != "enabled" || found.ThinkingBudgetTokens != 2048 || found.ReasoningEffort != "high" ||
		!found.InputTokensIncludesCache || !found.PreserveThinkingBlocks || found.CacheBreakpoints != "rolling" ||
		found.CacheTTL != "1h" || found.PromptCacheRetention != "24h" {
		t.Fatalf("list load mismatch: %+v", found)
	}

	// Update with a blank key takes the "keep existing key" UPDATE branch, which
	// has its own column list and is the easiest one to forget.
	p.APIKey = ""
	p.ThinkingType = "adaptive"
	p.ThinkingBudgetTokens = 0
	p.ReasoningEffort = "max"
	p.MaxTokens = 4096
	p.MaxTokensField = ""
	p.InputTokensIncludesCache = false
	p.PreserveThinkingBlocks = false
	p.CacheBreakpoints = "system"
	p.CacheTTL = "5m"
	p.PromptCacheRetention = "in_memory"
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	after, err := d.ProfileByID(id)
	if err != nil || after == nil {
		t.Fatalf("reload: %v", err)
	}
	if after.MaxTokens != 4096 || after.MaxTokensField != "" ||
		after.ThinkingType != "adaptive" || after.ThinkingBudgetTokens != 0 || after.ReasoningEffort != "max" ||
		after.InputTokensIncludesCache || after.PreserveThinkingBlocks || after.CacheBreakpoints != "system" ||
		after.CacheTTL != "5m" || after.PromptCacheRetention != "in_memory" {
		t.Fatalf("after update mismatch: %+v", after)
	}
	if after.APIKey != "k" {
		t.Fatalf("blank key on update must keep the stored one, got %q", after.APIKey)
	}
}

// A profile saved without touching the new fields must read back as "no cap,
// classic field name" — the pre-feature behaviour old rows also get.
func TestProfileMaxTokensDefaults(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// Close via Cleanup, registered FIRST so it runs LAST: cleanups are LIFO, and a
	// plain `t.Cleanup(func() { d.Close() })` would fire before them — the row-deleting cleanups
	// would then run against a closed pool and silently leave test rows behind.
	t.Cleanup(func() { d.Close() })

	id, err := d.SaveProfile(&LLMProfile{Name: "t-maxtok-default", Format: "openai", Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })

	p, err := d.ProfileByID(id)
	if err != nil || p == nil {
		t.Fatalf("ProfileByID: %v", err)
	}
	if p.MaxTokens != 0 || p.MaxTokensField != "" {
		t.Fatalf("defaults: max_tokens=%d field=%q, want 0 and \"\"", p.MaxTokens, p.MaxTokensField)
	}
}

func TestDeleteProfileClearsCompactorSetting(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	id, err := d.SaveProfile(&LLMProfile{Name: "t-comp-orphan", Format: "openai", Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id)
		_, _ = d.Exec(`DELETE FROM settings WHERE key='compactor_llm_profile_id' AND BTRIM(value)=$1`, strconv.FormatInt(id, 10))
	})
	if err := d.SetSetting("compactor_llm_profile_id", strconv.FormatInt(id, 10)); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteProfileContext(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := d.GetSetting("compactor_llm_profile_id"); ok {
		t.Fatalf("expected compactor_llm_profile_id deleted with profile, got %q", v)
	}
}
