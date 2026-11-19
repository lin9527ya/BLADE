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
	for _, want := range []string{"硬底线（最高优先）", "产出 0 个意图是正常结果", "深度优先于覆盖度", "保持路线多样", "TodoWrite",
		"add_task_scope", "add_company_scope", "扩面即授权", "可到 8 个", "至少 2–3 条"} {
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

// 写回/动作类工具不再默认绑给 planner，但仍留在工具目录，可在工具页改绑
// worker/mainagent；只读知识查询（打法/CVE/AD/opsec）继续留给 planner。
func TestPlannerDefaultBindingSlim(t *testing.T) {
	agents := map[string][]string{}
	for _, seed := range BuiltinToolSeeds() {
		agents[seed.Key] = seed.Agents
	}
	has := func(key, agent string) bool {
		for _, a := range agents[key] {
			if a == agent {
				return true
			}
		}
		return false
	}
	for _, gone := range []string{
		"record_playbook", "promote_knowledge",
		"merge_findings", "rotate_egress", "list_worker_traces",
	} {
		if has(gone, "planner") {
			t.Fatalf("write/action tool %q still default-bound to planner: %v", gone, agents[gone])
		}
	}
	for _, keep := range []string{
		"lookup_cves", "lookup_playbooks", "ad_paths", "opsec_notes",
		"drop_intent", "link_intent_source", "accept_artifact_risk",
		"accept_footprint_risk", "list_footprints", "blackboard_view",
		"consume_hint", "abandon_goal", "asset_neighbors",
	} {
		if !has(keep, "planner") {
			t.Fatalf("planner lost retained tool %q: %v", keep, agents[keep])
		}
	}
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

// 旧 requires 只保留为审计数据；显式 depends_on 默认 advisory，不挡领取。
// 只有 strict 才在活跃前置期间暂缓，且前置进入死状态后 fail-open。
func TestOverviewDoesNotExposeLegacyBlockingSignals(t *testing.T) {
	tools, ts, upstream := newToolWorker(t)
	downstream, err := ts.AddIntent(map[string]any{"summary": "下游：提权", "depends_on_mode": "strict"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(downstream, db.RelDependsOn, upstream); err != nil {
		t.Fatal(err)
	}
	// 前置仍活跃（open）时 strict 下游不可领。
	frontier, err := ts.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range frontier {
		if n.ID == downstream {
			t.Fatal("前置 open 时 strict 下游不应进入 frontier")
		}
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
	frontier, err = ts.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, n := range frontier {
		seen[n.ID] = true
	}
	if !seen[downstream] {
		t.Fatalf("前置 blocked 后 strict 下游应 fail-open 可领: %+v", seen)
	}
	if !seen[required] {
		t.Fatalf("无依赖意图应可领取: %+v", seen)
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
