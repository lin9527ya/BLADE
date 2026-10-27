package db

import "testing"

// intentLeaseFixture builds one task + exploration and returns its store.
func intentLeaseFixture(t *testing.T) (*DB, *ExplorationStore) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	task, err := d.CreateTask("intent-lease-test", "lease goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var expID int64
	if err := d.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task.ID).Scan(&expID); err != nil {
		t.Fatalf("exploration for task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		d.Close()
	})
	return d, d.Exploration(expID)
}

// TestIntentLeaseClaimRenewReap walks the lease lifecycle: claim stamps a lease,
// renewal is owner-guarded, a lapsed lease is listed and reclaimed exactly once,
// and the ownership guard protects a renewed lease from a stale reap.
func TestIntentLeaseClaimRenewReap(t *testing.T) {
	d, st := intentLeaseFixture(t)
	id, err := st.AddIntent(map[string]any{"summary": "lease lifecycle"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(id, "work#1", IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if ok, err := st.ClaimIntent(id, "work#2", IntentLeaseTTL); err != nil || ok {
		t.Fatalf("second claim must fail: ok=%v err=%v", ok, err)
	}
	var leased bool
	if err := d.QueryRow(`SELECT heartbeat_at IS NOT NULL AND lease_expires_at > now() FROM exploration_nodes WHERE id=$1`, id).Scan(&leased); err != nil || !leased {
		t.Fatalf("claim must stamp a live lease: leased=%v err=%v", leased, err)
	}
	if expired, err := st.ExpiredIntentLeases(); err != nil || len(expired) != 0 {
		t.Fatalf("fresh lease must not be listed: %+v err=%v", expired, err)
	}
	if ok, err := st.TouchIntentLease(id, "work#9", IntentLeaseTTL); err != nil || ok {
		t.Fatalf("renewal by wrong owner must fail: ok=%v err=%v", ok, err)
	}
	if ok, err := st.TouchIntentLease(id, "work#1", IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("renewal by owner: ok=%v err=%v", ok, err)
	}
	// 心跳停止：把租约拨回过去。
	if _, err := d.Exec(`UPDATE exploration_nodes SET heartbeat_at=now()-interval '30 minutes', lease_expires_at=now()-interval '15 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	expired, err := st.ExpiredIntentLeases()
	if err != nil || len(expired) != 1 || expired[0].IntentID != id || expired[0].Owner != "work#1" || expired[0].LastSeen == nil {
		t.Fatalf("lapsed lease must be listed: %+v err=%v", expired, err)
	}
	if ok, err := st.ReopenExpiredIntent(id, "work#9"); err != nil || ok {
		t.Fatalf("reap by wrong owner must fail: ok=%v err=%v", ok, err)
	}
	if ok, err := st.ReopenExpiredIntent(id, "work#1"); err != nil || !ok {
		t.Fatalf("reap: ok=%v err=%v", ok, err)
	}
	node, err := st.GetNode(id)
	if err != nil || node == nil {
		t.Fatalf("node: %+v err=%v", node, err)
	}
	// Owner is kept on reap: the slot that had the transcript should re-claim it,
	// preserving the reusable session/cache (affinity), not drift to another slot.
	if node.State != "open" || node.Owner != "work#1" {
		t.Fatalf("reclaimed intent: state=%s owner=%q", node.State, node.Owner)
	}
	if ok, err := st.ReopenExpiredIntent(id, "work#1"); err != nil || ok {
		t.Fatalf("second reap must fail: ok=%v err=%v", ok, err)
	}
	if expired, err := st.ExpiredIntentLeases(); err != nil || len(expired) != 0 {
		t.Fatalf("no expired leases after reap: %+v err=%v", expired, err)
	}
}

// TestIntentLeaseClearedByTransitions pins that a state transition ends the lease:
// a settled intent (CAS) and a restart reset (ResetRunningIntents) both drop it, so
// the watchdog never reaps a row whose work already finished.
func TestIntentLeaseClearedByTransitions(t *testing.T) {
	d, st := intentLeaseFixture(t)
	done, err := st.AddIntent(map[string]any{"summary": "settles to done"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(done, "work#1", IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if ok, err := st.CompareAndSetIntentState(done, "running", "done"); err != nil || !ok {
		t.Fatalf("settle: ok=%v err=%v", ok, err)
	}
	var leaseLeft bool
	if err := d.QueryRow(`SELECT lease_expires_at IS NOT NULL FROM exploration_nodes WHERE id=$1`, done).Scan(&leaseLeft); err != nil || leaseLeft {
		t.Fatalf("CAS must clear the lease: left=%v err=%v", leaseLeft, err)
	}
	reset, err := st.AddIntent(map[string]any{"summary": "reset on restart"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(reset, "work#1", IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if n, err := st.ResetRunningIntents(); err != nil || n != 1 {
		t.Fatalf("reset: n=%d err=%v", n, err)
	}
	// 重启重置后即便租约字段残留也不该被看门狗盯上：状态已非 running。
	if expired, err := st.ExpiredIntentLeases(); err != nil || len(expired) != 0 {
		t.Fatalf("no expired leases after reset: %+v err=%v", expired, err)
	}
	node, err := st.GetNode(reset)
	if err != nil || node == nil || node.State != "open" {
		t.Fatalf("reset intent: %+v err=%v", node, err)
	}
}
