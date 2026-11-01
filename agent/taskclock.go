package agent

import (
	"context"
	"time"
)

// TaskClock carries a task's absolute deadline into a worker/planner run so the run
// can clamp its own wall-clock budget to the task's remaining time and pick the
// right wrap-up words (per-run vs task-timeout). Attached to the run ctx by the
// engine. Zero value = no task-level timeout (behaves exactly as before).
type TaskClock struct {
	DeadlineUnix int64 // absolute deadline (unix seconds); 0 = no task timeout
	// EndgameUnix 是"收官期"的起点（unix 秒；0 = 没有收官期）。阈值由引擎按任务总时长
	// 算好再传进来：它属于调度策略，不该散落在提示词渲染代码里，也不该让每次渲染再读库。
	EndgameUnix int64
	Final       bool // coordinator-driven FINAL planner round (task ending now)
}

// Remaining 返回距任务截止还有多久；无截止时 ok=false（不把"没有期限"伪装成 0）。
func (tc TaskClock) Remaining(now time.Time) (time.Duration, bool) {
	if tc.DeadlineUnix <= 0 {
		return 0, false
	}
	return time.Unix(tc.DeadlineUnix, 0).Sub(now), true
}

// Endgame 报告是否已进入收官期（没有收官期时恒 false）。
func (tc TaskClock) Endgame(now time.Time) bool {
	return tc.EndgameUnix > 0 && !now.Before(time.Unix(tc.EndgameUnix, 0))
}

type taskClockKey struct{}

// WithTaskClock attaches a TaskClock to ctx for the run.
func WithTaskClock(ctx context.Context, tc TaskClock) context.Context {
	return context.WithValue(ctx, taskClockKey{}, tc)
}

// taskClockFrom reads the TaskClock (zero value if none attached).
func taskClockFrom(ctx context.Context) TaskClock {
	if v, ok := ctx.Value(taskClockKey{}).(TaskClock); ok {
		return v
	}
	return TaskClock{}
}

// clampMaxDuration folds a task deadline into a run's own wall-clock budget.
//   - ownBudget = the agent's own run_seconds (0 = unlimited).
//   - Returns eff = the MaxDuration to use (floored at 1s so we never pass ≤0, which
//     harness reads as "unlimited"), and clamped = whether the TASK deadline is the
//     binding constraint (remaining ≤ ownBudget, or ownBudget unlimited). When there
//     is no deadline, returns (ownBudget, false) unchanged.
func clampMaxDuration(deadlineUnix int64, ownBudget time.Duration) (eff time.Duration, clamped bool) {
	if deadlineUnix <= 0 {
		return ownBudget, false
	}
	remaining := time.Until(time.Unix(deadlineUnix, 0))
	if remaining < time.Second {
		remaining = time.Second // max(1, …): never pass ≤0 (harness treats 0 as unlimited)
	}
	// clamped when the task deadline binds this run's time: remaining ≤ own budget,
	// or the agent has no own time budget (then remaining always binds).
	clamped = ownBudget <= 0 || remaining <= ownBudget
	eff = remaining
	if ownBudget > 0 && ownBudget < remaining {
		eff = ownBudget
	}
	return eff, clamped
}
