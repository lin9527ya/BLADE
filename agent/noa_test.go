package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/compaction"
	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/noa"
	actool "github.com/lin9527ya/Norma/tool"
)

func noaPressureFixture() []llm.Message {
	body := strings.Repeat("compressible tool output. ", 450)
	msgs := []llm.Message{llm.UserText("initial task definition")}
	for i := range 6 {
		id := "t" + string(rune('a'+i))
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: id, Name: "Bash", Input: json.RawMessage(`{}`)},
		}})
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{{
			Type: llm.BlockToolResult, ToolUseID: id,
			Content: []llm.ContentBlock{llm.TextBlock(body)},
		}}})
	}
	tail := strings.Repeat("recent. ", 300)
	for range 6 {
		msgs = append(msgs, llm.UserText(tail))
	}
	return msgs
}

// TestEnableNoaPreservesDynamicBoundaryAndReportsCompacted verifies that:
//  1. noa's static prompt sections are spliced BEFORE the host's DynamicBoundary
//     rather than overturning DynamicBoundary and freezing dynamic tails in cache.
//  2. ModelContextLimit flows into noa (a 20K limit injects pressure nudge on a
//     compressible backlog whereas Norma's 200K fallback stays silent).
//  3. indexView sees compacted=true once noa has active blocks (it does not stay
//     pinned on Uncompacted=true simply because noa writes no Boundary block).
func TestEnableNoaPreservesDynamicBoundaryAndReportsCompacted(t *testing.T) {
	workDir := t.TempDir()
	const sessionID = "exp7-planner"

	opts := &agentcore.Options{
		SystemPrompt:    []string{"static-core", "dynamic-goal"},
		DynamicBoundary: 1,
		Compaction:      &compaction.Config{ContextWindow: 32000},
	}
	st := enableNoa(opts, func() bool { return true }, workDir, sessionID, 20000, nil)
	if !st.enabled {
		t.Fatal("expected noa state to be enabled")
	}
	if opts.Compaction != nil {
		t.Fatal("opts.Compaction must be cleared when noa is active")
	}
	if len(opts.AppendSystemPrompt) != 0 {
		t.Fatalf("noa static prompt must not remain in AppendSystemPrompt after dynamic tails: %+v", opts.AppendSystemPrompt)
	}
	if opts.DynamicBoundary != 2 || len(opts.SystemPrompt) != 3 {
		t.Fatalf("SystemPrompt len=%d DynamicBoundary=%d, want 3 / 2", len(opts.SystemPrompt), opts.DynamicBoundary)
	}
	if opts.SystemPrompt[0] != "static-core" || opts.SystemPrompt[2] != "dynamic-goal" {
		t.Fatalf("unexpected SystemPrompt order: %+v", opts.SystemPrompt)
	}

	var gotCompacted []bool
	InstallContextManagerDynamic(opts, 20000, 0, t.TempDir(), func(compacted bool) string {
		gotCompacted = append(gotCompacted, compacted)
		return "CTX"
	})
	attachNoaState(opts.Compactor, st)

	cv, ok := opts.Compactor.(harness.ContextView)
	if !ok {
		t.Fatalf("compactor %T does not implement ContextView", opts.Compactor)
	}

	msgs := noaPressureFixture()
	viewOut := cv.View(context.Background(), msgs)
	if len(gotCompacted) == 0 || gotCompacted[len(gotCompacted)-1] {
		t.Fatalf("with zero active blocks, compacted should start false (got %v)", gotCompacted)
	}
	hasNudge := false
	for _, m := range viewOut {
		if strings.Contains(m.Text(), "HOW TO COMPRESS") {
			hasNudge = true
			break
		}
	}
	if !hasNudge {
		t.Fatal("expected 20K ModelContextLimit to trigger noa pressure nudge on compressible backlog")
	}

	// Compress real messages in msgs via the registered Compress tool and verify
	// both live session and freshly resumed session report compacted=true to indexView.
	var compressTool actool.CoreTool
	for _, tl := range opts.Tools {
		if tl.Name() == noa.CompressToolName {
			compressTool = tl
			break
		}
	}
	if compressTool == nil {
		t.Fatal("compress tool missing from opts.Tools")
	}
	args, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{
			"startId": "m00002",
			"endId":   "m00005",
			"summary": strings.Repeat("Compressed early tool calls and enumerated results safely. ", 2),
		}},
	})
	res, err := compressTool.Call(context.Background(), args, &actool.ToolContext{ToolUseID: "toolu_cmp"})
	if err != nil || noa.PanelBlockCount(res.Content[0].Text) != 1 {
		t.Fatalf("compress call failed: err=%v out=%v", err, res)
	}

	// Re-check live session (bypass 15s index TTL by advancing fresh Install or observing second call):
	resumedOpts := &agentcore.Options{SystemPrompt: []string{"s"}, DynamicBoundary: 1}
	resumedSt := enableNoa(resumedOpts, func() bool { return true }, workDir, sessionID, 32000, nil)
	resumedCompacted := false
	InstallContextManagerDynamic(resumedOpts, 32000, 0, t.TempDir(), func(compacted bool) string {
		resumedCompacted = compacted
		return "CTX"
	})
	attachNoaState(resumedOpts.Compactor, resumedSt)
	_ = resumedOpts.Compactor.(harness.ContextView).View(context.Background(), msgs)
	if !resumedCompacted {
		t.Fatal("expected indexView to observe compacted=true once noa has active blocks")
	}
}

