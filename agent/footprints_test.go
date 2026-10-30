package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestReportFootprintRequiresEvidenceAndObservedState(t *testing.T) {
	tools, ts, intent := newToolWorker(t)

	out := callTool(t, tools.WorkerTools(), "report_footprint",
		fmt.Sprintf(`{"summary":"auth.log 留下失败登录","category":"audit","where":["目标系统日志"],`+
			`"signals":["4625"],"exposure":"高","host":"10.0.0.5","locator":"/var/log/auth.log",`+
			`"cleanup":"只删除对应时间窗的失败记录","state":"unknown",`+
			`"evidence":"hydra 尝试后 grep 到 10.0.0.9 的失败记录","confidence":"high","intent_id":%d}`, intent))
	if !strings.HasPrefix(out, "footprint recorded: ") {
		t.Fatalf("痕迹没记上：%s", out)
	}
	nodes, err := ts.ListByKind(db.KindFootprint, 10)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("痕迹节点数 = %d (err=%v)，应为 1", len(nodes), err)
	}
	n := nodes[0]
	if n.State != db.StateFootprintUnknown {
		t.Errorf("state = %q，应为 unknown（不能默认成 loot active）", n.State)
	}
	f := db.ParseFootprint(n)
	if f.Source != db.FootprintSourceObserved || f.Category != db.FootprintCatLog || f.Exposure != db.FootprintExposureHigh {
		t.Errorf("payload 归一化不对：%+v", f)
	}
	if f.Host != "10.0.0.5" || f.Locator != "/var/log/auth.log" || f.Cleanup == "" {
		t.Errorf("host/locator/cleanup 没写进去：%+v", f)
	}
	if tools.writes.Footprints != 1 {
		t.Errorf("WriteCounts.Footprints = %d，应为 1", tools.writes.Footprints)
	}

	// 不能绕过证据门直接创建 cleaned/accepted。
	out = callTool(t, tools.WorkerTools(), "report_footprint",
		fmt.Sprintf(`{"summary":"x","state":"cleaned","evidence":"e","confidence":"high","intent_id":%d}`, intent))
	if !strings.Contains(out, "state 只能是 open | unknown") {
		t.Fatalf("应拒绝创建 cleaned：%s", out)
	}
	out = callTool(t, tools.WorkerTools(), "report_footprint",
		fmt.Sprintf(`{"summary":"x","evidence":"","confidence":"high","intent_id":%d}`, intent))
	if !strings.Contains(out, "evidence 不能为空") {
		t.Fatalf("应拒绝空证据：%s", out)
	}
}

func TestFootprintAgentTransitions(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	report := func(summary string) string {
		return callTool(t, tools.WorkerTools(), "report_footprint",
			fmt.Sprintf(`{"summary":%q,"category":"log","evidence":"cmd → out","confidence":"medium","intent_id":%d}`,
				summary, intent))
	}
	report("a")
	report("b")
	nodes, _ := ts.ListByKind(db.KindFootprint, 10)
	if len(nodes) != 2 {
		t.Fatalf("应记 2 条，得到 %d", len(nodes))
	}
	idA, idB := nodes[0].ID, nodes[1].ID

	if out := callTool(t, tools.WorkerTools(), "mark_footprint_cleaned",
		fmt.Sprintf(`{"footprint_id":%d,"evidence":" "}`, idA)); !strings.Contains(out, "证据必填") {
		t.Fatalf("空清理证据应被拒绝：%s", out)
	}
	if out := callTool(t, tools.WorkerTools(), "mark_footprint_cleaned",
		fmt.Sprintf(`{"footprint_id":%d,"evidence":"sed -i '/10.0.0.9/d' /var/log/auth.log → 记录数减 1"}`, idA)); !strings.Contains(out, "footprint cleaned") {
		t.Fatalf("清理失败：%s", out)
	}
	if out := callTool(t, tools.WorkerTools(), "mark_footprint_cleaned",
		fmt.Sprintf(`{"footprint_id":%d,"evidence":"again"}`, idA)); !strings.Contains(out, "当前状态不可标记已清理") {
		t.Fatalf("重复销账应被拒绝：%s", out)
	}

	if out := callTool(t, tools.MainAgentTools(), "accept_footprint_risk",
		fmt.Sprintf(`{"footprint_id":%d,"reason":""}`, idB)); !strings.Contains(out, "理由") {
		t.Fatalf("空理由应被拒绝：%s", out)
	}
	if out := callTool(t, tools.MainAgentTools(), "accept_footprint_risk",
		fmt.Sprintf(`{"footprint_id":%d,"reason":"客户确认接受，清理会破坏证据链"}`, idB)); !strings.Contains(out, "risk accepted") {
		t.Fatalf("接受风险失败：%s", out)
	}

	report("c")
	nodes, _ = ts.ListByKind(db.KindFootprint, 10)
	var idC int64
	for _, n := range nodes {
		if db.ParseFootprint(n).Summary == "c" {
			idC = n.ID
		}
	}
	if out := callTool(t, tools.WorkerTools(), "mark_footprint_not_found",
		fmt.Sprintf(`{"footprint_id":%d,"evidence":"三个时间窗 grep 均无记录"}`, idC)); !strings.Contains(out, "not found") {
		t.Fatalf("标记未发现失败：%s", out)
	}

	out := callTool(t, tools.WorkerTools(), "list_footprints", `{"limit":10}`)
	if !strings.Contains(out, `"state_label"`) || !strings.Contains(out, "a") {
		t.Fatalf("list_footprints 输出不对：%s", out)
	}
}

