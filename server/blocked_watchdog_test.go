package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// blocked-only 看门狗只应在阻塞签名变化时唤醒 planner，不能每 30s 重复烧规划轮。
func TestSweepBlockedIntentsWakesOncePerSignature(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("blocked-watch-%d", time.Now().UnixNano()), "goal", db.TaskCreateOptions{AssetScopePolicy: db.AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	pre, err := ts.AddIntent(map[string]any{"summary": "前置"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AddIntentGraph(map[string]any{
		"summary": "下游", "depends_on": []int64{pre}, "depends_on_mode": "strict",
	}, 5, nil, "planner", []db.IntentEdge{{From: 0, To: pre, Rel: db.RelDependsOn}}); err != nil {
		t.Fatal(err)
	}
	// 前置暂停：down 被 strict 挡住，且没有可领的 open（blocked-only）。
	if err := ts.SetIntentState(pre, "paused"); err != nil {
		t.Fatal(err)
	}

	e := &Engine{}
	tk := &Task{ID: fmt.Sprint(task.ID), Store: ts, notify: make(chan struct{}, 1)}
	e.sweepBlockedIntents(tk)
	select {
	case <-tk.notify:
	default:
		t.Fatal("首次 blocked-only 应唤醒 planner")
	}
	e.sweepBlockedIntents(tk)
	select {
	case <-tk.notify:
		t.Fatal("签名未变不应重复唤醒")
	default:
	}

	// 新增一个同样被挡的下游：阻塞集合变化 → 签名变化 → 再唤醒一次。
	if _, err := ts.AddIntentGraph(map[string]any{
		"summary": "下游 2", "depends_on": []int64{pre}, "depends_on_mode": "strict",
	}, 5, nil, "planner", []db.IntentEdge{{From: 0, To: pre, Rel: db.RelDependsOn}}); err != nil {
		t.Fatal(err)
	}
	e.sweepBlockedIntents(tk)
	select {
	case <-tk.notify:
	default:
		t.Fatal("阻塞集合变化应改变签名并再次唤醒")
	}
}
