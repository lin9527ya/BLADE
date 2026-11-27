package server

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

type plannerChatTestProvider struct {
	mu       sync.Mutex
	calls    int
	requests []llm.CompletionRequest
	complete func(ctx context.Context, callIdx int, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error)
}

func (p *plannerChatTestProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {}
}

func (p *plannerChatTestProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.requests = append(p.requests, req)
	fn := p.complete
	p.mu.Unlock()
	if fn != nil {
		return fn(ctx, idx, req)
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("已收到，正在处理。")}}, "end_turn", llm.Usage{}, nil
}

func (p *plannerChatTestProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func reqHasSubstring(req llm.CompletionRequest, sub string) bool {
	for _, m := range req.Messages {
		if strings.Contains(m.Text(), sub) {
			return true
		}
		for _, b := range m.Content {
			if strings.Contains(b.Text, sub) {
				return true
			}
			for _, nested := range b.Content {
				if strings.Contains(nested.Text, sub) {
					return true
				}
			}
		}
	}
	return false
}

func TestPlannerChatUnitQueueStopAndSessionFilter(t *testing.T) {
	e := NewEngine(nil)
	runCtx, seq, cancel := e.beginPlannerRun("42", context.Background())
	if !e.IsPlannerRunning("42") {
		t.Fatal("expected planner running after beginPlannerRun")
	}

	e.planExecMu.Lock()
	e.ensurePlannerMapsLocked()
	e.plannerSteer["42"] = append(e.plannerSteer["42"], "先查 /admin", "再看 /api")
	e.planExecMu.Unlock()
	if got := e.PlannerSteerCount("42"); got != 2 {
		t.Fatalf("PlannerSteerCount=%d, want 2", got)
	}
	if msg, ok := e.drainPlannerSteer(42); !ok || msg != "先查 /admin" {
		t.Fatalf("first drainPlannerSteer=(%q, %v), want (%q, true)", msg, ok, "先查 /admin")
	}
	if !e.StopPlannerTurn("42") {
		t.Fatal("StopPlannerTurn returned false for active run")
	}
	if !errors.Is(context.Cause(runCtx), agent.AbortPlannerStoppedByUser) {
		t.Fatalf("expected AbortPlannerStoppedByUser cause, got %v", context.Cause(runCtx))
	}
	if got := e.PlannerSteerCount("42"); got != 0 {
		t.Fatalf("StopPlannerTurn should drop queued steer, got %d", got)
	}
	e.endPlannerRun("42", seq, cancel)
	if e.IsPlannerRunning("42") {
		t.Fatal("expected planner idle after endPlannerRun")
	}

	e.clearPlannerState("42")

	tk := &Task{ID: "42", notify: make(chan struct{}, 1)}
	tk.NotifyUrgentTrigger(agent.TriggerEvent{Kind: "human_chat", HumanMessage: "优先验证后台"})
	if !tk.urgentWake.Load() || !tk.HasUrgentTrigger() {
		t.Fatal("NotifyUrgentTrigger did not set urgentWake and pending human_chat")
	}
	drained := tk.drainTriggers()
	if len(drained) != 1 || drained[0].Kind != "human_chat" {
		t.Fatalf("unexpected drained triggers: %+v", drained)
	}

	filtered := nonHumanChatTriggers([]agent.TriggerEvent{
		{Kind: "goal", Goals: []string{"g"}},
		{Kind: "human_chat", HumanMessage: "chat"},
		{Kind: "finding", Detail: "f"},
	})
	if len(filtered) != 2 || filtered[0].Kind != "goal" || filtered[1].Kind != "finding" {
		t.Fatalf("nonHumanChatTriggers=%+v", filtered)
	}

	f, ok := parseActivitySession("plan")
	if !ok || f.Worker != "planner" {
		t.Fatalf("parseActivitySession(\"plan\")=(%+v, %v)", f, ok)
	}
}

func setupPlannerChatServer(t *testing.T, prov *plannerChatTestProvider, maxTurns int, withOpenGoal bool) (*Server, *Task, *agent.Planner, context.CancelFunc) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ctx: ctx, m: m, engine: NewEngine(m)}
	s.engine.SetBaseContext(ctx)
	s.llm.on, s.llm.prov = true, prov

	task, err := m.CreateTask("Planner Direct Chat", "验证目标攻击面", nil, 0, 0)
	if err != nil {
		cancel()
		m.Close()
		t.Fatal(err)
	}
	if withOpenGoal {
		if _, err := task.Store.AddNode(db.KindGoal, map[string]any{"text": "验证目标攻击面"}, 1, "open", "human", nil); err != nil {
			cancel()
			_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
			m.Close()
			t.Fatal(err)
		}
	}
	tx := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
	planner := agent.NewPlanner(prov, "test-model", t.TempDir(), tx, 0, maxTurns)
	planner.SetNonStreaming(func() bool { return true })
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return planner, nil
	})
	t.Cleanup(func() {
		cancel()
		drain, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = s.waitTaskQuiescent(drain, task.ID)
		s.engine.StopTask(task.ID)
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
		m.Close()
	})
	return s, task, planner, cancel
}

