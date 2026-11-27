package agent

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/db"
)

type blockingPlannerProvider struct {
	started  chan struct{}
	complete func(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error)
}

func (b *blockingPlannerProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {}
}

func (b *blockingPlannerProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if b.started != nil {
		select {
		case <-b.started:
		default:
			close(b.started)
		}
	}
	return b.complete(ctx, req)
}

func TestPlannerResetHelpersAndMainAgentToolUnit(t *testing.T) {
	if got := PlannerSessionID(12); got != "exp12-planner" {
		t.Fatalf("PlannerSessionID(12) = %q, want exp12-planner", got)
	}
	if got := PlannerSessionID(12, 0); got != "exp12-planner" {
		t.Fatalf("PlannerSessionID(12, 0) = %q, want exp12-planner", got)
	}
	if got := PlannerSessionID(12, 3); got != "exp12-planner-s3" {
		t.Fatalf("PlannerSessionID(12, 3) = %q, want exp12-planner-s3", got)
	}
	if TriggerPriority("context_reset") != 3 {
		t.Fatalf("TriggerPriority(context_reset) = %d, want 3", TriggerPriority("context_reset"))
	}

	ts := NewToolSet(nil, "human")
	if out := callTool(t, ts.MainAgentTools(), "reset_planner_context", `{}`); !strings.Contains(out, "需要任务上下文") {
		t.Fatalf("nil-store reset_planner_context should require task context: %s", out)
	}
	ts.ts = &db.ExplorationStore{}
	if out := callTool(t, ts.MainAgentTools(), "reset_planner_context", `{}`); !strings.Contains(out, "当前不可用") {
		t.Fatalf("unwired reset_planner_context should error cleanly: %s", out)
	}

	var gotClearTodos bool
	var gotReason string
	ts.resetPlannerContext = func(_ context.Context, clearTodos bool, reason string) (PlannerContextResetResult, error) {
		gotClearTodos = clearTodos
		gotReason = reason
		return PlannerContextResetResult{
			ContextSeg:   2,
			ClearedTodos: clearTodos,
			WasRunning:   true,
			Woken:        true,
		}, nil
	}
	out := callTool(t, ts.MainAgentTools(), "reset_planner_context", `{"clear_todos":true,"reason":"聚焦认证链"}`)
	if !gotClearTodos || gotReason != "聚焦认证链" {
		t.Fatalf("unexpected callback args: clearTodos=%v reason=%q", gotClearTodos, gotReason)
	}
	if !strings.Contains(out, "#2") || !strings.Contains(out, "同时清空") || !strings.Contains(out, "立即唤醒") {
		t.Fatalf("unexpected reset_planner_context output: %s", out)
	}

	// Nil store ResetContext still clears hot session / lastRoundNote and bumps segment.
	p := NewPlanner(nil, "", t.TempDir(), nil, 0, 1)
	p.lastRoundNote = "stale note"
	st, err := p.ResetContext(nil, 0, false)
	if err != nil || st.ContextSeg != 1 || p.lastRoundNote != "" {
		t.Fatalf("in-memory ResetContext got st=%+v note=%q err=%v", st, p.lastRoundNote, err)
	}
}

