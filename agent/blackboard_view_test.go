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

func TestBlackboardProjectionReadMetadata(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	base, err := ts.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const goalCount = 21
	for range goalCount {
		if _, err := ts.AddGoal(map[string]any{"text": "review observation"}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := ts.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	firstChange := base
	firstChange.Revision++
	stale := base
	stale.Epoch = "previous"
	for _, tc := range []struct {
		name   string
		view   map[string]any
		cursor string
		goals  int
		state  string
	}{
		{"initial", tools.blackboardViewData(0, 1), latest.Cursor(), 20, "truncated"},
		{"forward", tools.blackboardViewData(0, 1, base.Cursor()), firstChange.Cursor(), 20, "truncated"},
		{"delta", tools.blackboardDeltaData(0, 1, base.Cursor()), firstChange.Cursor(), goalCount, "complete"},
		{"resync", tools.blackboardDeltaData(0, 1, stale.Cursor()), latest.Cursor(), 20, "truncated"},
		{"detail", tools.graphOverviewData(), latest.Cursor(), goalCount, "complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.view["revision"] != latest.RevisionString() || tc.view["change_cursor"] != tc.cursor {
				t.Fatalf("revision=%v cursor=%v, want %s and %s", tc.view["revision"], tc.view["change_cursor"], latest.RevisionString(), tc.cursor)
			}
			if tc.name != "detail" {
				board, _ := tc.view["blackboard"].(map[string]any)
				if board["revision"] != tc.view["revision"] || tc.view["resync_required"] != (tc.name == "resync") {
					t.Fatalf("blackboard revision=%v resync_required=%v", board["revision"], tc.view["resync_required"])
				}
			}
			status, _ := tc.view["read_status"].(map[string]any)
			sections, _ := status["sections"].(map[string]db.ReadStatus)
			goals := sections["goals"]
			rows, _ := tc.view["goals"].([]map[string]any)
			if status["state"] != "complete" || goals.State != tc.state || goals.Total == nil || *goals.Total != goalCount ||
				len(rows) != tc.goals || goals.SampleCount != len(rows) {
				t.Fatalf("goals=%d read_status=%+v", len(rows), status)
			}
		})
	}
}

// The legacy after/next_cursor pair tracks node IDs, independently of change_cursor.
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
