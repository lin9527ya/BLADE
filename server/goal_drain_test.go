package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/llm"
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
	if workCtx.Err() == nil {
		t.Fatal("normal goal completion must retire the task execution context")
	}
	if cause := context.Cause(workCtx); !errors.Is(cause, agent.AbortGoalMet) {
		t.Fatalf("normal completion cause = %v", cause)
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

func TestGoalDrainSoftDeletesLeftoverOpenIntentsOnDone(t *testing.T) {
	e, task := goalDrainFixture(t)
	if err := e.m.SetTaskStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	runningID, err := task.Store.AddIntent(map[string]any{"summary": "正在收尾的意图"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := task.Store.ClaimNode(t.Context(), runningID, "work#1", "open", db.IntentLeaseTTL, nil, true); err != nil || n == nil {
		t.Fatalf("claim running: %v", err)
	}
	stale1, err := task.Store.AddIntent(map[string]any{"summary": "旧队列方向1"}, 10, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	stale2, err := task.Store.AddIntent(map[string]any{"summary": "旧队列方向2", "resume_after_restart": true}, 20, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if !e.markGoalDraining(task) {
		t.Fatal("failed to start drain")
	}
	if ok, err := task.Store.CompareAndSetIntentState(runningID, "running", "done"); err != nil || !ok {
		t.Fatalf("finish running intent: ok=%v err=%v", ok, err)
	}

	e.finishGoalDrain(t.Context(), task, time.Second)

	if got := e.m.TaskStatus(task.ID); got != "done" {
		t.Fatalf("task status=%q, want done", got)
	}
	for _, id := range []int64{stale1, stale2} {
		n, err := task.Store.GetNode(id)
		if err != nil || n == nil || n.State != db.StateIntentDeleted || n.DeleteReason != db.GoalDoneIntentDeleteReason {
			t.Fatalf("stale open intent %d not soft-deleted cleanly: n=%+v err=%v", id, n, err)
		}
	}

	acts, _, err := task.Store.ActivityList(nil, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	foundPruneActivity := false
	for _, a := range acts {
		if strings.Contains(a.Summary, "已自动取消 2 条未执行的排队意图") {
			foundPruneActivity = true
			break
		}
	}
	if !foundPruneActivity {
		t.Fatalf("missing auto-prune activity in timeline: %+v", acts)
	}

	// Done task with only pruned stale intents must reject terminal resume, and
	// reviving for a newly added intent must only claim the new one.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Server{m: e.m, engine: e, ctx: ctx}
	if _, err := s.applyTaskControl(task, "resume"); err == nil || !strings.Contains(err.Error(), "set_goals") {
		t.Fatalf("done task without remaining open intents must refuse resume: %v", err)
	}

	fresh, err := task.Store.AddIntent(map[string]any{"summary": "用户补充的新意图"}, 1, nil, "mainagent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.applyTaskControl(task, "resume"); err != nil {
		t.Fatalf("revive for new intent: %v", err)
	}
	claimed := e.claimNextIntent(task)
	if claimed == nil || claimed.ID != fresh {
		t.Fatalf("revived task should claim fresh intent #%d, got %+v", fresh, claimed)
	}
	if extra := e.claimNextIntent(task); extra != nil {
		t.Fatalf("unexpected extra claim after fresh intent: %+v", extra)
	}
}

func TestGoalDrainTimeoutPrunesUnstartedOpenIntentsOnly(t *testing.T) {
	e, task := goalDrainFixture(t)
	if err := e.m.SetTaskStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	captured, err := task.Store.AddIntent(map[string]any{"summary": "在跑的意图"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := task.Store.ClaimNode(t.Context(), captured, "work#1", "open", db.IntentLeaseTTL, nil, true); err != nil || n == nil {
		t.Fatalf("claim captured: %v", err)
	}
	stale, err := task.Store.AddIntent(map[string]any{"summary": "尚未开始的方向"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	control := e.execContextFor(t.Context(), task.ID)
	if !e.markGoalDraining(task) {
		t.Fatal("failed to start drain")
	}
	e.finishGoalDrain(t.Context(), task, 0, control)
	if e.m.TaskStatus(task.ID) != "done" {
		t.Fatalf("status=%q, want done", e.m.TaskStatus(task.ID))
	}
	if n, _ := task.Store.GetNode(captured); n == nil || n.State != "exhausted" {
		t.Fatalf("captured running intent should become exhausted, got %+v", n)
	}
	if n, _ := task.Store.GetNode(stale); n == nil || n.State != db.StateIntentDeleted {
		t.Fatalf("unstarted open intent should be soft-deleted, got %+v", n)
	}
}

func TestSetTaskStatusGuardedWithPrunedPreservesTimeoutOpenIntents(t *testing.T) {
	e, task := goalDrainFixture(t)
	if err := e.m.SetTaskStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	kept, err := task.Store.AddIntent(map[string]any{"summary": "超时保留的意图"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	won, pruned, err := e.m.SetTaskStatusGuardedWithPruned(task.ID, "timeout")
	if err != nil || !won || len(pruned) != 0 {
		t.Fatalf("timeout terminal should not prune open intents: won=%v pruned=%v err=%v", won, pruned, err)
	}
	if n, _ := task.Store.GetNode(kept); n == nil || n.State != "open" {
		t.Fatalf("timeout should keep open intent intact, got %+v", n)
	}
}

func TestGoalDrainCapturedIntentActuallyExecutesAndWrites(t *testing.T) {
	e, task := goalDrainFixture(t)
	control := e.execContextFor(t.Context(), task.ID)
	id, err := task.Store.AddIntent(map[string]any{"summary": "consume hosted output"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := task.Store.ClaimNode(t.Context(), id, "worker", "open", db.IntentLeaseTTL, nil, false); err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	if !e.markGoalDraining(task) {
		t.Fatal("drain failed")
	}
	defer e.goalDrainIntents.Delete(task.ID)
	defer e.settling.Delete(task.ID)
	if _, err := task.Store.CompareAndSetIntentState(id, "running", "open"); err != nil {
		t.Fatal(err)
	}
	n := e.claimNextIntent(task)
	if n == nil {
		t.Fatal("captured intent not claimed")
	}
	a := n.WriteAuthority(tidOfTask(task), task.ExpID)
	stale := a
	stale.Generation++
	if e.intentExecContextFor(t.Context(), task, stale).Err() == nil {
		t.Fatal("stale authority allowed")
	}
	called := false
	var factID int64
	worker := agent.NewWorker(retestProvider{complete: func(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		called = true
		if err := ctx.Err(); err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		var err error
		factID, err = task.Store.WithAuthority(a).AddNode(db.KindFact, map[string]any{"summary": "hosted output collected"}, 0, "confirmed", n.Owner, nil)
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("collected")}}, "end_turn", llm.Usage{}, err
	}}, "fixture", e.m.dir, nil, 10000, 1)
	worker.SetNonStreaming(func() bool { return true })
	e.runIntent(t.Context(), task, n.Owner, worker, n, "", "")
	if !called || factID == 0 {
		t.Fatal("claim succeeded but worker never consumed output")
	}
	if node, err := task.Store.GetNode(factID); err != nil || node == nil {
		t.Fatal("writeback missing")
	}
	if e.execContextFor(t.Context(), task.ID).Err() == nil {
		t.Fatal("ordinary settling context allowed")
	}
	e.Pause(task.ID, agent.AbortBudgetExhausted)
	e.Resume(task)
	if control.Err() == nil || e.intentExecContextFor(t.Context(), task, a).Err() == nil {
		t.Fatal("paused drain control recreated")
	}
}
