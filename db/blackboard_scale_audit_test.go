package db

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestWriteAuthorityConcurrencyAndReadOnly(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err := d.CreateTask("concurrent authority fixture", "concurrent writes", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)

	es := d.Exploration(tk.ExplorationID)
	i1, err := es.AddIntent(map[string]any{"summary": "intent 1"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	i2, err := es.AddIntent(map[string]any{"summary": "intent 2"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	n1, err := es.ClaimNode(t.Context(), i1, "worker#1", "open", IntentLeaseTTL, nil, false)
	if err != nil || n1 == nil {
		t.Fatalf("claim 1: %+v %v", n1, err)
	}
	n2, err := es.ClaimNode(t.Context(), i2, "worker#2", "open", IntentLeaseTTL, nil, false)
	if err != nil || n2 == nil {
		t.Fatalf("claim 2: %+v %v", n2, err)
	}
	a1 := n1.WriteAuthority(tk.ID, tk.ExplorationID)
	a2 := n2.WriteAuthority(tk.ID, tk.ExplorationID)

	// Read-only Query while worker 1 holds a write transaction MUST not block or deadlock.
	tx1, err := d.WithAuthority(a1).BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback()

	// Concurrent worker 2 holding another authority tx concurrently on the SAME task
	// must acquire FOR KEY SHARE without waiting for tx1 to commit/rollback!
	done := make(chan error, 1)
	go func() {
		var ok int
		if err := d.WithAuthority(a2).QueryRowContext(t.Context(), `SELECT 1`).Scan(&ok); err != nil {
			done <- err
			return
		}
		tx2, err := d.WithAuthority(a2).BeginTx(t.Context(), nil)
		if err != nil {
			done <- err
			return
		}
		_, err = tx2.ExecContext(t.Context(), `INSERT INTO exploration_nodes(exploration_id,kind,payload,state) VALUES($1,'fact','{"summary":"concurrent"}','confirmed')`, tk.ExplorationID)
		if err != nil {
			_ = tx2.Rollback()
			done <- err
			return
		}
		done <- tx2.Commit()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent worker blocked or failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker 2 serialized behind worker 1 on tasks lock")
	}

	// Open read-only Rows query held open must not serialize or deadlock same-task reads.
	rows, err := es.WithAuthority(a2).db.QueryContext(t.Context(), `SELECT id FROM exploration_nodes WHERE exploration_id=$1 ORDER BY id`, tk.ExplorationID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var cnt int
		_ = d.WithAuthority(a2).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM exploration_nodes WHERE exploration_id=$1`, tk.ExplorationID).Scan(&cnt)
	}()
	wg.Wait()
	_ = rows.Close()
}

func TestColdStampEventSilence(t *testing.T) {
	d, s := intentLeaseFixture(t)
	id, err := s.AddFactGraph(map[string]any{"summary": "cold fact"}, "test", nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyStampOps([]StampOp{{ID: id, Set: true, Round: 7}}); err != nil {
		t.Fatal(err)
	}
	after, err := s.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("cold_since_round stamp emitted blackboard event: %d -> %d", before.Revision, after.Revision)
	}
	_ = d
}

func TestGoalCompletePrunesPausedNonCleanupAndReopenBumpsVersion(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err := d.CreateTask("paused prune fixture", "prune paused on met", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)

	if _, err := es.AddNode(KindGoal, map[string]any{"text": "target goal"}, 0, "met", "test", nil); err != nil {
		t.Fatal(err)
	}
	pausedNormal := mustIntent(t, es, "暂停中的非清理意图")
	if err := es.SetIntentState(pausedNormal, "paused"); err != nil {
		t.Fatal(err)
	}
	pausedCleanup, err := es.AddIntent(map[string]any{"summary": "暂停中的清理意图", "cleanup": true}, 10, nil, "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := es.SetIntentState(pausedCleanup, "paused"); err != nil {
		t.Fatal(err)
	}
	beforeVersionNode, _ := es.GetNode(pausedNormal)

	won, pruned, err := d.SetGoalsCompleteGuardedWithPruned(tk.ID)
	if err != nil || !won {
		t.Fatalf("SetGoalsCompleteGuardedWithPruned: won=%v err=%v", won, err)
	}
	if len(pruned) != 1 || pruned[0] != pausedNormal {
		t.Fatalf("pruned=%v, want [%d]", pruned, pausedNormal)
	}
	afterPaused, _ := es.GetNode(pausedNormal)
	if afterPaused.State != StateIntentDeleted || afterPaused.ContentVersion <= beforeVersionNode.ContentVersion {
		t.Fatalf("paused non-cleanup not deleted or version not bumped: %+v", afterPaused)
	}
	if keep, _ := es.GetNode(pausedCleanup); keep == nil || keep.State != "paused" {
		t.Fatalf("paused cleanup intent should remain paused: %+v", keep)
	}

	// ReopenContentVersion bump + RevertUnstartedClaim.
	blockedID := mustIntent(t, es, "blocked intent")
	if err := es.SetIntentState(blockedID, "blocked"); err != nil {
		t.Fatal(err)
	}
	beforeReopen, _ := es.GetNode(blockedID)
	reopened, err := es.ReopenIntent(blockedID)
	if err != nil || !reopened {
		t.Fatalf("ReopenIntent: %v %v", reopened, err)
	}
	afterReopen, _ := es.GetNode(blockedID)
	if afterReopen.ContentVersion <= beforeReopen.ContentVersion {
		t.Fatalf("ReopenIntent did not bump content_version: %d -> %d", beforeReopen.ContentVersion, afterReopen.ContentVersion)
	}
	claimed, err := es.ClaimNode(context.Background(), blockedID, "worker#test", "open", IntentLeaseTTL, nil, false)
	if err != nil || claimed == nil {
		t.Fatalf("claim failed: %+v %v", claimed, err)
	}
	reverted, err := es.RevertUnstartedClaim(blockedID, claimed.Owner, claimed.ExecutionGeneration)
	if err != nil || !reverted {
		t.Fatalf("RevertUnstartedClaim: %v %v", reverted, err)
	}
	if got, _ := es.GetNode(blockedID); got == nil || got.State != "open" {
		t.Fatalf("reverted state=%+v, want open", got)
	}
}

func TestSetGoallessDoneGuardedRejectsConcurrentGoal(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err := d.CreateTask("goalless guard fixture", "guard goal race", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)

	goalID, err := es.AddGoal(map[string]any{"text": "open goal"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	won, err := d.SetGoallessDoneGuarded(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if won {
		t.Fatal("SetGoallessDoneGuarded closed task with an open goal")
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='met' WHERE id=$1`, goalID); err != nil {
		t.Fatal(err)
	}
	pausedID := mustIntent(t, es, "paused intent")
	_ = es.SetIntentState(pausedID, "paused")
	won, err = d.SetGoallessDoneGuarded(tk.ID)
	if err != nil || won {
		t.Fatalf("SetGoallessDoneGuarded closed task with a paused intent: won=%v err=%v", won, err)
	}
	_ = es.SetIntentState(pausedID, "done")
	won, err = d.SetGoallessDoneGuarded(tk.ID)
	if err != nil || !won {
		t.Fatalf("SetGoallessDoneGuarded failed when all clear: won=%v err=%v", won, err)
	}
}

func TestNodesNewestPreservesOrderAndTotal(t *testing.T) {
	_, s := intentLeaseFixture(t)
	_, baseTotal, err := s.NodesNewest(10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := s.AddFactGraph(map[string]any{"summary": "n"}, "test", nil, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	nodes, total, err := s.NodesNewest(3)
	if err != nil {
		t.Fatal(err)
	}
	if total != baseTotal+5 || len(nodes) != 3 {
		t.Fatalf("total=%d len=%d, want %d/3", total, len(nodes), baseTotal+5)
	}
	if nodes[0].ID != ids[2] || nodes[1].ID != ids[3] || nodes[2].ID != ids[4] {
		t.Fatalf("NodesNewest order mismatch: got [%d %d %d], want [%d %d %d]", nodes[0].ID, nodes[1].ID, nodes[2].ID, ids[2], ids[3], ids[4])
	}
	_ = sql.ErrNoRows
	_ = errors.New
}
