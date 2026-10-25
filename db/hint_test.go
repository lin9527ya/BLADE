package db

import (
	"encoding/json"
	"testing"
)

func TestHintLifecycle(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	expID, err := d.CreateExploration("hint lifecycle", "hints")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	older, err := es.AddNode(KindHint, map[string]any{"text": "重点看认证后接口"}, 0, StateHintActive, "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := es.AddNode(KindHint, map[string]any{"text": "试一下上传点"}, 0, StateHintActive, "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	consumed, err := es.AddNode(KindHint, map[string]any{"text": "已经采纳的"}, 0, StateHintConsumed, "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := es.AddIntent(map[string]any{"summary": "not a hint"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	active, err := es.ListByKindState(KindHint, StateHintActive, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active[0].ID != newer || active[1].ID != older {
		t.Fatalf("active hints should be newest-first and exclude consumed: %+v", active)
	}
	one, err := es.ListByKindState(KindHint, StateHintActive, 1)
	if err != nil || len(one) != 1 || one[0].ID != newer {
		t.Fatalf("limit should apply after the state filter: %+v err=%v", one, err)
	}

	if ok, err := es.ConsumeHint(intent, ""); err != nil || ok {
		t.Fatalf("consume must refuse a non-hint: ok=%v err=%v", ok, err)
	}
	if ok, err := es.ConsumeHint(consumed, ""); err != nil || ok {
		t.Fatalf("consume must be idempotent on an already-consumed hint: ok=%v err=%v", ok, err)
	}
	if ok, err := es.ConsumeHint(older, "已派意图 #7 覆盖"); err != nil || !ok {
		t.Fatalf("consume active hint: ok=%v err=%v", ok, err)
	}
	node, err := es.GetNode(older)
	if err != nil || node == nil {
		t.Fatalf("get consumed hint: %v", err)
	}
	if node.State != StateHintConsumed {
		t.Fatalf("state should be consumed, got %q", node.State)
	}
	var payload map[string]any
	_ = json.Unmarshal(node.Payload, &payload)
	if payload["text"] != "重点看认证后接口" || payload["consume_note"] != "已派意图 #7 覆盖" {
		t.Fatalf("payload lost text or note: %+v", payload)
	}

	if err := es.DeleteHint(intent); err == nil {
		t.Fatal("DeleteHint must refuse a non-hint")
	}
	if err := es.DeleteHint(newer); err != nil {
		t.Fatal(err)
	}
	if node, err := es.GetNode(newer); err != nil || node != nil {
		t.Fatalf("hint should be gone after delete: node=%+v err=%v", node, err)
	}
}
