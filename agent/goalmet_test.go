package agent

import (
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