func TestCleanupIntentsAreProtectedFromPlanner(t *testing.T) {
	_, ts, _ := newToolWorker(t)
	cleanup, err := ts.AddIntent(map[string]any{
		"summary": "清理测试痕迹", "cleanup": true, "cleanup_footprint_ids": []int{1},
	}, 9, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	planner := NewToolSet(ts, "planner")
	out := callTool(t, planner.PlannerTools(), "drop_intent",
		fmt.Sprintf(`{"intent_id":%d,"reason":"任务要收尾"}`, cleanup))
	if !strings.Contains(out, "痕迹清理意图") {
		t.Fatalf("drop_intent 应拒绝清理意图：%s", out)
	}

	killed := false
	planner.killWork = func(int64) error { killed = true; return nil }
	out = callTool(t, planner.PlannerTools(), "kill_work", fmt.Sprintf(`{"intent_id":%d}`, cleanup))
	if !strings.Contains(out, "痕迹清理意图") || killed {
		t.Fatalf("kill_work 应拒绝清理意图且不触发终止：%s killed=%v", out, killed)
	}
}

func TestFootprintToolRegistered(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	have := map[string]bool{}
	for _, tl := range tools.WorkerTools() {
		have[tl.Name()] = true
	}
	for _, name := range []string{"report_footprint", "list_footprints", "mark_footprint_cleaned", "mark_footprint_not_found"} {
		if !have[name] {
			t.Errorf("worker 工具面缺少 %s", name)
		}
	}
	if have["accept_footprint_risk"] {
		t.Error("worker 不应有接受风险工具（那是 planner/人工的决定）")
	}
	main := map[string]bool{}
	for _, tl := range tools.MainAgentTools() {
		main[tl.Name()] = true
	}
	if !main["accept_footprint_risk"] || !main["report_footprint"] {
		t.Errorf("mainagent 工具面应含痕迹工具：%v", main)
	}
}

func TestReportFootprintLinksSourceNodes(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	fact, err := ts.AddNode(db.KindFact, map[string]any{"summary": "确认上传点可写"}, 5, "confirmed", "work#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.WorkerTools(), "report_footprint",
		fmt.Sprintf(`{"summary":"上传点访问日志留下记录","evidence":"curl 上传 → access.log 200","confidence":"high",`+
			`"intent_id":%d,"source_ids":[%d]}`, intent, fact))
	if !strings.Contains(out, "footprint recorded") {
		t.Fatalf("痕迹没记上：%s", out)
	}
	edges, err := ts.Edges(100)
	if err != nil {
		t.Fatal(err)
	}
	var fromIntent, fromFact bool
	var fpID int64
	for _, e := range edges {
		if e.Rel == db.RelLeaves && e.From == intent {
			fromIntent = true
			fpID = e.To
		}
		if e.Rel == db.RelLeaves && e.From == fact {
			fromFact = true
		}
		if e.Rel == db.RelYields && e.From == intent {
			t.Fatalf("痕迹不应走 yields（会被意图删除级联带走）：%+v", e)
		}
	}
	if !fromIntent || !fromFact || fpID == 0 {
		t.Fatalf("来源连线不完整：intent=%v fact=%v fp=%d edges=%+v", fromIntent, fromFact, fpID, edges)
	}
}

// 意图漏填 parent_ids 时会被兜底连到任务起点；link_intent_source 必须能把它接回真实来源，
// 并去掉那条兜底边——否则图上永远是「从起点发散」。
func TestLinkIntentSourceRepairsFallbackEdge(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	// 开局（图里还没有事实）漏填 parent_ids：兜底连 origin。
	out := callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"开局侦察"}`)
	if !strings.Contains(out, "兜底连到任务起点") {
		t.Fatalf("开局漏填 parent_ids 时应给出兜底提示：%s", out)
	}
	intentID := lootNodeID(t, out)
	fact, err := ts.AddNode(db.KindFact, map[string]any{"summary": "确认上传点可写"}, 5, "confirmed", "work#1", nil)
	if err != nil {
		t.Fatal(err)
	}

	edgesOf := func() map[string]bool {
		edges, err := ts.Edges(200)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, e := range edges {
			got[fmt.Sprintf("%d-%s-%d", e.From, e.Rel, e.To)] = true
		}
		return got
	}
	origin, err := ts.OriginFactID()
	if err != nil || origin == 0 {
		t.Fatalf("origin fact: %d err=%v", origin, err)
	}
	before := edgesOf()
	if !before[fmt.Sprintf("%d-derived_from-%d", origin, intentID)] {
		t.Fatalf("兜底边应存在：%v", before)
	}

	if out := callTool(t, tools.PlannerTools(), "link_intent_source",
		fmt.Sprintf(`{"intent_id":%d,"source_ids":[%d]}`, intentID, fact)); !strings.Contains(out, "已补") {
		t.Fatalf("补血缘失败：%s", out)
	}
	after := edgesOf()
	if !after[fmt.Sprintf("%d-derived_from-%d", fact, intentID)] {
		t.Fatalf("应补上真实来源边：%v", after)
	}
	if after[fmt.Sprintf("%d-derived_from-%d", origin, intentID)] {
		t.Fatalf("补上真实来源后应去掉起点兜底边：%v", after)
	}

	// 来源必须是 fact/finding：拿意图当来源要被拒。
	if out := callTool(t, tools.PlannerTools(), "link_intent_source",
		fmt.Sprintf(`{"intent_id":%d,"source_ids":[%d]}`, intentID, intentID)); !strings.Contains(out, "不是意图") && !strings.Contains(out, "意图只能锚在") {
		t.Fatalf("非法来源应被拒绝：%s", out)
	}
}

// 图里已有事实时漏填 parent_ids：不能连任务起点（那会让所有意图看起来都从起点长出来），
// 回执必须点名要求补血缘。
func TestAddIntentWithoutParentsWhenKnowledgeExists(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	fact, err := ts.AddNode(db.KindFact, map[string]any{"summary": "确认上传点可写"}, 5, "confirmed", "work#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"从上传点继续推进"}`)
	if !strings.Contains(out, "link_intent_source") || !strings.Contains(out, "来源缺失") {
		t.Fatalf("已有事实时漏填 parent_ids 应要求补血缘：%s", out)
	}
	intentID := lootNodeID(t, out)
	origin, _ := ts.OriginFactID()
	edges, err := ts.Edges(200)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edges {
		if e.Rel == db.RelDerivedFrom && e.From == origin && e.To == intentID {
			t.Fatalf("已有事实时不应再兜底连起点：%+v", e)
		}
	}
	// 补上之后就有了真实来源。
	if out := callTool(t, tools.PlannerTools(), "link_intent_source",
		fmt.Sprintf(`{"intent_id":%d,"source_ids":[%d]}`, intentID, fact)); !strings.Contains(out, "已补") {
		t.Fatalf("补血缘失败：%s", out)
	}
}

