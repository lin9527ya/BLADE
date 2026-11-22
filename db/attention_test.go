package db

import (
	"fmt"
	"testing"
)

func TestAttentionWindowsPreferOldestAndRespectChildIntents(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "attention")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	var unconsumed []int64
	for i := 0; i < 3; i++ {
		id, err := es.AddNode(KindFact, map[string]any{
			"summary": fmt.Sprintf("未接手事实 %d", i), "polarity": "pos", "confidence": "observed",
			"leads": []map[string]any{{"text": fmt.Sprintf("线索 %d", i)}},
		}, 0, "confirmed", "worker", nil)
		if err != nil {
			t.Fatal(err)
		}
		unconsumed = append(unconsumed, id)
	}
	consumed, err := es.AddNode(KindFact, map[string]any{"summary": "已接手事实"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := es.AddIntent(map[string]any{"summary": "接手"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if intent == 0 {
		t.Fatal("intent not created")
	}
	if _, err := es.LinkIntentSource(intent, consumed); err != nil {
		t.Fatal(err)
	}

	facts, err := es.OldestUnconsumedFacts(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 || facts[0].ID != unconsumed[0] || facts[1].ID != unconsumed[1] {
		t.Fatalf("facts=%+v want oldest %v", facts, unconsumed)
	}
	if facts[0].Summary != "未接手事实 0" || facts[0].Polarity != "pos" {
		t.Fatalf("row=%+v", facts[0])
	}

	blocked, err := es.AddIntent(map[string]any{"summary": "被阻断的旧方向"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := es.SetNodeState(blocked, "blocked"); err != nil {
		t.Fatal(err)
	}
	gap, err := es.AddNode(KindFinding, map[string]any{"summary": "待补证候选"}, 0, "open_proof_gap", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	unresolved, err := es.OldestUnresolved(5)
	if err != nil {
		t.Fatal(err)
	}
	foundIntent, foundGap := false, false
	for _, u := range unresolved {
		if u.ID == blocked && u.Kind == KindIntent && u.State == "blocked" {
			foundIntent = true
		}
		if u.ID == gap && u.Kind == KindFinding && u.State == "open_proof_gap" {
			foundGap = true
		}
	}
	if !foundIntent || !foundGap {
		t.Fatalf("unresolved=%+v", unresolved)
	}

	report, err := es.LeadTraceReport("", "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.OldestPendingPreview) < 3 {
		t.Fatalf("oldest leads=%d", len(report.OldestPendingPreview))
	}
	if report.OldestPendingPreview[0].FactID != unconsumed[0] || report.OldestPendingPreview[1].FactID != unconsumed[1] {
		t.Fatalf("oldest leads order=%+v", report.OldestPendingPreview[:3])
	}
	if report.OldestPendingPreview[0].FactCreatedAt.After(report.OldestPendingPreview[1].FactCreatedAt) {
		t.Fatal("oldest preview must be ascending by fact age")
	}
}
