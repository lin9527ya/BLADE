package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestExecutionQueryFailureDoesNotFinalizeOrReclaimIntent(t *testing.T) {
	e, task := goalDrainFixture(t)
	id, err := task.Store.AddIntent(map[string]any{"summary": "ledger unknown"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	n, err := task.Store.ClaimNode(t.Context(), id, "worker", "open", db.IntentLeaseTTL, nil, false)
	if err != nil || n == nil {
		t.Fatal(err)
	}
	queried := make(chan struct{}, 4)
	svc := e.m.Executions()
	svc.activeIntentQuery = func(int64) ([]*db.ToolExecution, error) {
		select {
		case queried <- struct{}{}:
		default:
		}
		return nil, errors.New("injected active SELECT failure")
	}
	if !e.execGate(task, id, "worker", n.WriteAuthority(tidOfTask(task), task.ExpID)) {
		t.Fatal("query failure bypassed gate")
	}
	// Wait for both the gate query and the watcher's first query, not a random sleep.
	for i := 0; i < 2; i++ {
		select {
		case <-queried:
		case <-time.After(3 * time.Second):
			t.Fatal("watcher did not query")
		}
	}
	if _, err := e.m.pg.Exec(`UPDATE exploration_nodes SET heartbeat_at=now()-interval '30 minutes',lease_expires_at=now()-interval '15 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	e.sweepExpiredLeases(task)
	node, err := task.Store.GetNode(id)
	if err != nil || node.State != "running" {
		t.Fatalf("unknown execution reopened intent: %+v %v", node, err)
	}
	if err := e.controlWatchedIntent(t.Context(), id, "pause"); err != nil {
		t.Fatal(err)
	}
	node, err = task.Store.GetNode(id)
	if err != nil || node.State != "paused" {
		t.Fatal("unknown ledger blocked manual pause")
	}
}

func TestGoalDrainCannotCompleteOnUnknownExecutions(t *testing.T) {
	e, task := goalDrainFixture(t)
	e.execContextFor(t.Context(), task.ID)
	e.m.Executions().activeTaskQuery = func(string) ([]*db.ToolExecution, error) { return nil, errors.New("injected active SELECT failure") }
	if !e.markGoalDraining(task) {
		t.Fatal("drain failed")
	}
	e.finishGoalDrain(t.Context(), task, 0)
	if e.m.TaskStatus(task.ID) == "done" {
		t.Fatal("unknown ledger marked goal drain complete")
	}
}

func TestCancelStillStopsKnownLocalProcessWhenDatabaseUnavailable(t *testing.T) {
	svc, pg, _, task := newExecFixture(t)
	st, err := svc.Submit(context.Background(), task, 0, "worker", "sleep 30", "local stop fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	run := svc.live[st.ID]
	svc.mu.Unlock()
	if run == nil {
		t.Fatal("missing local handle")
	}
	pg.Close()
	ok, err := svc.ForTask(task).Cancel(t.Context(), st.ID, "database unavailable")
	if !ok || err == nil {
		t.Fatalf("must stop locally and report ledger error: %v %v", ok, err)
	}
	select {
	case <-run.done:
	case <-time.After(execCancelGrace + 3*time.Second):
		t.Fatal("database failure prevented local stop")
	}
	dsn, _, err := db.DSN()
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Close()
	if _, err := recovery.Exec(`DELETE FROM tool_executions WHERE task_id=$1`, task); err != nil {
		t.Fatal(err)
	}
}
