package agent

import (
	"fmt"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// attention_* 是最老/久未复查的全图召回窗口：规划者能看到落在主窗口之外的
// 老事实与未闭合意图，worker 视角则不应该拿到这些派单队列。
func TestGraphOverviewAttentionWindows(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("attention", "goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)

	var first int64
	for i := 0; i < 6; i++ {
		id, err := ts.AddNode(db.KindFact, map[string]any{
			"summary": fmt.Sprintf("未被接手的事实 %d", i), "polarity": "pos", "confidence": "observed",
			"leads": []map[string]any{{"text": fmt.Sprintf("旧线索 %d", i)}},
		}, 0, "confirmed", "worker", nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = id
		}
	}
	blocked, err := ts.AddIntent(map[string]any{"summary": "很老的阻断方向"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.SetNodeState(blocked, "exhausted"); err != nil {
		t.Fatal(err)
	}

	tools := NewToolSet(ts, "planner")
	view := tools.graphOverviewData()
	facts, _ := view["attention_oldest_facts"].([]map[string]any)
	if len(facts) != 5 {
		t.Fatalf("attention facts=%d want cap 5", len(facts))
	}
	if id, _ := facts[0]["id"].(int64); id != first {
		t.Fatalf("oldest fact id=%v want %d", facts[0]["id"], first)
	}
	if items, _ := view["attention_oldest_leads"].([]map[string]any); len(items) != 5 {
		t.Fatalf("attention leads=%d", len(items))
	}
	if items, _ := view["attention_oldest_unresolved"].([]map[string]any); len(items) == 0 {
		t.Fatal("old exhausted intent missing from attention window")
	}
	if status, _ := view["attention_status"].(map[string]any); status["state"] != "complete" {
		t.Fatalf("attention status=%v", view["attention_status"])
	}

	worker := tools.graphOverviewDataForWorker()
	for _, key := range []string{"attention_oldest_facts", "attention_oldest_leads", "attention_oldest_unresolved", "attention_status"} {
		if _, ok := worker[key]; ok {
			t.Fatalf("worker overview leaked planner attention key %q", key)
		}
	}
}

// delta 轮不应反复重发周期性全图召回窗口；它们只随周期性全量对账出现。
func TestBlackboardDeltaOmitsPeriodicAttentionWindows(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("attention-delta", "goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)
	if _, err := ts.AddNode(db.KindFact, map[string]any{
		"summary": "有线索的事实", "polarity": "positive", "confidence": "observed",
		"leads": []map[string]any{{"text": "旁路线索"}},
	}, 0, "confirmed", "worker", nil); err != nil {
		t.Fatal(err)
	}
	v, err := ts.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	delta := NewToolSet(ts, "planner").blackboardDeltaData(0, 20, v.Cursor())
	for _, key := range []string{"attention_oldest_leads", "attention_oldest_unresolved", "attention_note", "attention_status"} {
		if _, ok := delta[key]; ok {
			t.Fatalf("delta kept periodic attention key %q", key)
		}
	}
	if _, ok := delta["totals"]; !ok {
		t.Fatal("delta dropped totals")
	}
}
