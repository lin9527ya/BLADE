package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// The default planner flow is ARTEX's: exploration guidance lives in the prompt,
// not in code-held constraint blocks.
func TestPlannerPromptARTEXParity(t *testing.T) {
	p := plannerSystem("g", "/d", "/w", "")
	for _, want := range []string{"硬底线（最高优先）", "产出 0 个意图是正常结果", "深度优先于覆盖度", "保持路线多样", "TodoWrite"} {
		if !strings.Contains(p, want) {
			t.Fatalf("planner prompt missing ARTEX guidance %q", want)
		}
	}
	for _, gone := range []string{"混合版规划骨架", "槽位与队列深度", "广度纪律", "复盘轮（代码持有）", "探索约束开关"} {
		if strings.Contains(p, gone) {
			t.Fatalf("planner prompt still carries BLADE constraint block %q", gone)
		}
	}
}

func TestPlannerToolsARTEXSurface(t *testing.T) {
	have := map[string]bool{}
	for _, tool := range NewToolSet(nil, "").PlannerTools() {
		have[tool.Name()] = true
	}
	for _, banned := range []string{"unblock_intent", "record_probe_fact", "mark_strategy_exhausted", "coverage_matrix", "exploration_ledger", "dismiss_coverage", "chain_detail", "accept_completion_risk", "dismiss_leads"} {
		if have[banned] {
			t.Fatalf("ARTEX parity: planner still exposes constraint tool %q", banned)
		}
	}
	for _, keep := range []string{"add_intent", "prove_goal", "drop_intent", "graph_overview", "list_facts", "node_detail", "report_finding", "add_task_scope", "add_company_scope"} {
		if !have[keep] {
			t.Fatalf("planner lost capability tool %q", keep)
		}
	}
	// Feature tools unrelated to exploration remain available to the planner; they
	// are intentionally not treated as worker-only. Execution tools are still
	// excluded (checked by prompt_tools_test).
}

func TestAddIntentARTEXMinimalPayload(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	out := callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"对 /login 的 user 参数测账号枚举"}`)
	if !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("minimal ARTEX intent rejected: %s", out)
	}
	id := lootNodeID(t, out)
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"risk_area", "intent_kind", "goal_id", "contract", "requires", "depends_on", "uses_loot_ids"} {
		if _, ok := payload[gone]; ok {
			t.Fatalf("ARTEX payload must not synthesize %q: %v", gone, payload)
		}
	}
	// Fields BLADE used to validate are now ignored rather than rejected.
	out = callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"对 /admin 测越权","risk_area":"not-a-risk","kind":"bogus","goal_id":999999}`)
	if !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("removed constraint fields must be ignored: %s", out)
	}
}

// 旧 depends_on/requires 只保留为审计数据，不应在 planner 态势里继续表现为
// “被阻塞”——否则模型会少派本可领取的方向。Frontier 语义由 db parity test 覆盖。
func TestOverviewDoesNotExposeLegacyBlockingSignals(t *testing.T) {
	tools, ts, upstream := newToolWorker(t)
	downstream, err := ts.AddIntent(map[string]any{"summary": "下游：提权"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(downstream, db.RelDependsOn, upstream); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetIntentState(upstream, "blocked"); err != nil {
		t.Fatal(err)
	}
	required, err := ts.AddIntent(map[string]any{
		"summary":  "旧 requires 数据",
		"requires": []map[string]any{{"type": "capability", "key": "missing"}},
	}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	out := tools.graphOverviewData()
	for _, key := range []string{"blocked_by", "blocked_intents_total", "stranded_dependents", "stranded_dependents_total"} {
		if _, ok := out[key]; ok {
			t.Fatalf("overview still exposes %s: %+v", key, out[key])
		}
	}
	if items, ok := out["open_intents"].([]map[string]any); ok {
		for _, item := range items {
			if _, ok := item["requires_missing"]; ok {
				t.Fatalf("open intent still exposes requires_missing: %+v", item)
			}
		}
	}
	frontier, err := ts.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, n := range frontier {
		seen[n.ID] = true
	}
	if !seen[downstream] || !seen[required] {
		t.Fatalf("legacy dependency/requires still blocking: frontier=%+v", seen)
	}
}

func TestPlannerTemplateLibraryDoesNotOverrideARTEX(t *testing.T) {
	old := PlannerTemplateDefault
	t.Cleanup(func() { PlannerTemplateDefault = old })
	PlannerTemplateDefault = func() (string, bool) { return "旧模板：槽位补满、广度债、复盘轮", true }
	p := plannerSystem("g", "/d", "/w", "")
	if strings.Contains(p, "旧模板") || strings.Contains(p, "广度债") {
		t.Fatalf("planner template library must not reintroduce BLADE constraints: %s", p[:200])
	}
	if !strings.Contains(p, "硬底线（最高优先）") {
		t.Fatal("ARTEX built-in body missing")
	}
	// Task-level snapshots still win, matching the documented override path.
	got := plannerSystem("g", "/d", "/w", "任务自定义 {{.Goal}}")
	if !strings.HasPrefix(got, "任务自定义 g") {
		t.Fatalf("task snapshot should still win: %q", got[:80])
	}
}
