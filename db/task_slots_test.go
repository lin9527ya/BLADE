package db

import (
	"strconv"
	"testing"
)

func TestTaskWorkerSlotsPersist(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行，所以关库必须注册成 Cleanup——
	// 否则下面的数据清理跑在已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTask("task-slots-test", "slots", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, strconv.FormatInt(task.ID, 10)) })

	if got, err := d.TaskWorkerSlots(strconv.FormatInt(task.ID, 10)); err != nil || got != 0 {
		t.Fatalf("before ensure = %d,%v; want 0,nil", got, err)
	}
	got, err := d.EnsureTaskWorkerSlots(strconv.FormatInt(task.ID, 10), 3)
	if err != nil || got != 3 {
		t.Fatalf("ensure = %d,%v; want 3", got, err)
	}
	// A later global setting change must not resize an already-started task.
	if got, err = d.EnsureTaskWorkerSlots(strconv.FormatInt(task.ID, 10), 7); err != nil || got != 3 {
		t.Fatalf("second ensure = %d,%v; want persisted 3", got, err)
	}
	if got, err = d.TaskWorkerSlots(strconv.FormatInt(task.ID, 10)); err != nil || got != 3 {
		t.Fatalf("read = %d,%v; want 3", got, err)
	}
}

func TestActivitySlotFilterAndTokenBuckets(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行，所以关库必须注册成 Cleanup——
	// 否则下面的数据清理跑在已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = d.Close() })
	expID, err := d.CreateExploration("test", "slot activity")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	intentA, err := es.AddIntent(map[string]any{"summary": "slot one work"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intentB, err := es.AddIntent(map[string]any{"summary": "slot two work"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	s1, s2 := 1, 2
	for i := 0; i < 3; i++ {
		if _, err := es.AppendActivity(Activity{NodeID: &intentA, Worker: "work#1", Kind: "text", Summary: "one", SlotNo: &s1}); err != nil {
			t.Fatal(err)
		}
		if _, err := es.AppendActivity(Activity{NodeID: &intentB, Worker: "work#2", Kind: "text", Summary: "two", SlotNo: &s2}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := es.AppendActivity(Activity{NodeID: &intentA, Worker: "work#1", Kind: "result", Summary: "r1", SlotNo: &s1,
		InputTokens: iptr(10), OutputTokens: iptr(2), CacheReadTokens: iptr(90)}); err != nil {
		t.Fatal(err)
	}
	if _, err := es.AppendActivity(Activity{NodeID: &intentB, Worker: "work#2", Kind: "result", Summary: "r2", SlotNo: &s2,
		InputTokens: iptr(20), OutputTokens: iptr(3), CacheReadTokens: iptr(190)}); err != nil {
		t.Fatal(err)
	}

	items, _, err := es.ActivityPage(ActivitySessionFilter{Slot: &s1}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("slot1 activity = %d, want 4", len(items))
	}
	for _, a := range items {
		if a.SlotNo == nil || *a.SlotNo != 1 {
			t.Fatalf("slot filter leaked: %+v", a)
		}
	}

	usage, err := es.TokenStatsBySession()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, u := range usage {
		got[u.Session] = u.InputTokens
	}
	if got["slot:1"] != 10 || got["slot:2"] != 20 {
		t.Fatalf("token buckets = %+v (want slot:1=10, slot:2=20)", usage)
	}
}

func TestActiveSlotIntentsAndResetKeepsOwner(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行，所以关库必须注册成 Cleanup——
	// 否则下面的数据清理跑在已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = d.Close() })
	expID, err := d.CreateExploration("test", "slot intents")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)
	older, err := es.AddIntent(map[string]any{"summary": "older"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	newer, err := es.AddIntent(map[string]any{"summary": "newer"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	other, err := es.AddIntent(map[string]any{"summary": "slot three"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='running', owner='work#1' WHERE id=$1`, older); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='open', owner='work#1' WHERE id=$1`, newer); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='paused', owner='work#3' WHERE id=$1`, other); err != nil {
		t.Fatal(err)
	}
	// running must win over the newer open row for the same owner.
	nodes, err := es.ActiveSlotIntents([]string{"work#1", "work#2", "work#3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("active slot intents = %d (%+v), want 2", len(nodes), nodes)
	}
	found := map[string]string{}
	for _, n := range nodes {
		found[n.Owner] = n.State
	}
	if found["work#1"] != "running" || found["work#3"] != "paused" {
		t.Fatalf("slot states = %+v, want work#1 running, work#3 paused", found)
	}

	if _, err := es.ResetRunningIntents(); err != nil {
		t.Fatal(err)
	}
	var state, owner string
	if err := d.QueryRow(`SELECT state, COALESCE(owner,'') FROM exploration_nodes WHERE id=$1`, older).Scan(&state, &owner); err != nil {
		t.Fatal(err)
	}
	if state != "open" || owner != "work#1" {
		t.Fatalf("reset = %s/%s, want open/work#1 (owner affinity must survive restart)", state, owner)
	}
}

func iptr(n int) *int { return &n }