// TestEnableNoaOffMaterializesPriorBlocksIdempotently verifies switching off
// noa mid-session invokes Materialize once per resumed session when state.json
// holds active blocks, without creating stray directories when never enabled.
func TestEnableNoaOffMaterializesPriorBlocksIdempotently(t *testing.T) {
	workDir := t.TempDir()
	const neverUsedSID = "exp8-worker-i1"
	optsClean := &agentcore.Options{}
	stClean := enableNoa(optsClean, func() bool { return false }, workDir, neverUsedSID, 64000, nil)
	if stClean.materialize != nil {
		t.Fatal("session without prior noa state should not allocate a materializer")
	}
	if _, err := os.Stat(filepath.Join(workDir, "noa", neverUsedSID)); !os.IsNotExist(err) {
		t.Fatalf("disabled noa must not create state directories on disk: %v", err)
	}

	const sid = "exp9-main"
	body := strings.Repeat("compressible recon output. ", 300)
	tail := strings.Repeat("recent message. ", 250)
	rawMsgs := []llm.Message{llm.UserText("the task goal")}
	for range 4 {
		rawMsgs = append(rawMsgs, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(body)}})
	}
	for range 6 {
		rawMsgs = append(rawMsgs, llm.UserText(tail))
	}

	// Step 1: run with noa enabled and compress messages 2..5 through the real tool.
	onOpts := &agentcore.Options{SystemPrompt: []string{"s"}, DynamicBoundary: 1}
	onSt := enableNoa(onOpts, func() bool { return true }, workDir, sid, 64000, nil)
	InstallContextManagerDynamic(onOpts, 64000, 0, t.TempDir(), func(compacted bool) string { return "" })
	attachNoaState(onOpts.Compactor, onSt)
	_ = onOpts.Compactor.(harness.ContextView).View(context.Background(), rawMsgs)

	args, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{
			"startId": "m00002",
			"endId":   "m00005",
			"summary": strings.Repeat("Checked /login and /admin endpoints and enumerated auth behavior. ", 2),
		}},
	})
	var compressTool actool.CoreTool
	for _, tl := range onOpts.Tools {
		if tl.Name() == noa.CompressToolName {
			compressTool = tl
			break
		}
	}
	if compressTool == nil {
		t.Fatal("compress tool was not registered by enableNoa")
	}
	res, err := compressTool.Call(context.Background(), args, &actool.ToolContext{ToolUseID: "toolu_1"})
	if err != nil || noa.PanelBlockCount(res.Content[0].Text) != 1 {
		t.Fatalf("setup compression failed: err=%v out=%v", err, res)
	}

	// Step 2: turn noa off and verify DomainCompactor.Pre materializes once cleanly.
	opts := &agentcore.Options{}
	noaSt := enableNoa(opts, func() bool { return false }, workDir, sid, 64000, nil)
	var viewCompacted bool
	InstallContextManagerDynamic(opts, 64000, 0, t.TempDir(), func(compacted bool) string {
		viewCompacted = compacted
		return "IDX"
	})
	attachNoaState(opts.Compactor, noaSt)

	afterPre := opts.Compactor.Pre(context.Background(), rawMsgs, 0)
	if len(afterPre) >= len(rawMsgs) {
		t.Fatalf("expected Materialize to collapse compressed turns, len=%d -> %d", len(rawMsgs), len(afterPre))
	}
	var joined strings.Builder
	for _, m := range afterPre {
		joined.WriteString(m.Text())
	}
	if !strings.Contains(joined.String(), "Checked /login and /admin") {
		t.Fatalf("materialized summary missing: %s", joined.String())
	}
	if strings.Contains(joined.String(), "<noa-ref") {
		t.Fatalf("materialized view should strip ref tags when noa is off: %s", joined.String())
	}
	_ = opts.Compactor.(harness.ContextView).View(context.Background(), afterPre)
	if !viewCompacted {
		t.Fatal("expected DomainCompactor.View to report compacted=true after materialization")
	}

	// Calling Pre a second time inside the same session must be a no-op (sync.Once).
	afterSecond := opts.Compactor.Pre(context.Background(), afterPre, 0)
	if len(afterSecond) != len(afterPre) {
		t.Fatalf("second Pre must not alter already-materialized slice: %d != %d", len(afterSecond), len(afterPre))
	}
}

func TestScaledNoaConfigLargeWindow(t *testing.T) {
	for _, limit := range []int{32_000, 200_000, 400_000, 800_000, 1_000_000} {
		cfg := scaledNoaConfig(limit)
		if warns := noa.ValidateConfig(cfg); len(warns) > 0 {
			t.Fatalf("limit=%d ValidateConfig warnings: %v", limit, warns)
		}
	}
	cfg800 := scaledNoaConfig(800_000)
	if cfg800.Nudge.MinContextLimitPct != 0.65 || cfg800.Nudge.MaxContextLimitPct != 0.85 {
		t.Fatalf("800K nudge band = (%.2f, %.2f), want (0.65, 0.85)", cfg800.Nudge.MinContextLimitPct, cfg800.Nudge.MaxContextLimitPct)
	}
	if cfg800.Nudge.GrowthCap < 100_000 || cfg800.Nudge.MinGrowthFloor < 40_000 {
		t.Fatalf("800K growth scaling too small: cap=%d minFloor=%d", cfg800.Nudge.GrowthCap, cfg800.Nudge.MinGrowthFloor)
	}
	d := noa.DecideNudge(noa.DecideNudgeInput{
		Config:     cfg800,
		TokenCount: 400_000, // 50% of 800K — should not nudge yet (default 45% would)
		Recommendation: noa.Recommendation{
			CompressibleRanges: []noa.RangeInfo{{Chars: 240_000, Tokens: 60_000}},
		},
	})
	if d.ShouldInject {
		t.Fatalf("800K window at 50%% usage must not trigger early nudge: %s", d.Reason)
	}
}
