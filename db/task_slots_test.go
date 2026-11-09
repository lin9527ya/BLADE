package db

import (
	"encoding/json"
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

// 重启时必须优先领取“真正 running 过”的意图，并带上原槽位，worker 才能续跑
// 原槽位 transcript；普通 open 排队意图不能因为 priority 更高而抢走。
func TestRestartResumeMarksInterruptedIntent(t *testing.T) {
	d, st := intentLeaseFixture(t)
	interrupted, err := st.AddIntent(map[string]any{"summary": "interrupted"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := st.AddIntent(map[string]any{"summary": "queued"}, 9, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='running', owner='work#2' WHERE id=$1`, interrupted); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='open', owner='work#2' WHERE id=$1`, queued); err != nil {
		t.Fatal(err)
	}
	if n, err := st.ResetRunningIntents(); err != nil || n != 1 {
		t.Fatalf("reset: n=%d err=%v", n, err)
	}
	node, err := st.GetNode(interrupted)
	if err != nil || node == nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(node.Payload, &payload)
	if payload["resume_after_restart"] != true || payload["resume_slot"] != float64(2) {
		t.Fatalf("interrupted intent missing resume marker: %+v", payload)
	}
	// Marker beats a higher-priority queued intent and even allows another slot to
	// pick it up after a worker-count change.
	fr, err := st.FrontierForSlot("work#1", []string{"work#1", "work#2"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr) == 0 || fr[0].ID != interrupted {
		for _, n := range fr {
			t.Logf("frontier node id=%d state=%s owner=%s payload=%s", n.ID, n.State, n.Owner, string(n.Payload))
		}
		t.Fatalf("frontier must put interrupted intent first (interrupted=%d): %+v", interrupted, fr)
	}
	ok, err := st.ClaimIntentForSlot(interrupted, "work#1", IntentLeaseTTL, []string{"work#1", "work#2"})
	if err != nil || !ok {
		t.Fatalf("claim interrupted: ok=%v err=%v", ok, err)
	}
	node, _ = st.GetNode(interrupted)
	var after map[string]any
	_ = json.Unmarshal(node.Payload, &after)
	if _, ok := after["resume_after_restart"]; ok {
		t.Fatalf("claim must clear resume marker: %+v", after)
	}
	if _, ok := after["resume_slot"]; ok {
		t.Fatalf("claim must clear resume_slot: %+v", after)
	}
}

// 优雅关停会等 run 收尾，running 行在进程退出前已变成 open；启动时的
// ResetRunningIntents 只认 running 行，兜不到。关停分支必须自己盖续跑标记，
// 否则重启后换槽领取会丢原槽位 transcript，只能从头探索。
func TestParkIntentForRestartMarksSlot(t *testing.T) {
	d, st := intentLeaseFixture(t)
	id, err := st.AddIntent(map[string]any{"summary": "shutdown mid-run"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='running', owner='work#3' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	changed, err := st.ParkIntentForRestart(id, 3)
	if err != nil || !changed {
		t.Fatalf("park: changed=%v err=%v", changed, err)
	}
	node, err := st.GetNode(id)
	if err != nil || node == nil {
		t.Fatal(err)
	}
	if node.State != "open" {
		t.Fatalf("state = %s, want open", node.State)
	}
	var payload map[string]any
	_ = json.Unmarshal(node.Payload, &payload)
	if payload["resume_after_restart"] != true || payload["resume_slot"] != float64(3) {
		t.Fatalf("parked intent missing resume marker: %+v", payload)
	}
	// 已不是 running 的行不能被重复标记（CAS）。
	if changed, err := st.ParkIntentForRestart(id, 3); err != nil || changed {
		t.Fatalf("second park must be a no-op: changed=%v err=%v", changed, err)
	}
}
