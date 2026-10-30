package db

import (
	"strings"
	"testing"
)

func TestAbandonGoal(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "abandon goal")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	open, err := es.AddGoal(map[string]any{"text": "拿下后台"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	met, err := es.AddGoal(map[string]any{"text": "已达成目标"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := es.SetNodeState(met, "met"); err != nil {
		t.Fatalf("set met: %v", err)
	}

	if _, err := es.AbandonGoal(open, "   "); err == nil || !strings.Contains(err.Error(), "理由") {
		t.Fatalf("空白理由必须被拒，got %v", err)
	}
	changed, err := es.AbandonGoal(open, "目标已下线")
	if err != nil || changed != 1 {
		t.Fatalf("放弃 open 目标: changed=%d err=%v", changed, err)
	}
	node, err := es.GetNode(open)
	if err != nil || node == nil {
		t.Fatalf("读回目标: %v", err)
	}
	if node.State != "abandoned" {
		t.Fatalf("state = %q, want abandoned", node.State)
	}
	if !strings.Contains(string(node.Payload), "目标已下线") {
		t.Fatalf("payload 应保留放弃理由: %s", node.Payload)
	}
	if changed, _ := es.AbandonGoal(open, "再放弃一次"); changed != 0 {
		t.Fatal("已放弃的目标不能重复放弃")
	}
	if changed, _ := es.AbandonGoal(met, "已达成不能放弃"); changed != 0 {
		t.Fatal("已达成(met)的目标不能放弃")
	}
}

// owner 槽位亲和不是硬绑定：owner 槽位当前没有 running 意图时，其它空闲槽位可以接手；
// owner 槽位仍在跑别的意图时保持互斥。否则重启/租约回收/暂停恢复留下的 open 意图会被
// 原槽位“占着”，4 个 worker 长期只跑 1~2 条。
func TestSlotHandoffWhenOwnerIdle(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "slot handoff")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	// 场景 1：意图曾由 work#2 领取、现退回 open，work#2 没有 running → work#1 可领。
	i1, err := es.AddIntent(map[string]any{"summary": "a"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := es.ClaimIntent(i1, "work#2", IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim by work#2: ok=%v err=%v", ok, err)
	}
	if err := es.SetIntentState(i1, "open"); err != nil {
		t.Fatal(err)
	}
	fr, err := es.FrontierForSlot("work#1", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, n := range fr {
		if n.ID == i1 {
			seen = true
		}
	}
	if !seen {
		t.Fatal("owner 槽位空闲时，open 意图应对其它槽位可见")
	}
	if ok, err := es.ClaimIntentForSlot(i1, "work#1", IntentLeaseTTL, nil); err != nil || !ok {
		t.Fatalf("空闲 owner 槽位应允许跨槽领取: ok=%v err=%v", ok, err)
	}

	// 场景 2：work#3 正在跑另一条意图 → 它名下的 open 意图对 work#1 仍不可见。
	i2, _ := es.AddIntent(map[string]any{"summary": "busy"}, 5, nil, "planner")
	if ok, _ := es.ClaimIntent(i2, "work#3", IntentLeaseTTL); !ok {
		t.Fatal("claim i2 by work#3")
	}
	i3, _ := es.AddIntent(map[string]any{"summary": "affinity"}, 5, nil, "planner")
	if ok, _ := es.ClaimIntent(i3, "work#3", IntentLeaseTTL); !ok {
		t.Fatal("claim i3 by work#3")
	}
	if err := es.SetIntentState(i3, "open"); err != nil {
		t.Fatal(err)
	}
	fr, err = es.FrontierForSlot("work#1", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range fr {
		if n.ID == i3 {
			t.Fatal("owner 槽位仍在跑意图时，它的 open 意图不应被其它槽位看到")
		}
	}
	if ok, err := es.ClaimIntentForSlot(i3, "work#1", IntentLeaseTTL, nil); err != nil || ok {
		t.Fatalf("owner 槽位忙时应拒绝跨槽领取: ok=%v err=%v", ok, err)
	}
}
