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

// 目标初始必须独立：起点与目标之间不能有边。只有 prove_goal 写证据时，
// 才由事实/漏洞 --proves--> 目标，把达成路径接到图上。
func TestGoalStartsUnlinkedAndProofConnectsEvidence(t *testing.T) {
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
	for _, e := range edges {
		if e.From == goalID || e.To == goalID {
			t.Fatalf("新目标初始不应有任何边：%+v", e)
		}
		if e.From == origin && e.To == goalID && e.Rel == db.RelSpawns {
			t.Fatalf("起点不应直接连目标：%+v", e)
		}
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
		if e.From == origin && e.To == goalID {
			t.Fatalf("达成后也不该出现起点→目标的边：%+v", e)
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
