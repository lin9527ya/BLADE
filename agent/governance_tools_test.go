package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// mark_strategy_exhausted 是 planner 的受控「无方向」出口：必须写清原因并给出证据，
// 写一条结论事实后把信号交给引擎（本轮结束后暂停）。没有它，planner 只能靠边缘意图
// 凑硬底线，任务会无限空转（任务 14 实测 2 小时 0 产出）。
func TestMarkStrategyExhaustedRequiresReasonAndEvidence(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	var got string
	tools.SetStrategyExhaustedHandler(func(reason string) { got = reason })
	call := func(body string) string { return callTool(t, tools.PlannerTools(), "mark_strategy_exhausted", body) }

	if out := call(`{"reason":"  ","evidence_ids":[1]}`); !strings.Contains(out, "reason 必填") {
		t.Fatalf("空理由应被拒：%s", out)
	}
	if out := call(`{"reason":"都试过了"}`); !strings.Contains(out, "evidence_ids 必填") {
		t.Fatalf("没有证据应被拒：%s", out)
	}
	if out := call(`{"reason":"都试过了","evidence_ids":[999999]}`); !strings.Contains(out, "不存在于本任务") {
		t.Fatalf("不存在的证据应被拒：%s", out)
	}
	goal, err := ts.AddGoal(map[string]any{"text": "拿下后台"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if out := call(fmt.Sprintf(`{"reason":"都试过了","evidence_ids":[%d]}`, goal)); !strings.Contains(out, "只能用事实/漏洞/意图") {
		t.Fatalf("目标不能当证据：%s", out)
	}

	out := call(fmt.Sprintf(`{"reason":"所有入口都 403，口径已穷尽","evidence_ids":[%d]}`, intent))
	if !strings.HasPrefix(out, "strategy exhausted recorded: fact ") {
		t.Fatalf("应写入结论事实：%s", out)
	}
	if got != "所有入口都 403，口径已穷尽" {
		t.Fatalf("引擎应收到暂停原因，got %q", got)
	}
	facts, err := ts.ListByKind(db.KindFact, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range facts {
		if strings.Contains(string(f.Payload), `"exhausted": true`) || strings.Contains(string(f.Payload), `"exhausted":true`) {
			found = true
		}
	}
	if !found {
		t.Fatal("结论事实应带 exhausted=true 标记，供报告与后续轮次识别")
	}
	if tools.writes.Facts == 0 {
		t.Fatal("结论事实应计入写回统计")
	}
}

// abandon_goal：只有 open 目标能放弃，理由必填；放弃后目标保留在图上不再阻断收官。
func TestAbandonGoalTool(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	call := func(body string) string { return callTool(t, tools.PlannerTools(), "abandon_goal", body) }

	goal, err := ts.AddGoal(map[string]any{"text": "拿下后台"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if out := call(fmt.Sprintf(`{"goal_id":%d,"reason":" "}`, goal)); !strings.Contains(out, "reason 必填") {
		t.Fatalf("空理由应被拒：%s", out)
	}
	if out := call(`{"goal_id":999999,"reason":"已下线"}`); !strings.Contains(out, "必须是本任务的目标节点") {
		t.Fatalf("不存在的目标应被拒：%s", out)
	}
	if out := call(fmt.Sprintf(`{"goal_id":%d,"reason":"目标已下线"}`, goal)); !strings.HasPrefix(out, "goal abandoned: ") {
		t.Fatalf("放弃 open 目标应成功：%s", out)
	}
	n, err := ts.GetNode(goal)
	if err != nil || n == nil || n.State != "abandoned" {
		t.Fatalf("目标状态应为 abandoned，got %+v err=%v", n, err)
	}
	if out := call(fmt.Sprintf(`{"goal_id":%d,"reason":"再来一次"}`, goal)); !strings.Contains(out, "只有未达成(open)的目标可以放弃") {
		t.Fatalf("重复放弃应被拒：%s", out)
	}
}

// 目标已在同一轮被判达成后，不能再派新意图：否则任务收官时 frontier 留下永远
// 没人领取的 open 意图（任务结束后它们不会被 worker 领）。
func TestAddIntentRejectedAfterGoalMet(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	tools.GoalMet = true
	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"intents":[{"summary":"再补一条边缘方向"}]}`)
	if !strings.Contains(out, "不能再新增意图") {
		t.Fatalf("GoalMet 后 add_intent 应被拒：%s", out)
	}
}
