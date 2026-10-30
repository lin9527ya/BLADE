package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 串行链走图而不是记忆：add_intent 带 depends_on → 下游在前置有结论前不进 frontier；
// 态势里带 blocked_by；前置跑不通时 planner 可以拆链。
func TestAddIntentDependsOnAndUnblock(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	first := lootNodeID(t, callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"other","summary":"第一步：拿到 /admin 的登录态"}`))

	out := callTool(t, tools.PlannerTools(), "add_intent",
		fmt.Sprintf(`{"risk_area":"other","summary":"第二步：用登录态打后台功能级越权","depends_on":[%d]}`, first))
	if !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("带前置的意图应可创建：%s", out)
	}
	if !strings.Contains(out, "等前置") {
		t.Fatalf("回执应说明它在等前置：%s", out)
	}
	second := lootNodeID(t, out)

	// frontier 不包含下游。
	fr, err := ts.Frontier(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range fr {
		if n.ID == second {
			t.Fatal("前置未满足时下游不该进 frontier")
		}
	}

	// 态势里带 blocked_by，并给出被谁挡着。
	overview := tools.graphOverviewData()
	items, _ := overview["open_intents"].([]map[string]any)
	found := false
	for _, it := range items {
		if it["id"] == second {
			found = true
			by, _ := it["blocked_by"].([]map[string]any)
			if len(by) != 1 || by[0]["id"] != first {
				t.Fatalf("blocked_by 应指向前置：%+v", it["blocked_by"])
			}
		}
	}
	if !found {
		t.Fatal("下游应出现在 open_intents 里（带 blocked_by 标记）")
	}
	if got, _ := overview["blocked_intents_total"].(int); got != 1 {
		t.Fatalf("被前置挡住的意图数不对：%v", overview["blocked_intents_total"])
	}

	// 前置拿到结论 → 下游放行。
	if err := ts.SetIntentState(first, "negative"); err != nil {
		t.Fatal(err)
	}
	if fr, _ := ts.Frontier(50); !hasIntent(fr, second) {
		t.Fatal("前置有结论后下游应进 frontier")
	}

	// 拆链出口：理由必填；拆掉后立刻可派发。
	if got := callTool(t, tools.PlannerTools(), "unblock_intent",
		fmt.Sprintf(`{"intent_id":%d}`, second)); !strings.Contains(got, "reason 必填") {
		t.Fatalf("空理由应被拒：%s", got)
	}

	third := lootNodeID(t, callTool(t, tools.PlannerTools(), "add_intent",
		fmt.Sprintf(`{"risk_area":"other","summary":"第三步：不等前置了","depends_on":[%d]}`, first)))
	got := callTool(t, tools.PlannerTools(), "unblock_intent",
		fmt.Sprintf(`{"intent_id":%d,"reason":"前置跑不通，改从另一个入口进"}`, third))
	if !strings.Contains(got, "unblocked") {
		t.Fatalf("拆链应成功：%s", got)
	}
	if fr, _ := ts.Frontier(50); !hasIntent(fr, third) {
		t.Fatal("拆链后应可派发")
	}
}

// 前置必须是本任务的意图：拿事实/资产节点来当"依赖"是误用。
func TestAddIntentDependsOnRejectsNonIntent(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	factID, err := ts.AddNode(db.KindFact, map[string]any{"summary": "一条事实"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.PlannerTools(), "add_intent",
		fmt.Sprintf(`{"risk_area":"other","summary":"误把事实当前置","depends_on":[%d]}`, factID))
	if !strings.Contains(out, "不是意图") {
		t.Fatalf("非意图前置应被拒：%s", out)
	}
	if out := callTool(t, tools.PlannerTools(), "add_intent", `{"risk_area":"other","summary":"不存在的前置","depends_on":[999999]}`); !strings.Contains(out, "不存在") {
		t.Fatalf("不存在的前置应被拒：%s", out)
	}
}

func hasIntent(nodes []*db.Node, id int64) bool {
	for _, n := range nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

var _ = json.Marshal