func TestPlannerResetContextClearsHotNoteTranscriptAndOptionalTodos(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("planner context reset", "验证后台鉴权", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)

	txDir := filepath.Join(t.TempDir(), "transcripts")
	tx := transcript.NewStore(txDir)
	prov := &recordingProvider{text: "旧误判结论XYZ：先别查登录接口"}
	p := NewPlanner(prov, "test-model", t.TempDir(), tx, 0, 4)
	p.SetNonStreaming(func() bool { return true })

	// Seed TodoWrite item before first round.
	todoStore := p.restorePlannerState(context.Background(), ts, task.ID, task.ExplorationID)
	payload, _ := json.Marshal(map[string]any{"todos": []map[string]string{
		{"content": "保留这条串行待办", "status": "in_progress"},
	}})
	if _, err := todoStore.Tool().Call(context.Background(), payload, nil); err != nil {
		t.Fatal(err)
	}
	p.savePlannerState(ts, task.ID, task.ExplorationID, todoStore)

	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "验证后台鉴权", nil, nil); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if p.hot == nil || p.lastRoundNote == "" {
		t.Fatalf("expected hot session and lastRoundNote after round 1, got hot=%v note=%q", p.hot != nil, p.lastRoundNote)
	}

	// Reset context WITHOUT clearing todos.
	st, err := p.ResetContext(ts, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.ContextSeg != 1 || st.LastRoundNote != "" || len(st.Todos) != 1 {
		t.Fatalf("unexpected state after ResetContext(clearTodos=false): %+v", st)
	}
	if p.hot != nil || p.lastRoundNote != "" {
		t.Fatalf("expected p.hot=nil and empty lastRoundNote after reset, got hot=%v note=%q", p.hot != nil, p.lastRoundNote)
	}

	// Switch response & force resume mode too to confirm seg isolation applies cleanly to transcript.
	t.Setenv("BLADE_PLANNER_RESUME_SESSION", "1")
	prov.text = "干净上下文第1轮结论"
	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "验证后台鉴权", []TriggerEvent{{Kind: "context_reset", Detail: "重新评估鉴权"}}, nil); err != nil {
		t.Fatalf("round 2 after reset: %v", err)
	}
	lastReq := prov.last()
	var sawOldConclusion, sawResetTrigger, sawTodo, sawDelta bool
	for _, m := range lastReq.Messages {
		txt := m.Text()
		if strings.Contains(txt, "旧误判结论XYZ") {
			sawOldConclusion = true
		}
		if strings.Contains(txt, "已清空 Planner 先前上下文记忆") && strings.Contains(txt, "重新评估鉴权") {
			sawResetTrigger = true
		}
		if strings.Contains(txt, "保留这条串行待办") {
			sawTodo = true
		}
		if strings.Contains(txt, "【本轮增量态势") {
			sawDelta = true
		}
	}
	if sawOldConclusion {
		t.Fatal("reset 后仍携带了上一阶段的旧结论或旧对话")
	}
	if !sawResetTrigger || !sawTodo || sawDelta {
		t.Fatalf("post-reset prompt checks failed: resetTrigger=%v todo=%v delta=%v", sawResetTrigger, sawTodo, sawDelta)
	}
	if _, err := os.Stat(tx.MainPath(PlannerSessionID(task.ExplorationID, 1))); err != nil {
		t.Fatalf("expected segmented transcript exp%d-planner-s1 to exist: %v", task.ExplorationID, err)
	}

	// Reset again WITH clearTodos=true and verify a fresh Planner instance sees clean state.
	st2, err := p.ResetContext(ts, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if st2.ContextSeg != 2 || len(st2.Todos) != 0 || st2.LastRoundNote != "" {
		t.Fatalf("unexpected state after ResetContext(clearTodos=true): %+v", st2)
	}
	pFresh := NewPlanner(prov, "test-model", t.TempDir(), tx, 0, 4)
	restored := pFresh.restorePlannerState(context.Background(), ts, task.ID, task.ExplorationID)
	if len(restored.List()) != 0 || pFresh.lastRoundNote != "" {
		t.Fatalf("fresh planner should see cleared todos/note, got todos=%+v note=%q", restored.List(), pFresh.lastRoundNote)
	}
}

func TestPlannerResetDuringInflightPlanDoesNotLeakOldNote(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("planner reset race", "goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)

	started := make(chan struct{})
	prov := &blockingPlannerProvider{
		started: started,
		complete: func(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
			<-ctx.Done()
			return llm.Message{}, "", llm.Usage{}, ctx.Err()
		},
	}
	p := NewPlanner(prov, "test-model", t.TempDir(), nil, 0, 4)
	p.SetNonStreaming(func() bool { return true })
	p.lastRoundNote = "准备被清空的旧结论"
	_ = ts.SavePlannerState(task.ID, []db.PlannerTodoItem{{Content: "keep", Status: "pending"}}, "准备被清空的旧结论")

	planCtx, cancel := context.WithCancelCause(context.Background())
	planDone := make(chan error, 1)
	go func() {
		_, _, perr := p.Plan(planCtx, task.ID, d.Assets(), ts, "goal", nil, nil)
		planDone <- perr
	}()

	<-started
	cancel(AbortPlannerContextReset)
	st, err := p.ResetContext(ts, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	<-planDone

	loaded, found, err := ts.LoadPlannerState(task.ID)
	if err != nil || !found {
		t.Fatalf("LoadPlannerState err=%v found=%v", err, found)
	}
	if st.ContextSeg != 1 || loaded.ContextSeg != 1 {
		t.Fatalf("context_seg mismatch: reset=%d loaded=%d", st.ContextSeg, loaded.ContextSeg)
	}
	if loaded.LastRoundNote != "" || p.lastRoundNote != "" || p.hot != nil {
		t.Fatalf("aborted round leaked state back: dbNote=%q memNote=%q hot=%v", loaded.LastRoundNote, p.lastRoundNote, p.hot != nil)
	}
}