func postPlannerMsgHTTP(s *Server, taskID string, msg string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"message": msg, "request_id": "req-1"})
	r := httptest.NewRequest("POST", "/api/tasks/"+taskID+"/planner/messages", strings.NewReader(string(body)))
	r.SetPathValue("id", taskID)
	w := httptest.NewRecorder()
	s.sendPlannerMessage(w, r)
	return w
}

func TestPlannerChatImmediateBypassDebounce(t *testing.T) {
	doneReq := make(chan llm.CompletionRequest, 1)
	prov := &plannerChatTestProvider{
		complete: func(_ context.Context, _ int, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
			select {
			case doneReq <- req:
			default:
			}
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("已收到，先看 /admin。")}}, "end_turn", llm.Usage{}, nil
		},
	}
	s, task, _, cancel := setupPlannerChatServer(t, prov, 4, true)
	s.engine.debounce = 2 * time.Second
	s.engine.started.Store(task.ID, true)

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		s.engine.plannerLoop(s.ctx, task)
	}()
	defer func() {
		cancel()
		<-loopDone
	}()

	start := time.Now()
	w := postPlannerMsgHTTP(s, task.ID, "先优先检查 /admin 接口")
	if w.Code != 200 {
		t.Fatalf("POST planner message HTTP %d: %s", w.Code, w.Body.String())
	}
	var resp PlannerMessageResult
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Accepted || resp.Mode != "woken" || !resp.Running {
		t.Fatalf("unexpected response: %+v", resp)
	}

	var seenReq llm.CompletionRequest
	select {
	case seenReq = <-doneReq:
	case <-time.After(1200 * time.Millisecond):
		t.Fatalf("plannerLoop did not wake immediately (elapsed=%v; expected < 1s bypassing 2s debounce)", time.Since(start))
	}
	if !reqHasSubstring(seenReq, "【操作者直接在 Planner 对话框对你说话】") || !reqHasSubstring(seenReq, "先优先检查 /admin 接口") {
		t.Fatalf("planner request missing human_chat prompt")
	}

	acts, _, err := task.Store.ActivityPage(db.ActivitySessionFilter{Worker: "planner"}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var foundUser bool
	for _, a := range acts {
		if a.Worker == "planner" && a.Kind == "user" && strings.Contains(a.Summary, "先优先检查 /admin 接口") {
			foundUser = true
		}
	}
	if !foundUser {
		t.Fatalf("missing persisted planner user activity in plan session: %+v", acts)
	}
}

