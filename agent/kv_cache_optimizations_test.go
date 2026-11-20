package agent

import (
	"context"
	"strings"
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

	// Single-intent uncompacted worker: omits bulky Payload/IntentAssets/RunDir & buckets clock.
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
	if !strings.Contains(uncompacted, "剩余约 40 分钟") {
		t.Fatalf("expected 5-minute clock bucket (40m), got:\n%s", uncompacted)
	}

	// Compacted recovery: includes full IntentAssets, Payload, RunDir.
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
		Provider:        p,
		SystemPrompt:    []string{"STATIC_SYS", "DYNAMIC_SYS"},
		DynamicBoundary: 1,
		Tools:           []actool.CoreTool{actool.DefaultTools()[0]},
		PromptCacheKey:  "sess-compact-key",
		NonStreaming:    true,
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

	// Uncompacted view -> UNCOMPACTED_INDEX; compacted view -> COMPACTED_INDEX.
	if v1 := dc.View(context.Background(), []llm.Message{llm.UserText("hi")}); v1[len(v1)-1].Text() != "UNCOMPACTED_INDEX" {
		t.Fatalf("uncompacted index=%q", v1[len(v1)-1].Text())
	}
	dc.index.ttl = 0
	if v2 := dc.View(context.Background(), []llm.Message{llm.BoundaryMessage(llm.BoundaryMeta{Trigger: "auto", PreTokens: 100}), llm.UserText("hi")}); v2[len(v2)-1].Text() != "COMPACTED_INDEX" {
		t.Fatalf("compacted index=%q", v2[len(v2)-1].Text())
	}

	// Trigger Pre auto-compaction -> shares parent System, DynamicBoundary, Tools, and PromptCacheKey.
	var msgs []llm.Message
	longTurn := strings.Repeat("findings evidence payload trace ", 20)
	for range 12 {
		msgs = append(msgs, llm.UserText(longTurn))
	}
	if out := dc.Pre(context.Background(), msgs, 900000); len(out) <= len(msgs) {
		t.Fatalf("expected Pre auto-compaction to insert summary boundary (%d -> %d)", len(msgs), len(out))
	}
	req := p.last()
	if len(req.System) != 2 || req.System[0] != "STATIC_SYS" || req.DynamicBoundary != 1 || len(req.Tools) != 1 || req.PromptCacheKey != "sess-compact-key" {
		t.Fatalf("summary call did not preserve prefix: %+v", req)
	}
}

func TestWrapupSettlementPreservesToolSchemas(t *testing.T) {
	st := wrapupSettlement("worker", []string{"Bash", "run_execution"})
	if !st.KeepToolSchemas {
		t.Fatal("expected KeepToolSchemas=true on wrapupSettlement")
	}
	var _ *harness.Settlement = st
}
