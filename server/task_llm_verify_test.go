package server

import (
	"context"
	"iter"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/llmrec"
)

type verifyAccountingProvider struct {
	engine      *Engine
	taskID      string
	activeAtRun int64
	taskCtx     string
	expCtx      int64
}

func (p *verifyAccountingProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(func(llm.StreamEvent, error) bool) {}
}

func (p *verifyAccountingProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.activeAtRun = p.engine.ActiveLLMCalls(p.taskID)
	p.taskCtx = llmrec.TaskIDFrom(ctx)
	p.expCtx = llmrec.ExplorationIDFrom(ctx)
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("{\"verdict\":\"supported\"}")}}, "", llm.Usage{}, nil
}

// deep 复核必须计入 ActiveLLMCalls，并带 task/exploration 归因；否则暂停/预算/用量
// 视图看不到这笔调用。
func TestTaskLLMVerifierAccountsCall(t *testing.T) {
	eng := &Engine{}
	task := &Task{ID: "42", ExpID: 99}
	prov := &verifyAccountingProvider{engine: eng, taskID: task.ID}
	pl := agent.NewPlanner(prov, "test-model", t.TempDir(), nil, 0, 1)
	s := &Server{engine: eng}

	out, err := s.taskLLMVerifier(task, pl)(context.Background(), "digest")
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("empty verifier output")
	}
	if prov.activeAtRun != 1 {
		t.Fatalf("ActiveLLMCalls during verifier = %d, want 1", prov.activeAtRun)
	}
	if prov.taskCtx != task.ID || prov.expCtx != task.ExpID {
		t.Fatalf("attribution task=%q exp=%d, want %q/%d", prov.taskCtx, prov.expCtx, task.ID, task.ExpID)
	}
	if got := eng.ActiveLLMCalls(task.ID); got != 0 {
		t.Fatalf("ActiveLLMCalls after verifier = %d, want 0", got)
	}
}
