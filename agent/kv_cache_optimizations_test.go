package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/Norma/llm"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

func TestDeferredSystemWithDynamicPlacesConstraintsAfterStaticPrefix(t *testing.T) {
	def := DeferredInfo{
		AlwaysSkills:    "ALWAYS_SKILLS",
		CapabilityIndex: "CAPABILITY_INDEX",
		GlobalNames:     []string{"extra_tool"},
	}
	sys, boundary := deferredSystemWithDynamic("BASE_SYSTEM", def, "TASK_CONSTRAINTS")
	if len(sys) != 5 {
		t.Fatalf("len(sys)=%d, want 5 (%v)", len(sys), sys)
	}
	if boundary != 4 {
		t.Fatalf("boundary=%d, want 4 (static segments only)", boundary)
	}
	if sys[4] != "TASK_CONSTRAINTS" {
		t.Fatalf("last segment=%q, want TASK_CONSTRAINTS after boundary", sys[4])
	}
}

func TestBuildContextIndexUncompactedSlimmingAndClockBucket(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("slim index", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	itID, err := ts.AddNode(db.KindIntent, map[string]any{"summary": "枚举 /api", "secret_note": "BULKY_PAYLOAD_MARKER"}, 5, "running", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, _ := ts.GetNode(itID)

	now := time.Now()
	clock := TaskClock{DeadlineUnix: now.Add(43 * time.Minute).Unix()}

	// Single-intent uncompacted worker: omits bulky Payload/IntentAssets/RunDir AND
	// live sibling-churning queues (open/running intents, findings list, counts),
	// while still bucketing clock.
	_, _ = ts.AddIntent(map[string]any{"summary": "SIBLING_QUEUE_CHURN"}, 4, nil, "planner")
	_, _ = ts.AddNode(db.KindFinding, map[string]any{"summary": "SIBLING_FINDING_CHURN", "vulnclass": "sqli", "severity": "high"}, 0, "confirmed", "worker", nil)
	uncompacted := BuildContextIndex(ContextIndexVars{
		Store:          ts,
		Intent:         intent,
		IntentAssets:   `[{"id":1,"url":"https://asset.example"}]`,
		RunDir:         "/work/tasks/1/i1",
		Slot:           0,
		Clock:          clock,
		Uncompacted:    true,
		StabilizeClock: true,
	})
	if strings.Contains(uncompacted, "BULKY_PAYLOAD_MARKER") || strings.Contains(uncompacted, "asset.example") || strings.Contains(uncompacted, "/work/tasks/1/i1") {
		t.Fatalf("uncompacted single-intent index should omit duplicated Turn 1 blocks:\n%s", uncompacted)
	}
	if strings.Contains(uncompacted, "SIBLING_QUEUE_CHURN") || strings.Contains(uncompacted, "SIBLING_FINDING_CHURN") || strings.Contains(uncompacted, "计数：") {
		t.Fatalf("uncompacted worker index must omit sibling-churning intents/findings/stats:\n%s", uncompacted)
	}
	if !strings.Contains(uncompacted, "剩余约 40 分钟") {
		t.Fatalf("expected 5-minute clock bucket (40m), got:\n%s", uncompacted)
	}

	// Compacted recovery: includes full IntentAssets, Payload, RunDir, plus live graph sections.
	compacted := BuildContextIndex(ContextIndexVars{
		Store:          ts,
		Intent:         intent,
		IntentAssets:   `[{"id":1,"url":"https://asset.example"}]`,
		RunDir:         "/work/tasks/1/i1",
		Slot:           0,
		Clock:          clock,
		Uncompacted:    false,
		StabilizeClock: true,
	})
	if !strings.Contains(compacted, "BULKY_PAYLOAD_MARKER") || !strings.Contains(compacted, "asset.example") || !strings.Contains(compacted, "/work/tasks/1/i1") {
		t.Fatalf("compacted recovery index must restore full blocks:\n%s", compacted)
	}
	if !strings.Contains(compacted, "SIBLING_QUEUE_CHURN") || !strings.Contains(compacted, "SIBLING_FINDING_CHURN") {
		t.Fatalf("compacted recovery index must restore full graph queues/findings:\n%s", compacted)
	}

	// Uncompacted Planner (HasOverview=true) skips duplicate company/playbook/cve lists.
	plannerSlim := BuildContextIndex(ContextIndexVars{
		Store:          ts,
		HasOverview:    true,
		CompanyLines:   []string{"DUPLICATE_COMPANY_FACT"},
		Clock:          clock,
		Uncompacted:    true,
		StabilizeClock: true,
	})
	if strings.Contains(plannerSlim, "DUPLICATE_COMPANY_FACT") {
		t.Fatalf("uncompacted planner index should omit duplicate overview blocks:\n%s", plannerSlim)
	}
}

func TestInstallContextManagerDynamicForwardsPrefixToSummarizer(t *testing.T) {
	p := &recordingProvider{text: "<summary>PREFIX_KEPT</summary>"}
	opts := &agentcore.Options{
		Provider:             p,
		SystemPrompt:         []string{"STATIC_SYS", "DYNAMIC_SYS"},
		DynamicBoundary:      1,
		Tools:                []actool.CoreTool{actool.DefaultTools()[0]},
		PromptCacheKey:       "sess-compact-key",
		PromptCacheRetention: "24h",
		CacheTTL:             "1h",
		PreserveThinking:     true,
		NonStreaming:         true,
	}
	InstallContextManagerDynamic(opts, 200000, 8000, t.TempDir(), func(compacted bool) string {
		if compacted {
			return "COMPACTED_INDEX"
		}
		return "UNCOMPACTED_INDEX"
	})
	dc, ok := opts.Compactor.(*DomainCompactor)
	if !ok {
		t.Fatalf("expected DomainCompactor, got %T", opts.Compactor)
	}

	// Uncompacted view -> UNCOMPACTED_INDEX (marked CacheSkip ephemeral); compacted view -> COMPACTED_INDEX.
	v1 := dc.View(context.Background(), []llm.Message{llm.UserText("hi")})
	if v1[len(v1)-1].Text() != "UNCOMPACTED_INDEX" {
		t.Fatalf("uncompacted index=%q", v1[len(v1)-1].Text())
	}
	if len(v1[len(v1)-1].Content) == 0 || !v1[len(v1)-1].Content[0].CacheSkip {
		t.Fatalf("expected CacheSkip=true on appended ephemeral index block, got %+v", v1[len(v1)-1])
	}
	dc.index.ttl = 0
	if v2 := dc.View(context.Background(), []llm.Message{llm.BoundaryMessage(llm.BoundaryMeta{Trigger: "auto", PreTokens: 100}), llm.UserText("hi")}); v2[len(v2)-1].Text() != "COMPACTED_INDEX" {
		t.Fatalf("compacted index=%q", v2[len(v2)-1].Text())
	}

	// Trigger Pre auto-compaction -> shares parent System, DynamicBoundary, Tools, PromptCacheKey, CacheTTL, and PromptCacheRetention.
	var msgs []llm.Message
	longTurn := strings.Repeat("findings evidence payload trace ", 20)
	for range 12 {
		msgs = append(msgs, llm.UserText(longTurn))
	}
	if out := dc.Pre(context.Background(), msgs, 900000); len(out) <= len(msgs) {
		t.Fatalf("expected Pre auto-compaction to insert summary boundary (%d -> %d)", len(msgs), len(out))
	}
	req := p.last()
	if len(req.System) != 2 || req.System[0] != "STATIC_SYS" || req.DynamicBoundary != 1 || len(req.Tools) != 1 || req.PromptCacheKey != "sess-compact-key" || req.CacheTTL != "1h" || req.PromptCacheRetention != "24h" {
		t.Fatalf("summary call did not preserve prefix/cache options: %+v", req)
	}
}

func TestWrapupSettlementPreservesToolSchemas(t *testing.T) {
	st := wrapupSettlement("worker", []string{"Bash", "run_execution"})
	if !st.KeepToolSchemas {
		t.Fatal("expected KeepToolSchemas=true on wrapupSettlement")
	}
	var _ *harness.Settlement = st
}

func TestFromEnvCacheDefaultsAndOptOut(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("BLADE_LLM_PROVIDER", "anthropic")
	t.Setenv("BLADE_LLM_CACHE_BREAKPOINTS", "")
	t.Setenv("BLADE_LLM_CACHE_TTL", "1h")
	cfg, ok := FromEnv()
	if !ok {
		t.Fatal("expected FromEnv ok=true")
	}
	if cfg.CacheBreakpoints != "rolling" || cfg.CacheTTL != "1h" {
		t.Fatalf("expected anthropic default CacheBreakpoints=rolling & CacheTTL=1h, got %q / %q", cfg.CacheBreakpoints, cfg.CacheTTL)
	}

	t.Setenv("BLADE_LLM_CACHE_BREAKPOINTS", "system")
	cfg, ok = FromEnv()
	if !ok || cfg.CacheBreakpoints != "" {
		t.Fatalf("expected BLADE_LLM_CACHE_BREAKPOINTS=system opt-out -> \"\", got %q (ok=%t)", cfg.CacheBreakpoints, ok)
	}

	t.Setenv("OPENAI_API_KEY", "sk-oai-test")
	t.Setenv("BLADE_LLM_PROVIDER", "openai")
	t.Setenv("BLADE_LLM_PROMPT_CACHE_RETENTION", "24h")
	cfg, ok = FromEnv()
	if !ok || cfg.PromptCacheRetention != "24h" {
		t.Fatalf("expected openai PromptCacheRetention=24h, got %q (ok=%t)", cfg.PromptCacheRetention, ok)
	}
}

func TestSystemPartsSplitPerTaskFieldsAfterBoundary(t *testing.T) {
	prev := PromptOverride
	PromptOverride = nil
	t.Cleanup(func() { PromptOverride = prev })

	def := DeferredInfo{AlwaysSkills: "ALWAYS"}
	s1, d1 := plannerSystemParts("Goal Alpha", "/data", "/data/tasks/1")
	s2, d2 := plannerSystemParts("Goal Beta", "/data", "/data/tasks/2")
	if s1 != s2 || strings.Contains(s1, "Goal Alpha") || strings.Contains(s1, "/data/tasks/1") {
		t.Fatalf("planner staticPart must be identical across tasks and free of goal/taskDir")
	}
	if !strings.Contains(d1, "Goal Alpha") || !strings.Contains(d1, "/data/tasks/1") {
		t.Fatalf("planner dynamicPart missing per-task goal/taskDir: %q", d1)
	}
	sys1, b1 := deferredSystemWithDynamic(s1, def, d1, "CONSTRAINT_A")
	sys2, b2 := deferredSystemWithDynamic(s2, def, d2, "CONSTRAINT_B")
	if b1 != b2 || sys1[0] != sys2[0] || !strings.Contains(sys1[b1], "Goal Alpha") {
		t.Fatalf("deferredSystemWithDynamic failed to align static prefix before boundary (%d vs %d)", b1, b2)
	}

	// 3.A: Custom PromptOverride with standard "任务目标：{{.Goal}}" still splits static body before boundary.
	PromptOverride = func(k string) (string, bool) {
		if k == "planner" {
			return "定制共享规则主体\n任务目标：{{.Goal}}\n后续规则", true
		}
		return "", false
	}
	ps1, pd1 := plannerSystemParts("Goal One", "/data", "/data/tasks/11")
	ps2, pd2 := plannerSystemParts("Goal Two", "/data", "/data/tasks/12")
	if ps1 != ps2 || !strings.Contains(ps1, "定制共享规则主体") || strings.Contains(ps1, "Goal One") {
		t.Fatalf("custom planner prompt static body should match across tasks before boundary: ps1=%q", ps1)
	}
	if !strings.Contains(pd1, "Goal One") || !strings.Contains(pd2, "Goal Two") {
		t.Fatalf("custom planner prompt goal should remain in dynamicPart: pd1=%q pd2=%q", pd1, pd2)
	}
	PromptOverride = nil

	ms1, md1 := mainAgentSystemParts("MainGoal A", "/data", "/data/tasks/10")
	ms2, md2 := mainAgentSystemParts("MainGoal B", "/data", "/data/tasks/20")
	if ms1 != ms2 || !strings.Contains(md1, "MainGoal A") || !strings.Contains(md1, "/data/tasks/10") || !strings.Contains(md2, "MainGoal B") || !strings.Contains(md2, "/data/tasks/20") {
		t.Fatalf("mainAgent staticPart must match across tasks while dynamicPart carries goal/workDir")
	}

	cs1, cd1 := chatSystemParts("chat", "/data", "/data/sessions/conv-1")
	cs2, cd2 := chatSystemParts("chat", "/data", "/data/sessions/conv-2")
	if cs1 != cs2 || !strings.Contains(cd1, "/data/sessions/conv-1") || !strings.Contains(cd2, "/data/sessions/conv-2") {
		t.Fatalf("chat staticPart must match across sessions while dynamicPart isolates sessionWorkDir")
	}
}

func TestWorkerSharedCacheKeyAndFrozenOverviewSnapshot(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("worker overview wave", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	i1, _ := ts.AddIntent(map[string]any{"summary": "intent 1"}, 5, nil, "planner")
	i2, _ := ts.AddIntent(map[string]any{"summary": "intent 2"}, 5, nil, "planner")

	wantKey := fmt.Sprintf("exp%d-worker", task.ExplorationID)
	if got := WorkerPromptCacheKey(task.ExplorationID); got != wantKey {
		t.Fatalf("WorkerPromptCacheKey=%q, want %q", got, wantKey)
	}

	w := NewWorker(nil, "m", t.TempDir(), nil, 0, 1)
	var calls atomic.Int32
	build := func() string {
		n := calls.Add(1)
		return strings.Repeat("WAVE_SNAPSHOT_", 1) + string(rune('0'+n))
	}
	snap1 := w.cachedWorkerOverview(ts, true, build)
	// Claiming intents changes intent states, which must NOT bust the wave overview snapshot.
	_, _ = ts.ClaimNode(context.Background(), i1, "worker#1", "open", db.IntentLeaseTTL, nil, false)
	_, _ = ts.ClaimNode(context.Background(), i2, "worker#2", "open", db.IntentLeaseTTL, nil, false)
	snap2 := w.cachedWorkerOverview(ts, true, build)
	if snap1 != snap2 || calls.Load() != 1 {
		t.Fatalf("expected concurrent wave to share frozen overview snapshot: snap1=%q snap2=%q calls=%d", snap1, snap2, calls.Load())
	}
	// Writing a concrete fact invalidates the epoch on the next worker wave.
	_, _ = ts.AddFactGraph(map[string]any{"summary": "new observation"}, "worker#1", nil, i1, nil)
	snap3 := w.cachedWorkerOverview(ts, true, build)
	if snap3 == snap1 || calls.Load() != 2 {
		t.Fatalf("expected new fact to refresh overview epoch: snap1=%q snap3=%q calls=%d", snap1, snap3, calls.Load())
	}
}

func TestStabilizeAgeMinutesBucketing(t *testing.T) {
	cases := map[int]int{-3: 0, 0: 0, 4: 0, 5: 5, 9: 5, 10: 10, 44: 40, 45: 45}
	for in, want := range cases {
		if got := stabilizeAgeMinutes(in); got != want {
			t.Fatalf("stabilizeAgeMinutes(%d)=%d, want %d", in, got, want)
		}
	}
}

func TestConfigPreservesThinkingMatrix(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"anthropic disabled overrides preserve", Config{Format: llm.FormatAnthropic, ThinkingType: "disabled", PreserveThinkingBlocks: true}, false},
		{"anthropic enabled", Config{Format: llm.FormatAnthropic, ThinkingType: "enabled"}, true},
		{"anthropic adaptive", Config{Format: llm.FormatAnthropic, ThinkingType: "adaptive"}, true},
		{"anthropic unset preserve=true", Config{Format: llm.FormatAnthropic, PreserveThinkingBlocks: true}, true},
		{"openai disabled overrides effort", Config{Format: llm.FormatOpenAI, ThinkingType: "disabled", ReasoningEffort: "high"}, false},
		{"openai unset with effort", Config{Format: llm.FormatOpenAI, ReasoningEffort: "high"}, true},
		{"responses never replays thinking", Config{Format: llm.FormatOpenAIResponses, ThinkingType: "enabled", ReasoningEffort: "high"}, false},
	}
	for _, tc := range cases {
		if got := tc.cfg.PreservesThinking(); got != tc.want {
			t.Fatalf("%s: PreservesThinking()=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFromEnvExtendedThinkingAndCustomRetention(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant")
	t.Setenv("BLADE_LLM_PROVIDER", "anthropic")
	t.Setenv("BLADE_LLM_THINKING_TYPE", "enabled")
	t.Setenv("BLADE_LLM_THINKING_BUDGET_TOKENS", "4096")
	t.Setenv("BLADE_LLM_REASONING_EFFORT", "high")
	t.Setenv("BLADE_LLM_MAX_TOKENS", "12000")
	t.Setenv("BLADE_LLM_CONTEXT_WINDOW_K", "1500") // clamps to 1000
	t.Setenv("BLADE_LLM_SESSION_HEADER_KEY", "x-session")
	t.Setenv("BLADE_LLM_INPUT_TOKENS_INCLUDES_CACHE", "true")
	t.Setenv("BLADE_LLM_PRESERVE_THINKING_BLOCKS", "1")
	cfg, ok := FromEnv()
	if !ok {
		t.Fatal("FromEnv ok=false")
	}
	if cfg.ThinkingType != "enabled" || cfg.ThinkingBudgetTokens != 4096 || cfg.ReasoningEffort != "high" ||
		cfg.MaxTokens != 12000 || cfg.ContextWindowK != 1000 || cfg.SessionHeaderKey != "x-session" ||
		!cfg.InputTokensIncludesCache || !cfg.PreserveThinkingBlocks {
		t.Fatalf("unexpected FromEnv anthropic cfg: %+v", cfg)
	}

	// Custom OpenAI proxy with explicit PromptCacheRetention must keep PromptCacheRetention wired.
	t.Setenv("OPENAI_API_KEY", "sk-oai")
	t.Setenv("BLADE_LLM_PROVIDER", "openai")
	t.Setenv("BLADE_LLM_BASE_URL", "https://custom-proxy.example.com/v1")
	t.Setenv("BLADE_LLM_MAX_TOKENS_FIELD", "max_completion_tokens")
	t.Setenv("BLADE_LLM_PROMPT_CACHE_RETENTION", "24h")
	oaiCfg, ok := FromEnv()
	if !ok || oaiCfg.MaxTokensField != "max_completion_tokens" || oaiCfg.PromptCacheRetention != "24h" {
		t.Fatalf("unexpected FromEnv openai cfg: %+v (ok=%v)", oaiCfg, ok)
	}
	prov, err := oaiCfg.NewProvider()
	if err != nil || prov == nil {
		t.Fatalf("NewProvider: %v", err)
	}
}
