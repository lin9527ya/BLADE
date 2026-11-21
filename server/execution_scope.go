package server

import (
	"context"
	"fmt"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// Agent-facing adapter; the engine/admin keep the unscoped service. Do not embed
// it: embedding would accidentally expose future methods without authorization.
type taskExecutionService struct {
	svc    *ExecutionService
	taskID string
}

func (s *ExecutionService) ForTask(taskID string) agent.ExecService {
	if s == nil {
		return nil
	}
	return &taskExecutionService{svc: s, taskID: taskID}
}
func (s *taskExecutionService) checkTask(taskID string) error {
	if s.taskID == "" || taskID != s.taskID {
		return fmt.Errorf("执行不属于当前任务")
	}
	return nil
}
func (s *taskExecutionService) checkExecution(ctx context.Context, id int64) error {
	s.svc.mu.Lock()
	run := s.svc.live[id]
	s.svc.mu.Unlock()
	if run != nil {
		return s.checkTask(run.taskID)
	}
	var taskID string
	if err := s.svc.pg.QueryRowContext(ctx, `SELECT task_id FROM tool_executions WHERE id=$1`, id).Scan(&taskID); err != nil {
		return err
	}
	return s.checkTask(taskID)
}
func (s *taskExecutionService) checkIntent(id int64) error {
	var belongs bool
	if err := s.svc.pg.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE n.id=$1 AND t.id::text=$2)`, id, s.taskID).Scan(&belongs); err != nil {
		return err
	}
	if !belongs {
		return fmt.Errorf("意图不属于当前任务")
	}
	return nil
}
func (s *taskExecutionService) Submit(ctx context.Context, taskID string, intentID int64, worker, command, desc string, timeout time.Duration) (agent.ExecStatus, error) {
	return s.SubmitTool(ctx, taskID, intentID, worker, "run_execution", command, desc, timeout)
}
func (s *taskExecutionService) SubmitTool(ctx context.Context, taskID string, intentID int64, worker, tool, command, desc string, timeout time.Duration) (agent.ExecStatus, error) {
	if err := s.checkTask(taskID); err != nil {
		return agent.ExecStatus{}, err
	}
	return s.svc.SubmitTool(ctx, taskID, intentID, worker, tool, command, desc, timeout)
}
func (s *taskExecutionService) SubmitToolMeta(ctx context.Context, taskID string, intentID int64, worker, tool, command, desc string, timeout time.Duration, meta map[string]any) (agent.ExecStatus, error) {
	if err := s.checkTask(taskID); err != nil {
		return agent.ExecStatus{}, err
	}
	return s.svc.SubmitToolMeta(ctx, taskID, intentID, worker, tool, command, desc, timeout, meta)
}
func (s *taskExecutionService) StageScript(ctx context.Context, taskID string, intentID int64, name string, content []byte) (string, error) {
	if err := s.checkTask(taskID); err != nil {
		return "", err
	}
	return s.svc.StageScript(ctx, taskID, intentID, name, content)
}
func (s *taskExecutionService) Get(ctx context.Context, id int64) (agent.ExecStatus, error) {
	if err := s.checkExecution(ctx, id); err != nil {
		return agent.ExecStatus{}, err
	}
	return s.svc.Get(ctx, id)
}
func (s *taskExecutionService) GetExecutionRow(ctx context.Context, id int64) (*db.ToolExecution, error) {
	if err := s.checkExecution(ctx, id); err != nil {
		return nil, err
	}
	return s.svc.GetExecutionRow(ctx, id)
}
func (s *taskExecutionService) Wait(ctx context.Context, id int64, timeout time.Duration) (agent.ExecStatus, error) {
	if err := s.checkExecution(ctx, id); err != nil {
		return agent.ExecStatus{}, err
	}
	return s.svc.Wait(ctx, id, timeout)
}
func (s *taskExecutionService) Cancel(ctx context.Context, id int64, reason string) (bool, error) {
	if err := s.checkExecution(ctx, id); err != nil {
		return false, err
	}
	return s.svc.Cancel(ctx, id, reason)
}
func (s *taskExecutionService) CountRanForIntent(id int64) (int64, error) {
	if err := s.checkIntent(id); err != nil {
		return 0, err
	}
	return s.svc.CountRanForIntent(id)
}
func (s *taskExecutionService) ListForIntent(id int64) ([]*db.ToolExecution, error) {
	if err := s.checkIntent(id); err != nil {
		return nil, err
	}
	return s.svc.ListForIntent(id)
}
func (s *taskExecutionService) ActiveForTask(taskID string) ([]*db.ToolExecution, error) {
	if err := s.checkTask(taskID); err != nil {
		return nil, err
	}
	return s.svc.ActiveForTask(taskID)
}

var _ agent.ExecService = (*taskExecutionService)(nil)
var _ agent.ExecutionRowService = (*taskExecutionService)(nil)
var _ agent.MetaExecService = (*taskExecutionService)(nil)
var _ agent.CVEScriptService = (*taskExecutionService)(nil)
