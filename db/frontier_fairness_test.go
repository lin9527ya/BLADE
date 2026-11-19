package db

import (
	"fmt"
	"testing"
	"time"
)

func TestOverdueIntentWinsAgainstNewHighPriorityStream(t *testing.T) {
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
	old, err := ts.AddIntent(map[string]any{"summary": "old independent branch"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET created_at=now()-interval '16 minutes' WHERE id=$1`, old); err != nil {
		t.Fatal(err)
	}
	// Fill beyond the candidate window: oldest-tail selection must also participate.
	for i := 0; i < 125; i++ {
		if _, err := ts.AddIntent(map[string]any{"summary": fmt.Sprintf("new %d", i)}, 10, nil, "planner"); err != nil {
			t.Fatal(err)
		}
	}
	fr, err := ts.FrontierForSlot("work#1", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr) != 1 || fr[0].ID != old {
		t.Fatalf("overdue direction starved: %v", nodeIDs(fr))
	}
}

func TestWorkerSessionDefaultsAndResumeCompatibility(t *testing.T) {
	t.Setenv("BLADE_WORKER_PER_INTENT_SESSION", "")
	for _, mode := range []string{"balanced", "divergent", ""} {
		if !PerIntentWorkerSession(mode) {
			t.Fatalf("mode %s should isolate intents", mode)
		}
	}
	n := &Node{ID: 42}
	if got := logicalSessionForClaim(n, 7, "work#1", "balanced"); got != "exp7-worker-i42" {
		t.Fatalf("session=%s", got)
	}
	n.LogicalSessionID = "exp7-worker-slot3"
	if got := logicalSessionForClaim(n, 7, "work#1", "balanced"); got != n.LogicalSessionID {
		t.Fatal("lost existing restart transcript")
	}
	t.Setenv("BLADE_WORKER_PER_INTENT_SESSION", "false")
	if PerIntentWorkerSession("divergent") {
		t.Fatal("explicit rollback ignored")
	}
}
