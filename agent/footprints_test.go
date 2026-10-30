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
