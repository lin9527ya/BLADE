package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// Enter the dispatch barrier before returning from the planner round. The task
// runtime owns the drain goroutine so shutdown/deletion also waits or cancels it.
func (e *Engine) startGoalDrain(ctx context.Context, t *Task, reason string) {
	e.deleteMu.RLock()
	if e.IsDeleting(t.ID) {
		e.deleteMu.RUnlock()
		return
	}
	// Capture the live context before the settling guard turns new contexts into
	// cancelled ones; this context represents the work already in flight.
	control := e.execContextFor(ctx, t.ID)
	if !e.markGoalDraining(t) {
		e.deleteMu.RUnlock()
		return
	}
	rt := e.registerTaskRoutines(ctx, t.ID, 1)
	e.deleteMu.RUnlock()
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
		Summary: "目标已达成：停止领新意图，等待当前工作收尾",
		Detail:  fmt.Sprintf("%s；最多等待 %s。暂停、删除及授权停止仍立即生效；新增目标会撤销本次达标收官。", reason, settleDrainGrace)})
	runTaskRoutine(rt, func(ctx context.Context) { e.finishGoalDrain(ctx, t, settleDrainGrace, control) })
}

// No terminal planner is needed: the goals were just proved. Workers keep their
// write authority while running, and the final report reads their persisted output.
func (e *Engine) finishGoalDrain(ctx context.Context, t *Task, grace time.Duration, controls ...context.Context) {
	// Keep the original execution context: even a quick pause/resume cannot make
	// this older drain forget that the operator cancelled it. Our own bounded
	// drain cancellation is the one exception.
	interrupted := func() bool {
		return len(controls) > 0 && controls[0].Err() != nil && !errors.Is(context.Cause(controls[0]), agent.AbortSettleDrainTimeout)
	}
	defer func() {
		e.goalDrainIntents.Delete(t.ID)
		if !isTerminalStatus(e.m.TaskStatus(t.ID)) {
			e.settling.Delete(t.ID)
			if !e.IsDeleting(t.ID) {
				t.Notify()
			}
		}
	}()
	stopAt := time.Now().Add(grace)
	timedOut := false
	for {
		if interrupted() || ctx.Err() != nil || e.IsPaused(t.ID) || e.IsDeleting(t.ID) || t.lifecycleSnapshot().Paused {
			return
		}
		open, err := t.Store.HasOpenGoal()
		if err != nil {
			log.Printf("[goal-drain] task %s 读取目标失败，撤销收官: %v", t.ID, err)
			return
		}
		if open {
			e.emitActivity(t, db.Activity{Worker: "system", Kind: "system", Summary: "出现未达成目标，撤销达标收官并恢复探索"})
			return
		}
		pending := 0
		executionUnknown := false
		if execs := e.m.Executions(); execs != nil {
			active, err := execs.ActiveForTask(t.ID)
			executionUnknown = err != nil
			if executionUnknown {
				pending = -1
			} else {
				pending = len(active)
			}
		}
		remaining, err := e.goalDrainRemaining(t)
		if err != nil {
			log.Printf("[goal-drain] task %s 读取续跑状态失败: %v", t.ID, err)
			return
		}
		if e.inflightCount(t.ID) == 0 && pending == 0 && remaining == 0 {
			break
		}
		if time.Now().After(stopAt) {
			timedOut = true
			e.cancelExec(t.ID, agent.AbortSettleDrainTimeout)
			e.m.CancelTaskExecutions(t.ID, "达标收官等待超时")
			if executionUnknown {
				log.Printf("[goal-drain] task %s 执行状态仍未知，撤销成功收官（已请求停止本地执行）", t.ID)
				return
			}
			e.emitActivity(t, db.Activity{Worker: "system", Kind: "system", Summary: "达标收官等待超时：已请求停止剩余工作，保留已写回成果"})
			if sleepCtx(ctx, 3*time.Second) {
				return
			}
			break
		}
		if sleepCtx(ctx, 100*time.Millisecond) {
			return
		}
	}
	if interrupted() || ctx.Err() != nil || e.IsPaused(t.ID) || e.IsDeleting(t.ID) {
		return
	}
	if timedOut {
		for _, id := range e.goalDrainIDs(t.ID) {
			for _, from := range []string{"running", "open"} {
				if _, err := t.Store.CompareAndSetIntentState(id, from, "exhausted"); err != nil {
					log.Printf("[goal-drain] task %s 无法栅栏收尾意图 %d: %v", t.ID, id, err)
					return
				}
			}
		}
	}
	won, pruned, err := e.m.completeGoalsGuardedWithPruned(t.ID)
	if err != nil {
		log.Printf("[goal-drain] task %s 完成落库失败: %v", t.ID, err)
		return
	}
	if won {
		// Normal completion also retires the task execution context. This is the
		// terminal cause; any late goroutine must not keep a live context.
		e.cancelExec(t.ID, agent.AbortGoalMet)
		e.completionWarn.Delete(t.ID)
		e.blockedSignature.Delete(t.ID)
		e.budgetStage.Delete(t.ID)
		e.notifyTaskSettled(t.ID)
		e.emitPrunedIntentsOnGoalDone(t, pruned)
		e.promoteTaskKnowledge(t)
		summary := "目标已达成，当前工作已完成收尾，任务结束"
		if timedOut {
			summary = "目标已达成，收尾等待超时后结束：部分工作被中断，已写回成果保留"
		}
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "system", Summary: summary})
	}
}