func TestPlannerChatSteerWhileRunningAndResidualRecovery(t *testing.T) {
	t.Run("mid-round steer delivered via Stop hook", func(t *testing.T) {
		inCall1 := make(chan struct{})
		allowCall1 := make(chan struct{})
		call2Req := make(chan llm.CompletionRequest, 1)
		prov := &plannerChatTestProvider{
			complete: func(ctx context.Context, idx int, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				if idx == 0 {
					close(inCall1)
					select {
					case <-allowCall1:
					case <-ctx.Done():
						return llm.Message{}, "", llm.Usage{}, ctx.Err()
					}
					return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("准备结束第一轮。")}}, "end_turn", llm.Usage{}, nil
				}
				select {
				case call2Req <- req:
				default:
				}
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("已据此改道。")}}, "end_turn", llm.Usage{}, nil
			},
		}
		s, task, _, cancel := setupPlannerChatServer(t, prov, 6, true)
		s.engine.started.Store(task.ID, true)
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			s.engine.plannerLoop(s.ctx, task)
		}()
		defer func() {
			cancel()
			<-loopDone
		}()

		task.Notify() // kick initial round
		select {
		case <-inCall1:
		case <-time.After(4 * time.Second):
			t.Fatal("round 1 did not start")
		}

		w := postPlannerMsgHTTP(s, task.ID, "等一下，优先查 /sso")
		if w.Code != 200 {
			t.Fatalf("steer HTTP %d: %s", w.Code, w.Body.String())
		}
		var resp PlannerMessageResult
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Mode != "steered" || !resp.Steered || !resp.Running || resp.Queued != 1 {
			t.Fatalf("expected steered/queued=1, got %+v", resp)
		}

		sw := httptest.NewRecorder()
		sr := httptest.NewRequest("GET", "/api/tasks/"+task.ID+"/planner/status", nil)
		sr.SetPathValue("id", task.ID)
		s.plannerChatStatus(sw, sr)
		var statusResp map[string]any
		_ = json.Unmarshal(sw.Body.Bytes(), &statusResp)
		if statusResp["running"] != true || statusResp["queued"] != float64(1) {
			t.Fatalf("unexpected planner status: %+v", statusResp)
		}

		close(allowCall1)
		select {
		case req := <-call2Req:
			if !reqHasSubstring(req, "【操作者实时对话提示】等一下，优先查 /sso") {
				t.Fatalf("turn 2 did not receive Stop-hook steered hint")
			}
		case <-time.After(4 * time.Second):
			t.Fatal("round 1 did not continue with steered Stop-hook message")
		}
	})

	t.Run("residual steer unconsumed due to maxTurns wakes immediate next round", func(t *testing.T) {
		inCall1 := make(chan struct{})
		allowCall1 := make(chan struct{})
		round2Req := make(chan llm.CompletionRequest, 1)
		prov := &plannerChatTestProvider{
			complete: func(ctx context.Context, idx int, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				if idx == 0 {
					close(inCall1)
					select {
					case <-allowCall1:
					case <-ctx.Done():
						return llm.Message{}, "", llm.Usage{}, ctx.Err()
					}
					return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("第 1 轮已达单轮上限。")}}, "end_turn", llm.Usage{}, nil
				}
				select {
				case round2Req <- req:
				default:
				}
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("第 2 轮已拾取残留提示。")}}, "end_turn", llm.Usage{}, nil
			},
		}
		// maxTurns=1 so Stop hook cannot trigger an extra turn inside Round 1;
		// queued steer stays unconsumed until plannerLoop drains it after Plan exits.
		s, task, _, cancel := setupPlannerChatServer(t, prov, 1, true)
		s.engine.debounce = 2 * time.Second
		s.engine.started.Store(task.ID, true)
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			s.engine.plannerLoop(s.ctx, task)
		}()
		defer func() {
			cancel()
			<-loopDone
		}()

		task.Notify()
		select {
		case <-inCall1:
		case <-time.After(4 * time.Second):
			t.Fatal("round 1 did not start")
		}

		w := postPlannerMsgHTTP(s, task.ID, "残留消息请在下一轮立刻处理")
		if w.Code != 200 {
			t.Fatalf("steer HTTP %d: %s", w.Code, w.Body.String())
		}
		startWake := time.Now()
		close(allowCall1)

		select {
		case req := <-round2Req:
			if time.Since(startWake) >= 1500*time.Millisecond {
				t.Fatalf("residual wakeup did not bypass 2s debounce (elapsed=%v)", time.Since(startWake))
			}
			if !reqHasSubstring(req, "残留消息请在下一轮立刻处理") {
				t.Fatalf("round 2 missing residual steer message")
			}
		case <-time.After(4 * time.Second):
			t.Fatal("residual steer did not wake round 2")
		}
	})
}

