package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBlackboardCredentialHistoricalVerificationAndFreshness(t *testing.T) {
	_, s := intentLeaseFixture(t)
	cred, err := s.AddNode(KindCredential, map[string]any{"summary": "synthetic credential", "targets": []CredentialTarget{{Host: "synthetic.invalid", Verified: true, Evidence: "synthetic success"}}}, 5, StateLootActive, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AddIntent(map[string]any{"summary": "use verified credential", "requires": []IntentRequirement{{Type: "credential", NodeID: cred, Verification: true, Target: "synthetic.invalid"}}}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	issues, err := s.RequirementIssues(id)
	if err != nil || len(issues) != 0 {
		t.Fatalf("legacy verified must remain valid without age: %v %v", issues, err)
	}
	payload := map[string]any{"summary": "fresh verification required", "requires": []IntentRequirement{{Type: "credential", NodeID: cred, Verification: true, Target: "synthetic.invalid", MaxAgeSeconds: 300}}}
	if err := s.SetNodePayload(id, KindIntent, payload); err != nil {
		t.Fatal(err)
	}
	issues, err = s.RequirementIssues(id)
	if err != nil || len(issues) != 1 {
		t.Fatalf("unknown verification time silently fresh: %v %v", issues, err)
	}
	if err := s.SetNodePayload(cred, KindCredential, map[string]any{"summary": "fresh", "targets": []CredentialTarget{{Host: "synthetic.invalid", Verified: true, Evidence: "synthetic success", ObservedAt: time.Now().UTC().Format(time.RFC3339)}}}); err != nil {
		t.Fatal(err)
	}
	issues, err = s.RequirementIssues(id)
	if err != nil || len(issues) != 0 {
		t.Fatalf("fresh verified credential rejected: %v %v", issues, err)
	}
	merged := MergeCredentialTarget([]CredentialTarget{{Host: "synthetic.invalid", Verified: true, Evidence: "old"}}, CredentialTarget{Host: "synthetic.invalid", Verified: true, Evidence: "new", ObservedAt: "2026-11-09T00:00:00Z", ValidUntil: "2026-11-10T00:00:00Z"})
	if merged[0].ObservedAt == "" || merged[0].ValidUntil == "" {
		t.Fatal("verification metadata lost on merge")
	}
}

func TestBlackboardStableSessionAndStaleWriteAuthority(t *testing.T) {
	d, s := intentLeaseFixture(t)
	id, err := s.AddIntent(map[string]any{"summary": "resume twice"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimNode(t.Context(), id, "work#1", "open", IntentLeaseTTL, nil, false)
	if err != nil || first == nil {
		t.Fatalf("claim: %v", err)
	}
	stale := s.WithAuthority(first.WriteAuthority(0, s.ID()))
	if _, err := stale.AddFactGraph(map[string]any{"summary": "first generation"}, "work#1", nil, id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIntentState(id, "paused"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLADE_WORKER_PER_INTENT_SESSION", "1")
	second, err := s.ClaimNode(t.Context(), id, "work#2", "paused", IntentLeaseTTL, nil, false)
	if err != nil || second == nil {
		t.Fatalf("second claim: %v", err)
	}
	if second.LogicalSessionID != first.LogicalSessionID || second.ExecutionGeneration != first.ExecutionGeneration+1 {
		t.Fatalf("identity drift: first=%+v second=%+v", first, second)
	}
	if err := s.SetIntentState(id, "open"); err != nil {
		t.Fatal(err)
	}
	third, err := s.ClaimNode(t.Context(), id, "work#3", "open", IntentLeaseTTL, nil, false)
	if err != nil || third == nil {
		t.Fatalf("third claim: %v", err)
	}
	if third.LogicalSessionID != first.LogicalSessionID || third.ExecutionGeneration != 3 {
		t.Fatalf("second recovery drift: %+v", third)
	}
	if _, err := stale.AddFactGraph(map[string]any{"summary": "late orphan"}, "work#1", nil, id, nil); !errors.Is(err, ErrStaleExecution) {
		t.Fatalf("late fact: %v", err)
	}
	if _, err := stale.TouchIntentLease(id, first.Owner, IntentLeaseTTL); !errors.Is(err, ErrStaleExecution) {
		t.Fatalf("late lease: %v", err)
	}
	if err := stale.SettleIntent(id, "done", nil, ""); !errors.Is(err, ErrStaleExecution) {
		t.Fatalf("late settlement: %v", err)
	}
	if err := stale.SetIntentSemanticOutcome(id, "negative", "late"); !errors.Is(err, ErrStaleExecution) {
		t.Fatalf("late semantic conclusion: %v", err)
	}
	a := first.WriteAuthority(0, s.ID())
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := RecordFindingTx(t.Context(), tx, RecordFindingInput{Authority: &a, ExplorationID: s.ID(), IntentID: id}, nil); !errors.Is(err, ErrStaleExecution) {
		t.Fatalf("late finding: %v", err)
	}
	_ = tx.Rollback()
	if rows, err := d.WithAuthority(a).Query(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state)VALUES($1,'fact','{}','confirmed')RETURNING id`, s.ID()); !errors.Is(err, ErrStaleExecution) {
		if rows != nil {
			rows.Close()
		}
		t.Fatalf("Query bypasses fence: %v", err)
	}
	current := s.WithAuthority(third.WriteAuthority(0, s.ID()))
	if err := current.SettleIntent(id, "done", []int64{123}, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(id)
	if err != nil || got.State != "done" || !strings.Contains(string(got.Payload), "validated_asset_ids") {
		t.Fatalf("atomic settlement missing: %+v %v", got, err)
	}
}

// 非 verify 意图的 validated 是 nil；结算不能被 jsonb_array_length('null')
// 拒绝（v0.9.8 回归：意图永远停在 running，随后被规划者当陈旧队列丢弃）。
func TestSettleIntentNilValidated(t *testing.T) {
	_, s := intentLeaseFixture(t)
	id, err := s.AddIntent(map[string]any{"summary": "non-verify settlement"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimNode(t.Context(), id, "work#1", "open", IntentLeaseTTL, nil, false)
	if err != nil || n == nil {
		t.Fatalf("claim: %v %v", n, err)
	}
	current := s.WithAuthority(n.WriteAuthority(0, s.ID()))
	if err := current.SettleIntent(id, "done", nil, ""); err != nil {
		t.Fatalf("nil validated settlement failed: %v", err)
	}
	got, err := s.GetNode(id)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.State != "done" {
		t.Fatalf("state = %s, want done", got.State)
	}
	if strings.Contains(string(got.Payload), "validated_asset_ids") {
		t.Fatalf("nil validated must not add validated_asset_ids: %s", got.Payload)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["last_outcome"] != "done" {
		t.Fatalf("last_outcome = %v, want done", payload["last_outcome"])
	}
}

func TestBlackboardSnapshotNeverHidesQueryOrScanErrors(t *testing.T) {
	d, _ := intentLeaseFixture(t)
	cases := []func(*DB){
		func(v *DB) { _, _ = v.Query(`SELECT missing_column FROM exploration_nodes`) },
		func(v *DB) { var n int; _ = v.QueryRow(`SELECT 'not an integer'`).Scan(&n) },
		func(v *DB) {
			rows, err := v.Query(`SELECT 'not an integer'`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var n int
				_ = rows.Scan(&n)
			}
		},
	}
	for i, run := range cases {
		err := d.ReadSnapshot(t.Context(), func(v *DB) error { run(v); return nil })
		if err == nil {
			t.Fatalf("case %d converted read error to success", i)
		}
	}
}

func TestBlackboardJournalCommitOrderCursorResyncAndPrivacy(t *testing.T) {
	d, s := intentLeaseFixture(t)
	base, err := s.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	slow, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Rollback()
	var slowID int64
	if err := slow.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state)VALUES($1,'fact','{"secret":"PRIVATE_SENTINEL"}','confirmed')RETURNING id`, s.ID()).Scan(&slowID); err != nil {
		t.Fatal(err)
	}
	fastID, err := s.AddFactGraph(map[string]any{"summary": "commits first"}, "test", nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := s.BlackboardChanges(t.Context(), base.Cursor(), 100)
	if err != nil || len(mid.Changes) != 1 || *mid.Changes[0].NodeID != fastID {
		t.Fatalf("uncommitted event visible: %+v %v", mid, err)
	}
	if err := slow.Commit(); err != nil {
		t.Fatal(err)
	}
	end, err := s.BlackboardChanges(t.Context(), mid.Cursor, 100)
	if err != nil || len(end.Changes) != 1 || *end.Changes[0].NodeID != slowID {
		t.Fatalf("late commit lost: %+v %v", end, err)
	}
	raw, _ := json.Marshal(end)
	if strings.Contains(string(raw), "PRIVATE_SENTINEL") {
		t.Fatal("journal copied secret")
	}
	bad, err := s.BlackboardChanges(t.Context(), fmt.Sprintf("ev1:%d:old:0", s.ID()), 10)
	if err != nil || !bad.Resync {
		t.Fatalf("epoch mismatch not resynced: %+v %v", bad, err)
	}
	if _, err := s.BlackboardChanges(t.Context(), "ev1:999999999:old:0", 10); err == nil {
		t.Fatal("cross-task cursor accepted")
	}
	if _, err := d.Exec(`DELETE FROM blackboard_events WHERE exploration_id=$1 AND seq>$2`, s.ID(), base.Revision); err != nil {
		t.Fatal(err)
	}
	bad, err = s.BlackboardChanges(t.Context(), base.Cursor(), 10)
	if err != nil || !bad.Resync {
		t.Fatalf("missing history not resynced: %+v %v", bad, err)
	}
}

func TestBlackboardRequiresRejectsMalformedAndForeignReferences(t *testing.T) {
	_, s := intentLeaseFixture(t)
	_, other := intentLeaseFixture(t)
	foreign, err := other.AddFactGraph(map[string]any{"summary": "foreign"}, "test", nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddIntent(map[string]any{"summary": "foreign requirement", "requires": []IntentRequirement{{Type: "fact", NodeID: foreign}}}, 5, nil, "test"); err == nil {
		t.Fatal("foreign requirement accepted")
	}
	if err := s.ValidateEvidenceMetadata(&EvidenceMetadata{Sources: []EvidenceSource{{Type: "activity", ID: 999999999999}}}); err == nil {
		t.Fatal("nonexistent evidence accepted")
	}
	for _, raw := range []string{`null`, `{"type":"fact"}`, `[{"type":"credential","node_id":"garbage","verification":"bad"}]`, `[{"type":"fact","node_id":999999999999999999999999}]`} {
		var ready bool
		err := s.db.QueryRow(`SELECT blackboard_intent_ready(jsonb_build_object('requires',$1::jsonb),$2::bigint)`, raw, s.ID()).Scan(&ready)
		if err != nil || ready {
			t.Fatalf("malformed requires not safely blocked: %s ready=%v err=%v", raw, ready, err)
		}
	}
}

func TestBlackboardJournalCrossTaskFinalizationAndProtocol(t *testing.T) {
	d, left := intentLeaseFixture(t)
	_, right := intentLeaseFixture(t)
	beforeLeft, _ := left.BlackboardVersion(t.Context())
	beforeRight, _ := right.BlackboardVersion(t.Context())
	a, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Rollback()
	b, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Rollback()
	insert := func(tx *sql.Tx, exp int64) {
		t.Helper()
		if _, err := tx.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state)VALUES($1,'fact','{}','confirmed')`, exp); err != nil {
			t.Fatal(err)
		}
	}
	insert(a, right.ID())
	insert(a, left.ID())
	insert(b, left.ID())
	insert(b, right.ID())
	done := make(chan error, 2)
	go func() { done <- a.Commit() }()
	go func() { done <- b.Commit() }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reverse task version lock order deadlocked")
		}
	}
	for _, v := range []BlackboardVersion{beforeLeft, beforeRight} {
		got, err := d.Exploration(v.ExplorationID).BlackboardVersion(t.Context())
		if err != nil || got.Revision != v.Revision+2 {
			t.Fatalf("version lost cross-task events: %+v %v", got, err)
		}
	}
	raw, err := sql.Open("pgx", testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state)VALUES($1,'fact','{}','confirmed')`, left.ID()); err == nil || !strings.Contains(err.Error(), "writer protocol 2 required") {
		t.Fatalf("old writer not rejected: %v", err)
	}
}

func TestBlackboardLargeLedgersCountBeyondSamples(t *testing.T) {
	d, s := intentLeaseFixture(t)
	if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state)SELECT $1,'fact',jsonb_build_object('summary','synthetic lead','leads',jsonb_build_array(jsonb_build_object('text','synthetic follow-up'))),'confirmed' FROM generate_series(1,2105)`, s.ID()); err != nil {
		t.Fatal(err)
	}
	if n, err := s.OpenLeadsCount(); err != nil || n != 2105 {
		t.Fatalf("lead window mistaken for total: %d %v", n, err)
	}
	if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state)SELECT $1,'credential','{"summary":"synthetic active loot"}','active' FROM generate_series(1,505)`, s.ID()); err != nil {
		t.Fatal(err)
	}
	chains, err := s.ChainLedger()
	if err != nil || len(chains.Rows) != 505 {
		t.Fatalf("chain ledger clipped at 500: %v %v", chains, err)
	}
	gaps, err := s.CompletionGaps(nil, CompletionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range gaps {
		if g.Kind == GapChain {
			found = strings.Contains(g.Detail, "505 条")
		}
	}
	if !found {
		t.Fatalf("five examples mistaken for total: %+v", gaps)
	}
}

func TestBlackboardFreshExplorationHasEmptyBaseline(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("fresh baseline", "no graph writes yet")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, exp) })
	s := d.Exploration(exp)
	v, err := s.BlackboardVersion(t.Context())
	if err != nil || v.Epoch == "" {
		t.Fatalf("fresh version: %+v %v", v, err)
	}
	// Creation is itself a captured graph change; a fresh exploration therefore has
	// exactly one baseline event rather than an unreadable missing row.
	changes, err := s.BlackboardChanges(t.Context(), "", 10)
	if err != nil || changes.Status.State != "complete" || len(changes.Changes) != 1 ||
		changes.Changes[0].Entity != "exploration" || v.Revision != 1 {
		t.Fatalf("fresh changes: %+v %v", changes, err)
	}
}