func (e *Engine) emitPrunedIntentsOnGoalDone(t *Task, pruned []int64) {
	if e == nil || t == nil || len(pruned) == 0 {
		return
	}
	log.Printf("[goal-drain] task %s 目标达成，已自动取消 %d 条未执行意图 %v", t.ID, len(pruned), pruned)
	const maxDetailIDs = 20
	shown := pruned
	suffix := ""
	if len(shown) > maxDetailIDs {
		shown = shown[:maxDetailIDs]
		suffix = fmt.Sprintf(" 等共 %d 条", len(pruned))
	}
	parts := make([]string, len(shown))
	for i, id := range shown {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	e.emitActivity(t, db.Activity{
		Worker:  "system",
		Kind:    "system",
		Summary: fmt.Sprintf("目标已达成，已自动取消 %d 条未执行的排队意图", len(pruned)),
		Detail:  fmt.Sprintf("已将剩余待领意图置为删除态（%s%s），后续因补充目标或单派意图恢复任务时不会重复探索旧队列；痕迹清理意图与历史血缘保留。", strings.Join(parts, "、"), suffix),
	})
}

func (e *Engine) markGoalDraining(t *Task) bool {
	e.dispatchMu.Lock()
	defer e.dispatchMu.Unlock()
	if e.isSettling(t.ID) {
		return false
	}
	running, err := t.Store.ListByKindState(db.KindIntent, "running", 1000)
	if err != nil {
		log.Printf("[goal-drain] task %s 无法读取在跑方向: %v", t.ID, err)
		return false
	}
	ids := make([]int64, 0, len(running))
	for _, n := range running {
		ids = append(ids, n.ID)
	}
	e.goalDrainIntents.Store(t.ID, ids)
	e.settling.Store(t.ID, true)
	return true
}

func (e *Engine) isGoalDraining(taskID string) bool {
	_, ok := e.goalDrainIntents.Load(taskID)
	return ok
}
func (e *Engine) goalDrainIDs(taskID string) []int64 {
	v, _ := e.goalDrainIntents.Load(taskID)
	ids, _ := v.([]int64)
	return ids
}
func (e *Engine) goalDrainRemaining(t *Task) (int, error) {
	pending := 0
	for _, id := range e.goalDrainIDs(t.ID) {
		n, err := t.Store.GetNode(id)
		if err != nil {
			return 0, err
		}
		if n != nil && (n.State == "running" || n.State == "open") {
			pending++
		}
	}
	return pending, nil
}