func TestPlannerChatWhilePausedStaysPausedAndNoWorkers(t *testing.T) {
	seenReq := make(chan llm.CompletionRequest, 1)
	prov := &plannerChatTestProvider{
		complete: func(_ context.Context, _ int, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
			select {
			case seenReq <- req:
			default:
			}
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("暂停期间已记录，恢复后再执行。")}}, "end_turn", llm.Usage{}, nil
		},
	}
	s, task, _, _ := setupPlannerChatServer(t, prov, 4, true)

	if err := s.m.SetTaskPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	s.engine.Pause(task.ID, nil)
	iid, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "待执行探测"}, 1, "open", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}

	w := postPlannerMsgHTTP(s, task.ID, "为什么刚刚没有推进 /api？")
	if w.Code != 200 {
		t.Fatalf("paused interactive chat HTTP %d: %s", w.Code, w.Body.String())
	}
	var resp PlannerMessageResult
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Mode != "interactive" || !resp.Paused {
		t.Fatalf("expected interactive paused mode, got %+v", resp)
	}

	select {
	case req := <-seenReq:
		if !reqHasSubstring(req, "【当前任务已暂停】") {
			t.Fatalf("paused interactive trigger missing paused guidance")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("paused interactive planner turn did not run")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.engine.IsPlannerRunning(task.ID) {
		time.Sleep(20 * time.Millisecond)
	}
	if !s.engine.IsPaused(task.ID) {
		t.Fatal("interactive planner chat must not clear engine paused state")
	}
	if lc := task.lifecycleSnapshot(); !lc.Paused {
		t.Fatalf("task lifecycle paused=%v, want true", lc.Paused)
	}
	if node, err := task.Store.GetNode(iid); err != nil || node == nil || node.State != "open" {
		t.Fatalf("paused interactive chat unexpectedly mutated open intent: %+v err=%v", node, err)
	}
}

func TestPlannerStopTurnDoesNotAutoRetryAndValidation(t *testing.T) {
	inCall1 := make(chan struct{})
	var callCtxErr error
	var causeMu sync.Mutex
	prov := &plannerChatTestProvider{
		complete: func(ctx context.Context, idx int, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
			if idx == 0 {
				close(inCall1)
			}
			<-ctx.Done()
			causeMu.Lock()
			callCtxErr = context.Cause(ctx)
			causeMu.Unlock()
			return llm.Message{}, "", llm.Usage{}, ctx.Err()
		},
	}
	s, task, _, cancel := setupPlannerChatServer(t, prov, 4, true)

	// Validation checks before launching loop:
	if w := postPlannerMsgHTTP(s, task.ID, "   "); w.Code != 400 {
		t.Fatalf("empty message HTTP %d, want 400", w.Code)
	}
	if w := postPlannerMsgHTTP(s, task.ID, strings.Repeat("a", 4001)); w.Code != 400 {
		t.Fatalf("too-long message HTTP %d, want 400", w.Code)
	}

	s.engine.started.Store(task.ID, true)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		s.engine.plannerLoop(s.ctx, task)
	}()
	defer func() {
		cancel()
		<-loopDone
	}()

	task.Notify()
	select {
	case <-inCall1:
	case <-time.After(4 * time.Second):
		t.Fatal("round 1 did not start")
	}

	// Queue one steer then immediately stop the turn.
	if w := postPlannerMsgHTTP(s, task.ID, "马上丢弃这条实时提示"); w.Code != 200 {
		t.Fatalf("steer HTTP %d: %s", w.Code, w.Body.String())
	}
	stopW := httptest.NewRecorder()
	stopR := httptest.NewRequest("POST", "/api/tasks/"+task.ID+"/planner/stop", nil)
	stopR.SetPathValue("id", task.ID)
	s.stopPlannerTurn(stopW, stopR)
	if stopW.Code != 200 || !strings.Contains(stopW.Body.String(), `"stopping"`) {
		t.Fatalf("stopPlannerTurn HTTP %d: %s", stopW.Code, stopW.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.engine.IsPlannerRunning(task.ID) {
		time.Sleep(20 * time.Millisecond)
	}
	causeMu.Lock()
	gotCause := callCtxErr
	causeMu.Unlock()
	if !errors.Is(gotCause, agent.AbortPlannerStoppedByUser) {
		t.Fatalf("expected AbortPlannerStoppedByUser cancel cause, got %v", gotCause)
	}
	if q := s.engine.PlannerSteerCount(task.ID); q != 0 {
		t.Fatalf("expected steer queue cleared after stop, got %d", q)
	}
	// Ensure stopped turn does NOT auto-retry across 2.5s window.
	time.Sleep(2300 * time.Millisecond)
	if gotCalls := prov.callCount(); gotCalls != 1 {
		t.Fatalf("user-stopped planner round unexpectedly retried: calls=%d, want 1", gotCalls)
	}

	acts, _, err := task.Store.ActivityPage(db.ActivitySessionFilter{Worker: "planner"}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var foundStopNote bool
	for _, a := range acts {
		if strings.Contains(a.Summary, "本轮规划已停止") {
			foundStopNote = true
		}
	}
	if !foundStopNote {
		t.Fatalf("missing user-stop notice activity: %+v", acts)
	}

	if !s.engine.BeginDelete(task.ID) {
		t.Fatal("BeginDelete returned false")
	}
	if w := postPlannerMsgHTTP(s, task.ID, "删除中发送应被拒"); w.Code != 409 {
		t.Fatalf("deleting task message HTTP %d, want 409", w.Code)
	}
}

func postResetPlannerContextHTTP(s *Server, taskID, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/tasks/"+taskID+"/planner/context/reset", strings.NewReader(body))
	r.SetPathValue("id", taskID)
	w := httptest.NewRecorder()
	s.resetPlannerContext(w, r)
	return w
}

func TestPlannerResetContextAPIAndWakesCleanRound(t *testing.T) {
	inCall1 := make(chan struct{})
	call2Req := make(chan llm.CompletionRequest, 1)
	var causeMu sync.Mutex
	var call1Cause error

	prov := &plannerChatTestProvider{
		complete: func(ctx context.Context, callIdx int, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
			if callIdx == 0 {
				close(inCall1)
				<-ctx.Done()
				causeMu.Lock()
				call1Cause = context.Cause(ctx)
				causeMu.Unlock()
				return llm.Message{}, "", llm.Usage{}, ctx.Err()
			}
			select {
			case call2Req <- req:
			default:
			}
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("已切换干净上下文，重新评估目标。")}}, "end_turn", llm.Usage{}, nil
		},
	}
	s, task, planner, cancel := setupPlannerChatServer(t, prov, 4, true)
	s.engine.debounce = 2 * time.Second
	if err := task.Store.SavePlannerState(tidOfTask(task), []db.PlannerTodoItem{{Content: "保留待办", Status: "pending"}}, "应被清空的旧结论ABC"); err != nil {
		t.Fatal(err)
	}

	if w := postResetPlannerContextHTTP(s, "missing-task-id", `{}`); w.Code != 404 {
		t.Fatalf("missing task reset HTTP %d, want 404", w.Code)
	}
	if w := postResetPlannerContextHTTP(s, task.ID, `{bad`); w.Code != 400 {
		t.Fatalf("bad json reset HTTP %d, want 400", w.Code)
	}

	s.engine.started.Store(task.ID, true)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		s.engine.plannerLoop(s.ctx, task)
	}()
	defer func() {
		cancel()
		<-loopDone
	}()

	task.Notify()
	select {
	case <-inCall1:
	case <-time.After(4 * time.Second):
		t.Fatal("round 1 did not start")
	}

	// Queue a mid-round steer that belongs to the dirty round and should be dropped on reset.
	if w := postPlannerMsgHTTP(s, task.ID, "旧上下文的过时提示"); w.Code != 200 {
		t.Fatalf("steer HTTP %d: %s", w.Code, w.Body.String())
	}

	start := time.Now()
	resetW := postResetPlannerContextHTTP(s, task.ID, `{"clear_todos":false,"reason":"聚焦鉴权链"}`)
	if resetW.Code != 200 {
		t.Fatalf("resetPlannerContext HTTP %d: %s", resetW.Code, resetW.Body.String())
	}
	var res PlannerContextResetResult
	if err := json.Unmarshal(resetW.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.ContextSeg != 1 || !res.WasRunning || !res.Woken || !res.Running || res.ClearedTodos {
		t.Fatalf("unexpected reset response: %+v", res)
	}
	causeMu.Lock()
	gotCause := call1Cause
	causeMu.Unlock()
	if !errors.Is(gotCause, agent.AbortPlannerContextReset) {
		t.Fatalf("expected AbortPlannerContextReset, got %v", gotCause)
	}

	select {
	case req2 := <-call2Req:
		if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
			t.Fatalf("clean follow-up round took too long (%v), expected urgent wake bypassing debounce", elapsed)
		}
		if reqHasSubstring(req2, "应被清空的旧结论ABC") || reqHasSubstring(req2, "旧上下文的过时提示") {
			t.Fatal("follow-up clean round leaked old conclusion or dropped steer")
		}
		if !reqHasSubstring(req2, "已清空 Planner 先前上下文记忆") || !reqHasSubstring(req2, "聚焦鉴权链") {
			t.Fatal("follow-up clean round missing context_reset trigger guidance")
		}
		if !reqHasSubstring(req2, "保留待办") {
			t.Fatal("clear_todos=false should preserve TodoWrite item")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("clean follow-up round did not run after reset")
	}

	// Paused reset should NOT wake a new round and clear_todos=true wipes TodoWrite.
	s.engine.paused.Store(task.ID, true)
	pauseResetW := postResetPlannerContextHTTP(s, task.ID, `{"clear_todos":true}`)
	if pauseResetW.Code != 200 {
		t.Fatalf("paused reset HTTP %d: %s", pauseResetW.Code, pauseResetW.Body.String())
	}
	var res2 PlannerContextResetResult
	_ = json.Unmarshal(pauseResetW.Body.Bytes(), &res2)
	if res2.ContextSeg != 2 || !res2.ClearedTodos || res2.Woken || res2.Running {
		t.Fatalf("unexpected paused reset result: %+v", res2)
	}
	stDB, _, _ := task.Store.LoadPlannerState(tidOfTask(task))
	if stDB.ContextSeg != 2 || len(stDB.Todos) != 0 || stDB.LastRoundNote != "" {
		t.Fatalf("expected DB todos cleared and context_seg=2, got %+v", stDB)
	}
	acts, _, err := task.Store.ActivityPage(db.ActivitySessionFilter{Worker: "planner"}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var foundBoundary bool
	for _, a := range acts {
		if a.Kind == "round" && strings.Contains(a.Summary, "已清空 Planner 上下文记忆") {
			foundBoundary = true
		}
	}
	if !foundBoundary {
		t.Fatalf("expected planner activity boundary divider, got %+v", acts)
	}
	_ = planner

	if !s.engine.BeginDelete(task.ID) {
		t.Fatal("BeginDelete returned false")
	}
	if w := postResetPlannerContextHTTP(s, task.ID, `{}`); w.Code != 409 {
		t.Fatalf("deleting task reset HTTP %d, want 409", w.Code)
	}
}

func TestMainAgentResetPlannerContextToolEndToEnd(t *testing.T) {
	prov := &plannerChatTestProvider{
		complete: func(_ context.Context, callIdx int, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
			if callIdx == 0 {
				return llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentBlock{{
						Type:  llm.BlockToolUse,
						ID:    "toolu_reset_1",
						Name:  "reset_planner_context",
						Input: json.RawMessage(`{"clear_todos":false,"reason":"主Agent要求重置"}`),
					}},
				}, "tool_use", llm.Usage{}, nil
			}
			return llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.ContentBlock{llm.TextBlock("已清空 Planner 上下文，后续将从干净上下文继续规划。")},
			}, "end_turn", llm.Usage{}, nil
		},
	}
	s, task, _, _ := setupPlannerChatServer(t, prov, 4, true)
	s.agents.bundles = map[string]*taskAgentBundle{}
	if err := task.Store.SavePlannerState(tidOfTask(task), []db.PlannerTodoItem{{Content: "keep", Status: "pending"}}, "应被清空的旧摘要"); err != nil {
		t.Fatal(err)
	}

	bundle := s.agentsForTask(task)
	if got := bundle.plannerRuntime.poolSessionHint(); got != agent.PlannerSessionID(task.ExpID, 0) {
		t.Fatalf("initial poolSessionHint=%q, want exp%d-planner", got, task.ExpID)
	}
	reply, err := bundle.main.Chat(t.Context(), tidOfTask(task), 0, s.m.Assets(), task.Store, task.Goal, "帮我清空 planner 上下文", nil, task.Notify, func() {}, task.NotifyGoal, task.NotifyHint)
	if err != nil {
		t.Fatalf("main.Chat failed: %v", err)
	}
	if !strings.Contains(reply, "已清空") {
		t.Fatalf("unexpected reply: %s", reply)
	}
	st, found, err := task.Store.LoadPlannerState(tidOfTask(task))
	if err != nil || !found {
		t.Fatalf("LoadPlannerState err=%v found=%v", err, found)
	}
	if st.ContextSeg != 1 || st.LastRoundNote != "" || len(st.Todos) != 1 {
		t.Fatalf("expected context_seg=1, empty note, preserved todo; got %+v", st)
	}
	if got := bundle.plannerRuntime.poolSessionHint(); got != agent.PlannerSessionID(task.ExpID, 1) {
		t.Fatalf("post-reset poolSessionHint=%q, want exp%d-planner-s1", got, task.ExpID)
	}
}
