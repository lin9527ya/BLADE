package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// graph_overview 默认返回有界黑板视图：totals 与窗口分离，带 revision/events/routes；
// detail=true 才回到完整态势（related_tasks/edges_total 等字段）。
func TestBlackboardViewBoundedAndDetail(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	if _, err := ts.AddIntent(map[string]any{
		"summary":   "对 /search?q= 做报错注入",
		"risk_area": "sqli",
		"contract": map[string]any{
			"mechanism":  "sqli-error",
			"route":      "search-sqli",
			"produces":   []string{"db-error"},
			"acceptance": "出现数据库报错",
		},
	}, 5, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AddNode(db.KindFact, map[string]any{"summary": "首页返回 nginx/1.24"}, 0, "confirmed", "work#1", nil); err != nil {
		t.Fatal(err)
	}

	var compact map[string]any
	if err := json.Unmarshal([]byte(callTool(t, tools.PlannerTools(), "graph_overview", `{}`)), &compact); err != nil {
		t.Fatalf("compact 不是 JSON: %v", err)
	}
	if _, ok := compact["blackboard"]; !ok {
		t.Fatalf("默认视图应带 blackboard 元数据: %+v", compact)
	}
	if _, ok := compact["totals"]; !ok {
		t.Fatalf("默认视图应带 totals: %+v", compact)
	}
	if _, ok := compact["routes"]; !ok {
		t.Fatalf("默认视图应带 routes: %+v", compact)
	}
	routes, _ := compact["routes"].([]any)
	foundRoute := false
	for _, raw := range routes {
		if r, ok := raw.(map[string]any); ok && r["route"] == "search-sqli" {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Fatalf("routes 应按 contract.route 聚合: %+v", compact["routes"])
	}
	if _, ok := compact["events"]; !ok {
		t.Fatalf("默认视图应带 events: %+v", compact)
	}
	if _, ok := compact["folded_intents"]; ok {
		t.Fatal("有界视图不该带 folded_intents（完整态势才有）")
	}

	var detail map[string]any
	if err := json.Unmarshal([]byte(callTool(t, tools.PlannerTools(), "graph_overview", `{"detail":true}`)), &detail); err != nil {
		t.Fatalf("detail 不是 JSON: %v", err)
	}
	if _, ok := detail["related_tasks"]; !ok {
		t.Fatal("detail=true 应回到完整态势（缺 related_tasks）")
	}
	if _, ok := detail["folded_intents"]; !ok {
		// 没有折叠节点时该字段本来就不存在；用 edges_total 确认走的是完整分支。
		if _, ok := detail["edges_total"]; !ok {
			t.Fatal("detail=true 应回到完整态势（缺 edges_total）")
		}
	}
}

// blackboard_view 的 after/limit 是真实事件游标：limit=1 时 has_more 为真，
// next_cursor 指向返回的最后一条，下一次只能看到更晚的节点。
func TestBlackboardViewEventCursor(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	for i := 0; i < 3; i++ {
		if _, err := ts.AddNode(db.KindFact, map[string]any{"summary": "观察 " + string(rune('A'+i))}, 0, "confirmed", "work#1", nil); err != nil {
			t.Fatal(err)
		}
	}
	out := callTool(t, tools.PlannerTools(), "blackboard_view", `{"limit":1}`)
	var first map[string]any
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatalf("blackboard_view 不是 JSON: %v", err)
	}
	bb, _ := first["blackboard"].(map[string]any)
	if bb == nil {
		t.Fatalf("缺 blackboard 元数据: %s", out)
	}
	if bb["has_more"] != false || bb["has_earlier"] != true {
		t.Fatalf("首屏应取最新一页并标记 has_earlier: %s", out)
	}
	next, _ := bb["next_cursor"].(float64)
	if next <= 0 {
		t.Fatalf("next_cursor 应为节点 id: %+v", bb)
	}
	second := callTool(t, tools.PlannerTools(), "blackboard_view", `{"after":`+jsonNumber(next)+`,"limit":10}`)
	if strings.Contains(second, `"has_more":true`) {
		t.Fatalf("游标之后不该再有更多: %s", second)
	}
}

func jsonNumber(v float64) string {
	b, _ := json.Marshal(int64(v))
	return string(b)
}
