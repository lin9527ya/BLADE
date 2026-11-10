package agent

import (
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 血缘 kind：parents 里混着 goal/fact 时，planner 要能分清。
func TestOverviewParentKinds(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	fact, err := ts.AddNode(db.KindFact, map[string]any{"summary": "入口观察", "polarity": "positive"}, 5, "confirmed", "work#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"auth","summary":"对 /login 测认证","parent_ids":[`+strconv.FormatInt(fact, 10)+`]}`)
	if !strings.Contains(out, "intent created") {
		t.Fatalf("add_intent 应成功：%s", out)
	}
	data := tools.graphOverviewData()
	items, _ := data["open_intents"].([]map[string]any)
	var target map[string]any
	for _, it := range items {
		if sv, _ := it["summary"].(string); strings.Contains(sv, "测认证") {
			target = it
		}
	}
	if target == nil {
		t.Fatalf("没找到新建的意图：%+v", items)
	}
	pk, _ := target["parent_kinds"].(map[string]string)
	if pk[strconv.FormatInt(fact, 10)] != "fact" {
		t.Fatalf("parent_kinds 应标出 fact：%+v", target)
	}
}

// 被终止的意图要出现在 recent_done_intents，否则会被当成「没派过」重派。
func TestStoppedIntentVisibleInOverview(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	if err := ts.SetIntentState(intent, "stopped"); err != nil {
		t.Fatal(err)
	}
	data := tools.graphOverviewData()
	items, _ := data["recent_done_intents"].([]map[string]any)
	for _, it := range items {
		if id, ok := it["id"].(int64); ok && id == intent {
			if st, _ := it["state"].(string); st == "stopped" {
				return
			}
		}
	}
	t.Fatalf("stopped 意图应出现在 recent_done_intents：%+v", items)
}
