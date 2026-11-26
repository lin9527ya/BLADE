package db

import (
	"fmt"
	"testing"
	"time"
)

func TestHighPriorityChainBeatsStaleLowPriorityStream(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask(fmt.Sprintf("fair-%d", time.Now().UnixNano()), "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	oldLow, err := ts.AddIntent(map[string]any{"summary": "old independent branch"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	oldSameOld, err := ts.AddIntent(map[string]any{"summary": "old same tier branch"}, 10, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET created_at=now()-interval '16 minutes' WHERE id IN ($1,$2)`, oldLow, oldSameOld); err != nil {
		t.Fatal(err)
	}
	var newestHigh int64
	for i := 0; i < 125; i++ {
		id, err := ts.AddIntent(map[string]any{"summary": fmt.Sprintf("new %d", i)}, 10, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		newestHigh = id
	}
	_ = newestHigh
	fr, err := ts.FrontierForSlot("work#1", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Stale P1 must NEVER hijack P10; within the same P10 tier, the overdue peer wins fairly.
	if len(fr) != 1 || fr[0].ID == oldLow || fr[0].ID != oldSameOld {
		t.Fatalf("expected same-tier P10 #%d to lead (not P1 #%d): got %v", oldSameOld, oldLow, nodeIDs(fr))
	}
}

func TestBestFirstFocusYieldMomentumAndParkUnpark(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask(fmt.Sprintf("bestfirst-%d", time.Now().UnixNano()), "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	broad, err := ts.AddIntent(map[string]any{"summary": "broad recon", "contract": map[string]any{"route": "recon"}}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET created_at=now()-interval '25 minutes' WHERE id=$1`, broad); err != nil {
		t.Fatal(err)
	}
	fh, err := ts.AddNode(KindFoothold, map[string]any{"summary": "shell on web01"}, 0, StateLootActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	deep, err := ts.AddIntentGraph(map[string]any{"summary": "escalate from web01 shell", "contract": map[string]any{"route": "rce-chain"}}, 5, nil, "planner", []IntentEdge{{From: fh, Rel: RelDerivedFrom}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.SetExplorationFocus(ExplorationFocus{Mode: ExploreModeDeepen, FocusRoutes: []string{"rce-chain"}, Reason: "foothold hit"}); err != nil {
		t.Fatal(err)
	}
	fr, err := ts.FrontierForSlot("work#1", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr) < 2 || fr[0].ID != deep || fr[0].Schedule.Components.Focus <= 0 || fr[0].Schedule.Components.YieldMomentum <= 0 {
		t.Fatalf("focused yield chain should win head of queue: got %v (schedule=%+v)", nodeIDs(fr), fr[0].Schedule)
	}
	res, err := ts.BatchUpdateIntents("focus rce-chain", []ReprioritizeIntentUpdate{{IntentID: broad, Action: "park"}})
	if err != nil || len(res) != 1 || !res[0].Changed || res[0].NewState != "paused" {
		t.Fatalf("unexpected batch park result: %+v (%v)", res, err)
	}
	cnt, err := ts.CountDeferredParkedIntents()
	if err != nil || cnt != 1 {
		t.Fatalf("CountDeferredParkedIntents=%d err=%v, want 1", cnt, err)
	}
	unparked, err := ts.AutoUnparkDeferred(5)
	if err != nil || len(unparked) != 1 || unparked[0] != broad {
		t.Fatalf("AutoUnparkDeferred=%v err=%v, want [%d]", unparked, err, broad)
	}
}

func TestWorkerSessionDefaultsAndResumeCompatibility(t *testing.T) {
	t.Setenv("BLADE_WORKER_PER_INTENT_SESSION", "")
	if !PerIntentWorkerSession("") {
		t.Fatal("default should isolate intents")
	}
	n := &Node{ID: 42}
	if got := logicalSessionForClaim(n, 7, "worker#42"); got != "exp7-worker-i42" {
		t.Fatalf("session=%s", got)
	}
	n.LogicalSessionID = "exp7-worker-slot3"
	if got := logicalSessionForClaim(n, 7, "work#1"); got != n.LogicalSessionID {
		t.Fatal("lost existing restart transcript")
	}
	t.Setenv("BLADE_WORKER_PER_INTENT_SESSION", "false")
	if PerIntentWorkerSession("") {
		t.Fatal("explicit rollback ignored")
	}
}
