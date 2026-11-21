package server

import (
	"context"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// Ordinary new runs remain fenced by settling. Only a previously captured
// direction may re-enter to consume long-task output, with current authority
// and the original (never recreated during drain) execution control context.
func (e *Engine) intentExecContextFor(parent context.Context, task *Task, a db.WriteAuthority) context.Context {
	reject := func() context.Context {
		c, cancel := context.WithCancelCause(parent)
		cancel(agent.AbortTaskNotRunning)
		return c
	}
	if !e.isSettling(task.ID) {
		return e.execContextFor(parent, task.ID)
	}
	if parent.Err() != nil || e.m == nil || a.TaskID != tidOfTask(task) || a.ExplorationID != task.ExpID {
		return reject()
	}
	captured := false
	for _, id := range e.goalDrainIDs(task.ID) {
		if id == a.IntentID {
			captured = true
			break
		}
	}
	if !captured {
		return reject()
	}
	var valid int
	if err := e.m.pg.WithAuthority(a).QueryRowContext(parent, `SELECT 1`).Scan(&valid); err != nil {
		return reject()
	}
	e.execMu.Lock()
	defer e.execMu.Unlock()
	if !e.isGoalDraining(task.ID) || e.IsPaused(task.ID) || e.IsDeleting(task.ID) || isTerminalStatus(e.m.TaskStatus(task.ID)) {
		return reject()
	}
	control := e.execCtx[task.ID]
	if control == nil || control.Err() != nil {
		return reject()
	}
	return control
}