// 态势里必须直接给出空闲槽位数：只给 workers/running 两个数时，模型会把
// "有 1 条在跑"当成"已经有人在干活"，空转的槽位没人认领（任务 14 实测）。
func TestGraphOverviewExposesIdleSlots(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	tools.SetWorkerCap(4)
	running, err := ts.AddIntent(map[string]any{"summary": "在跑的方向"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.SetNodeState(running, "running"); err != nil {
		t.Fatal(err)
	}
	data := tools.graphOverviewData()
	q, ok := data["intent_queue"].(map[string]any)
	if !ok {
		t.Fatalf("态势缺 intent_queue：%v", data["intent_queue"])
	}
	if q["workers"] != 4 || q["running"] != 1 {
		t.Fatalf("workers/running 不对：%v", q)
	}
	if q["idle_slots"] != 3 {
		t.Fatalf("idle_slots = %v，应为 3（4 槽 - 1 在跑）", q["idle_slots"])
	}
}

// 槽位利用率纪律必须随 planner 系统提示词下发，且用户改正文也删不掉。
func TestPlannerSystemCarriesSlotUtilization(t *testing.T) {
	sys := plannerSystem("目标", "/tmp", "/tmp", "")
	for _, want := range []string{"槽位利用率", "idle_slots", "min(idle_slots, 4)"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("planner 系统提示词缺 %q", want)
		}
	}
	// 自定义正文（第四级兜底之前的任意一级）同样要带上这段。
	custom := plannerSystem("目标", "/tmp", "/tmp", "你是一个自定义规划者。")
	if !strings.Contains(custom, "槽位利用率") {
		t.Fatal("自定义正文也必须带代码持有的槽位利用率段")
	}
}
