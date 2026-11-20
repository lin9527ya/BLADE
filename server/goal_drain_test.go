package server

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

func goalDrainFixture(t *testing.T) (*Engine, *Task) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	task, err := m.CreateTask(fmt.Sprintf("goal-drain-%d", time.Now().UnixNano()), "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteTask(tidOfTask(task)) })
	if _, err := task.Store.AddNode(db.KindGoal, map[string]any{"text": "fixture goal"}, 0, "met", "test", nil); err != nil {
		t.Fatal(err)
	}
	return NewEngine(m), task
}

func TestGoalDrainKeepsWorkerAliveUntilWriteback(t *testing.T) {
	e, task := goalDrainFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	workCtx := e.execContextFor(ctx, task.ID)
	if !e.beginTaskOperation(task.ID) || !e.markSettling(task.ID) {
		t.Fatal("failed to enter drain")
	}
	done := make(chan struct{})
	go func() { defer close(done); e.finishGoalDrain(ctx, task, time.Second) }()
	select {
	case <-done:
		t.Fatal("completed before worker writeback")
	case <-time.After(25 * time.Millisecond):
	}
	if workCtx.Err() != nil || e.m.TaskStatus(task.ID) == "done" {
		t.Fatal("goal drain killed live work")
	}
	fid, err := task.Store.AddNode(db.KindFact, map[string]any{"summary": "worker's final result"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	e.decInflight(task.ID)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not finish")
	}
	if e.m.TaskStatus(task.ID) != "done" {
		t.Fatal("goals should complete after drain")
	}
	if n, err := task.Store.GetNode(fid); err != nil || n == nil {
		t.Fatal("final output lost")
	}
	if workCtx.Err() != nil {
		t.Fatal("natural completion cancelled worker context")
	}
	if e.claimNextIntent(task) != nil {
		t.Fatal("terminal drain still dispatches")
	}
}

func TestGoalDrainAbortsForNewGoalAndPause(t *testing.T) {
	for _, reason := range []string{"new goal", "pause", "shutdown"} {
		t.Run(reason, func(t *testing.T) {
			e, task := goalDrainFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			e.markSettling(task.ID)
			switch reason {
			case "new goal":
				if _, err := task.Store.AddNode(db.KindGoal, map[string]any{"text": "new question"}, 0, "open", "test", nil); err != nil {
					t.Fatal(err)
				}
			case "pause":
				e.Pause(task.ID, agent.AbortBudgetExhausted)
			case "shutdown":
				cancel()
			}
			e.finishGoalDrain(ctx, task, time.Second)
			if e.m.TaskStatus(task.ID) == "done" {
				t.Fatal("cancelled drain marked done")
			}
			if e.isSettling(task.ID) {
				t.Fatal("cancelled drain left dispatch barrier stuck")
			}
		})
	}
}

func TestGoalCompletionGuardChecksPersistedPauseAndGoals(t *testing.T) {
	e, task := goalDrainFixture(t)
	if _, err := e.m.pg.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, tidOfTask(task)); err != nil {
		t.Fatal(err)
	}
	if won, err := e.m.completeGoalsGuarded(task.ID); err != nil || won {
		t.Fatalf("paused task completed: %v %v", won, err)
	}
	if _, err := e.m.pg.Exec(`UPDATE tasks SET paused=false WHERE id=$1`, tidOfTask(task)); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AddNode(db.KindGoal, map[string]any{"text": "open goal"}, 0, "open", "test", nil); err != nil {
		t.Fatal(err)
	}
	if won, err := e.m.completeGoalsGuarded(task.ID); err != nil || won {
		t.Fatalf("open goal completed: %v %v", won, err)
	}
}

func TestGoalDrainResumesOnlyCapturedLongTaskDirection(t *testing.T) {
	e, task := goalDrainFixture(t)
	captured, err := task.Store.AddIntent(map[string]any{"summary": "already running"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := task.Store.ClaimNode(t.Context(), captured, "work#1", "open", db.IntentLeaseTTL, nil, true); err != nil || n == nil {
		t.Fatalf("initial claim: %v", err)
	}
	fresh, err := task.Store.AddIntent(map[string]any{"summary": "new branch"}, 10, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if !e.markGoalDraining(task) {
		t.Fatal("failed to enter goal drain")
	}
	defer e.goalDrainIntents.Delete(task.ID)
	defer e.settling.Delete(task.ID)
	// Simulate the execution watcher handing output back for reading.
	if _, err := task.Store.CompareAndSetIntentState(captured, "running", "open"); err != nil {
		t.Fatal(err)
	}
	got := e.claimNextIntent(task)
	if got == nil || got.ID != captured {
		t.Fatalf("drain should resume captured direction, not fresh %d: %+v", fresh, got)
	}
	if e.claimNextIntent(task) != nil {
		t.Fatal("drain dispatched a new direction")
	}
	if remaining, err := e.goalDrainRemaining(task); err != nil || remaining != 1 {
		t.Fatalf("unfinished captured work omitted: %d %v", remaining, err)
	}
}

func TestGoalDrainDoesNotForgetQuickPauseResume(t *testing.T) {
	e, task := goalDrainFixture(t)
	control := e.execContextFor(t.Context(), task.ID)
	e.markSettling(task.ID)
	e.Pause(task.ID, agent.AbortBudgetExhausted)
	// A resumed task has a new context, but the original drain stays cancelled.
	e.Resume(task)
	e.execContextFor(t.Context(), task.ID)
	e.finishGoalDrain(t.Context(), task, time.Second, control)
	if e.m.TaskStatus(task.ID) == "done" || e.isSettling(task.ID) {
		t.Fatal("old drain survived pause/resume")
	}
}

func TestGoalDrainTimeoutFencesLateWriteback(t *testing.T) {
	e, task := goalDrainFixture(t)
	id, err := task.Store.AddIntent(map[string]any{"summary": "stuck worker"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	n, err := task.Store.ClaimNode(t.Context(), id, "work#1", "open", db.IntentLeaseTTL, nil, true)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	authority := n.WriteAuthority(tidOfTask(task), task.ExpID)
	control := e.execContextFor(t.Context(), task.ID)
	e.beginTaskOperation(task.ID)
	defer e.decInflight(task.ID)
	if !e.markGoalDraining(task) {
		t.Fatal("failed to start drain")
	}
	e.finishGoalDrain(t.Context(), task, 0, control)
	if e.m.TaskStatus(task.ID) != "done" {
		t.Fatal("bounded drain did not settle")
	}
	settled, err := task.Store.GetNode(id)
	if err != nil || settled.State != "exhausted" {
		t.Fatalf("unfinished intent not fenced: %+v %v", settled, err)
	}
	if _, err := task.Store.WithAuthority(authority).AddNode(db.KindFact, map[string]any{"summary": "late stale output"}, 0, "confirmed", "worker", nil); !errors.Is(err, db.ErrStaleExecution) {
		t.Fatalf("late write must be rejected by authority: %v", err)
	}
}
