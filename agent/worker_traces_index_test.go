package agent

import (
	"encoding/json"
	"fmt"
	"testing"
)

// 上百意图的任务里，recent_done_intents 只是一窗；planner 必须能翻全量已跑意图，
// 否则会把试过的方向当新方向重派。这里验证分页与总数口径。
func TestListWorkerTracesPaginates(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	for i := 0; i < 12; i++ {
		id, err := ts.AddIntent(map[string]any{"summary": fmt.Sprintf("方向 %02d", i), "risk_area": "other"}, 5, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := ts.SetIntentState(id, "done"); err != nil {
			t.Fatal(err)
		}
	}
	call := func(body string) map[string]any {
		t.Helper()
		out := callTool(t, tools.PlannerTools(), "list_worker_traces", body)
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("解析失败：%s", out)
		}
		return m
	}

	page1 := call(`{"limit":5}`)
	works, _ := page1["works"].([]any)
	if len(works) != 5 {
		t.Fatalf("第一页应有 5 条，got %d", len(works))
	}
	if total, _ := page1["total"].(float64); int(total) != 12 {
		t.Fatalf("total 应为 12（全部已跑意图），got %v", page1["total"])
	}
	next, ok := page1["next_before"].(float64)
	if !ok || next <= 0 {
		t.Fatalf("应返回 next_before 游标，got %v", page1["next_before"])
	}

	page2 := call(fmt.Sprintf(`{"limit":5,"before":%d}`, int64(next)))
	works2, _ := page2["works"].([]any)
	if len(works2) != 5 {
		t.Fatalf("第二页应有 5 条，got %d", len(works2))
	}
	// 两页不能有重叠
	ids1 := map[float64]bool{}
	for _, w := range works {
		m := w.(map[string]any)
		ids1[m["intent_id"].(float64)] = true
	}
	for _, w := range works2 {
		m := w.(map[string]any)
		if ids1[m["intent_id"].(float64)] {
			t.Fatalf("分页重叠：%v", m["intent_id"])
		}
	}

	// 关键字过滤后 total 跟着变（不是全量）。
	filtered := call(`{"q":"方向 03"}`)
	if total, _ := filtered["total"].(float64); int(total) != 1 {
		t.Fatalf("过滤后 total 应为 1，got %v", filtered["total"])
	}
}

// 上千条已跑意图的窗口问题：recent_done_intents 必须比旧版（12）更宽，否则大任务里
// 大部分已结束意图既不在折叠摘要、也不在常规视野。
func TestRecentDoneIntentWindowIsWideEnough(t *testing.T) {
	if recentDoneCap < 30 {
		t.Fatalf("recent_done_intents 窗口应 ≥30，got %d", recentDoneCap)
	}
}
