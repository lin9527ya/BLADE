package agent

import (
	"context"
	"errors"
	"iter"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/db"
)

func TestCoarseIntentSummary(t *testing.T) {
	cases := []struct {
		summary string
		kind    string
		assets  bool
		coarse  bool
	}{
		{"对目标做认证面测试", "", false, true},
		{"测试注入", "verify", false, true},
		{"对 https://example.test/admin/login 测账号枚举", "", false, false},
		{"对 /search?q= 做三通道注入验证", "verify", false, false},
		{"核实 nopCommerce 4.1 的 CVE", "", false, false},
		{"从 finding-4156 继续", "", false, false},
		{"发现 uradi-sam.rs 的同根子域", "discovery", false, false},
		{"侦察目标", "discovery", true, false},
		{"侦察目标", "discovery", false, true},
	}
	for _, c := range cases {
		got, why := coarseIntentSummary(c.summary, c.kind, c.assets)
		if got != c.coarse {
			t.Fatalf("%q kind=%s assets=%v: 期望 coarse=%v 实际 %v（%s）", c.summary, c.kind, c.assets, c.coarse, got, why)
		}
	}
}

// 开局轮：零事实时写「做认证面测试」这类风险类目录必须被拒收——那一轮重写没有代价，
// 而放行它就是 worker 每个路径各探一次、深度全丢在最浅处的开始。
func TestAddIntentRejectsCoarseSummaryAtOpening(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	tools.openingStrict = true
	out := callTool(t, tools.PlannerTools(), "add_intent", `{"risk_area":"auth","summary":"对目标做认证面测试"}`)
	if !strings.Contains(out, "没有点名任何具体入口") {
		t.Fatalf("开局轮的目录型意图应被拒收：%s", out)
	}
	out = callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"auth","summary":"对 /admin/login（302→/login?ReturnUrl=/admin）测账号枚举与无锁定下的低速口令喷洒"}`)
	if !strings.Contains(out, "intent created") {
		t.Fatalf("点名了具体入口的意图应通过：%s", out)
	}
}

// 中后段：已有事实可依，粗粒度只留痕不拦，避免白烧一轮（判定依据见回执的 coarse_warning）。
func TestAddIntentWarnsOnCoarseSummaryAfterOpening(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	out := callTool(t, tools.PlannerTools(), "add_intent", `{"risk_area":"auth","summary":"测试注入"}`)
	if !strings.Contains(out, "intent created") {
		t.Fatalf("中后段的粗粒度意图应放行：%s", out)
	}
	if !strings.Contains(out, "coarse_warning") {
		t.Fatalf("放行时应在回执里留 coarse_warning：%s", out)
	}
}

// 还有候选停在 open_proof_gap 时宣告穷尽必须被拒——那是「没测完」，不是「没得测」。
func TestStrategyExhaustedBlockedByOpenProofGap(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	if _, err := ts.AddNode(db.KindFinding, map[string]any{
		"summary":   "/login 的账号枚举差异仍缺后台侧读回面",
		"vulnclass": "auth",
	}, 0, db.FindingOpenProofGap, "work#1", nil); err != nil {
		t.Fatal(err)
	}
	ev := strconv.FormatInt(intent, 10)
	out := callTool(t, tools.PlannerTools(), "mark_strategy_exhausted",
		`{"reason":"各方向都试过了","evidence_ids":[`+ev+`]}`)
	if !strings.Contains(out, "open_proof_gap") {
		t.Fatalf("未闭合候选未在 reason 里点名时应拒绝穷尽：%s", out)
	}
	// 逐条点名后放行（这个测试任务没有范围资产，后两道硬门不适用）。
	var gapID int64
	findings, _ := ts.ListByKind(db.KindFinding, 10)
	for _, f := range findings {
		if f.State == db.FindingOpenProofGap {
			gapID = f.ID
		}
	}
	if gapID == 0 {
		t.Fatal("没找到 open_proof_gap 发现")
	}
	out = callTool(t, tools.PlannerTools(), "mark_strategy_exhausted",
		`{"reason":"各方向都试过了；#`+strconv.FormatInt(gapID, 10)+` 需要后台会话，属外部阻断","evidence_ids":[`+ev+`]}`)
	if !strings.Contains(out, "strategy exhausted recorded") {
		t.Fatalf("点名后的穷尽声明应放行：%s", out)
	}
}

// 复核否决后的冷却期内，声明穷尽直接返回，不再消耗一轮复核。
func TestStrategyExhaustedRespectsReviewCooldown(t *testing.T) {
	tools, _, intent := newToolWorker(t)
	tools.exhaustCooldownFn = func() int { return 2 }
	out := callTool(t, tools.PlannerTools(), "mark_strategy_exhausted",
		`{"reason":"又没方向了","evidence_ids":[`+strconv.FormatInt(intent, 10)+`]}`)
	if !strings.Contains(out, "冷却") {
		t.Fatalf("冷却期内应直接拒绝穷尽声明：%s", out)
	}
}

// coarseIntentProvider 每次规划轮都提交一条只有风险类的意图，用于验证 Plan 是否在
// 开局轮打开 add_intent 的硬粒度门（openingStrict）。
type coarseIntentProvider struct{ calls int }

func (p *coarseIntentProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if n := len(req.Messages); n > 0 {
		for _, b := range req.Messages[n-1].Content {
			if b.Type == llm.BlockToolResult {
				return assistantMsg("结论：本轮已派发方向"), "end_turn", llm.Usage{}, nil
			}
		}
	}
	p.calls++
	return toolUseMsg("tu-coarse", "add_intent", `{"risk_area":"auth","summary":"对目标做认证面测试"}`),
		"tool_use", llm.Usage{}, nil
}

func (p *coarseIntentProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("test provider is non-streaming only"))
	}
}

func TestPlannerOpeningRejectsCoarseIntent(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("opening granularity", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	p := NewPlanner(&coarseIntentProvider{}, "m", t.TempDir(), nil, 0, 8)
	p.SetNonStreaming(func() bool { return true })
	p.SetWorkersResolver(func() int { return 0 })

	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "g", nil, nil); err != nil {
		t.Fatalf("开局轮 Plan: %v", err)
	}
	if intents, _ := ts.ListByKind(db.KindIntent, 10); len(intents) != 0 {
		t.Fatalf("开局轮应拒收粗粒度意图，实际建了 %d 条", len(intents))
	}
	// 摸底窗口用完（前两轮）后恢复常规口径：同一句话只警告、不拦。
	p.openingRounds = 2
	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "g", nil, nil); err != nil {
		t.Fatalf("第三轮 Plan: %v", err)
	}
	if intents, _ := ts.ListByKind(db.KindIntent, 10); len(intents) != 1 {
		t.Fatalf("开局窗口结束后应放行（只警告），实际建了 %d 条", len(intents))
	}
}
