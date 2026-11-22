package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// goal_met 是"绕过逐个证明、直接全局收官"的手段：必须带证据，且在有显式目标时让位给
// prove_goal（那里才有「证据→目标」的边）。
func TestGoalMetRequiresEvidence(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	call := func(body string) string {
		return callTool(t, tools.PlannerTools(), "goal_met", body)
	}

	if out := call(`{"reason":"干完了"}`); !strings.Contains(out, "evidence_id 必填") {
		t.Fatalf("没有证据应被拒：%s", out)
	}
	if out := call(`{"reason":"干完了","evidence_id":999999}`); !strings.Contains(out, "必须是本任务") {
		t.Fatalf("指向不存在的节点应被拒：%s", out)
	}
	factID, err := ts.AddNode(db.KindFact, map[string]any{"summary": "目标物已取得"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := call(fmt.Sprintf(`{"reason":"已取得目标物","evidence_id":%d}`, factID))
	if !strings.Contains(out, "acknowledged") || !strings.Contains(out, fmt.Sprintf("#%d", factID)) {
		t.Fatalf("带证据的收官应通过并回执证据 id：%s", out)
	}

	// 有显式目标的任务：goal_met 让位给 prove_goal。
	tools2, ts2, _ := newToolWorker(t)
	goalID, err := ts2.AddGoal(map[string]any{"summary": "拿下后台"}, "goals")
	if err != nil {
		t.Fatal(err)
	}
	fact2, _ := ts2.AddNode(db.KindFact, map[string]any{"summary": "证据"}, 0, "confirmed", "worker", nil)
	out = callTool(t, tools2.PlannerTools(), "goal_met", fmt.Sprintf(`{"reason":"x","evidence_id":%d}`, fact2))
	if !strings.Contains(out, "请用 prove_goal") {
		t.Fatalf("有目标时应让位给 prove_goal：%s", out)
	}
	_ = goalID
}

// 目标初始只保留 origin --spawns--> goal 这一条链；prove_goal 才追加
// 事实/漏洞 --proves--> 目标，把达成路径接到图上。
func TestGoalStartsFromOriginAndProofConnectsEvidence(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	origin, err := ts.OriginFactID()
	if err != nil || origin == 0 {
		t.Fatalf("origin fact: id=%d err=%v", origin, err)
	}
	res, err := tools.setGoals().Call(context.Background(), json.RawMessage(`{"text":"拿下后台"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("set_goals: %v %s", err, res.Flatten())
	}
	goals, err := ts.ListByKind(db.KindGoal, 10)
	if err != nil || len(goals) != 1 {
		t.Fatalf("goals=%d err=%v", len(goals), err)
	}
	goalID := goals[0].ID
	edges, _ := ts.Edges(100)
	initial := false
	for _, e := range edges {
		if e.From == origin && e.To == goalID && e.Rel == db.RelSpawns {
			initial = true
			continue
		}
		if e.From == goalID || e.To == goalID {
			t.Fatalf("新目标除起点初始边外不应有其它边：%+v", e)
		}
	}
	if !initial {
		t.Fatal("新目标应保留 origin --spawns--> goal 初始边")
	}

	factID, err := ts.AddNode(db.KindFact, map[string]any{"summary": "已拿到管理员会话"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.PlannerTools(), "prove_goal",
		fmt.Sprintf(`{"goal_id":%d,"evidence_id":%d,"reason":"证据充分"}`, goalID, factID))
	if !strings.Contains(out, "marked met") {
		t.Fatalf("prove_goal 应标记目标达成：%s", out)
	}
	edges, _ = ts.Edges(100)
	linked := false
	for _, e := range edges {
		if e.From == factID && e.To == goalID && e.Rel == db.RelProves {
			linked = true
		}

	}
	if !linked {
		t.Fatalf("达成后应写 evidence --proves--> goal：%+v", edges)
	}
	node, err := ts.GetNode(goalID)
	if err != nil || node == nil || node.State != "met" {
		t.Fatalf("goal 应为 met：node=%+v err=%v", node, err)
	}
}

// prove_goal 的 reason 不是装饰：缺失时必须拒绝，且不得在回执里宣称成功。
// 证据类型/归属不合法时同样不能留下「已 met」的假象——写入失败必须被看见。
func TestProveGoalRequiresReasonAndDoesNotClaimSuccess(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	goal, err := ts.AddGoal(map[string]any{"text": "拿下后台"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := ts.AddNode(db.KindFact, map[string]any{"summary": "管理员会话可用"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.PlannerTools(), "prove_goal", fmt.Sprintf(
		`{"goal_id":%d,"evidence_id":%d}`, goal, fact))
	if !strings.Contains(out, "reason 必填") || strings.Contains(out, "marked met") {
		t.Fatalf("缺 reason 应拒绝且不得宣称成功：%s", out)
	}
	if n, err := ts.GetNode(goal); err != nil || n == nil || n.State != "open" {
		t.Fatalf("拒绝后目标必须仍是 open：node=%+v err=%v", n, err)
	}
	out = callTool(t, tools.PlannerTools(), "prove_goal", fmt.Sprintf(
		`{"goal_id":%d,"evidence_id":%d,"reason":"用目标自证"}`, goal, goal))
	if !strings.Contains(out, "事实/漏洞/据点/凭据") || strings.Contains(out, "marked met") {
		t.Fatalf("非证据节点应拒绝：%s", out)
	}
	out = callTool(t, tools.PlannerTools(), "prove_goal", fmt.Sprintf(
		`{"goal_id":%d,"evidence_id":%d,"reason":"管理员会话可直接登录后台"}`, goal, fact))
	if !strings.Contains(out, "marked met") {
		t.Fatalf("合法证明应成功：%s", out)
	}
	n, err := ts.GetNode(goal)
	if err != nil || n == nil || n.State != "met" {
		t.Fatalf("goal 应为 met：node=%+v err=%v", n, err)
	}
	if !strings.Contains(string(n.Payload), "管理员会话可直接登录后台") {
		t.Fatalf("目标 payload 应保存证明理由：%s", n.Payload)
	}
}

// 放弃不是达成：只剩「已达成 + 已放弃」时收官文案必须区分，不能把放弃算进 met。
func TestProveGoalDistinguishesSettledFromAllMet(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	metGoal, err := ts.AddGoal(map[string]any{"text": "能达成的目标"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	abandonedGoal, err := ts.AddGoal(map[string]any{"text": "放弃的目标"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := ts.AbandonGoal(abandonedGoal, "客户取消"); err != nil || changed != 1 {
		t.Fatalf("abandon: changed=%d err=%v", changed, err)
	}
	fact, err := ts.AddNode(db.KindFact, map[string]any{"summary": "证据"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.PlannerTools(), "prove_goal", fmt.Sprintf(
		`{"goal_id":%d,"evidence_id":%d,"reason":"证据充分"}`, metGoal, fact))
	if !strings.Contains(out, "达成 1 / 放弃 1") || strings.Contains(out, "所有目标均已达成") {
		t.Fatalf("放弃必须是「了结」而不是「全部达成」：%s", out)
	}
	if !tools.GoalMet {
		t.Fatal("已无 open 目标时应触发收官")
	}
}
