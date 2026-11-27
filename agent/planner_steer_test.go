package agent

import (
	"context"
	"encoding/json"
	"iter"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/db"
)

type scriptedPlannerProvider struct {
	mu       sync.Mutex
	calls    int
	requests []llm.CompletionRequest
	steps    []func(callIdx int, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error)
}

func (p *scriptedPlannerProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {}
}

func (p *scriptedPlannerProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.requests = append(p.requests, req)
	var step func(int, llm.CompletionRequest) (llm.Message, string, llm.Usage, error)
	if idx < len(p.steps) {
		step = p.steps[idx]
	}
	p.mu.Unlock()
	if step != nil {
		return step(idx, req)
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}}, "end_turn", llm.Usage{}, nil
}

func blockContainsText(b llm.ContentBlock, substr string) bool {
	if strings.Contains(b.Text, substr) || strings.Contains(b.Thinking, substr) {
		return true
	}
	for _, nested := range b.Content {
		if blockContainsText(nested, substr) {
			return true
		}
	}
	return false
}

func requestContainsText(req llm.CompletionRequest, substr string) bool {
	for _, m := range req.Messages {
		if strings.Contains(m.Text(), substr) {
			return true
		}
		for _, b := range m.Content {
			if blockContainsText(b, substr) {
				return true
			}
		}
	}
	return false
}

func TestPlannerHumanChatTriggerRenderPriorityAndWindow(t *testing.T) {
	if got := TriggerPriority("human_chat"); got != 4 {
		t.Fatalf("TriggerPriority(human_chat)=%d, want 4", got)
	}
	if !hasHumanChatTrigger([]TriggerEvent{{Kind: "human_chat", HumanMessage: "优先验证 /admin"}}) {
		t.Fatal("hasHumanChatTrigger returned false for non-empty human_chat")
	}
	if hasHumanChatTrigger([]TriggerEvent{{Kind: "human_chat", HumanMessage: "   "}, {Kind: "timer"}}) {
		t.Fatal("hasHumanChatTrigger returned true without a message")
	}

	// human_chat has priority 4 and must survive selectTriggerWindow even when
	// saturated by priority-3 events.
	events := []TriggerEvent{
		{Kind: "human_chat", HumanMessage: "先看后台登录"},
		{Kind: "goal", Goals: []string{"g1"}},
		{Kind: "hint", Hints: []string{"h1"}},
		{Kind: "finding", Detail: "f1"},
	}
	selected := selectTriggerWindow(events, 2)
	if len(selected) != 2 || !hasHumanChatTrigger(selected) {
		t.Fatalf("selectTriggerWindow dropped human_chat: %+v", selected)
	}

	renderedPaused := renderTriggers(new(db.ExplorationStore), []TriggerEvent{{
		Kind:              "human_chat",
		HumanMessage:      "为什么暂停前跳过 /upload？",
		InteractivePaused: true,
	}})
	for _, want := range []string{"【操作者直接在 Planner 对话框对你说话】", "为什么暂停前跳过 /upload？", "【当前任务已暂停】"} {
		if !strings.Contains(renderedPaused, want) {
			t.Fatalf("paused trigger rendering missing %q: %s", want, renderedPaused)
		}
	}

	renderedTerm := renderTriggers(new(db.ExplorationStore), []TriggerEvent{{
		Kind:                "human_chat",
		HumanMessage:        "总结关键证据链",
		InteractiveTerminal: true,
	}})
	if !strings.Contains(renderedTerm, "【当前任务已无未完成目标】") {
		t.Fatalf("terminal trigger rendering missing notice: %s", renderedTerm)
	}

	sys := plannerSystem("目标", "/tmp/data", "/tmp/work")
	if !strings.Contains(sys, "【与操作者直接对话（代码持有）】") {
		t.Fatalf("plannerSystem missing direct dialogue guidance")
	}
}

func TestPlannerSteerHooksUnitBehavior(t *testing.T) {
	var q []string
	h := plannerSteerHooks{drain: func() (string, bool) {
		if len(q) == 0 {
			return "", false
		}
		top := q[0]
		q = q[1:]
		return top, true
	}}

	if block, msg, _ := h.PreToolUse(context.Background(), "TodoWrite", nil); block || msg != "" {
		t.Fatalf("expected empty PreToolUse pass-through, got (%v, %q)", block, msg)
	}
	if prevent, blocking, msg := h.Stop(context.Background(), nil); prevent || len(blocking) != 0 || msg != "" {
		t.Fatalf("expected empty Stop pass-through, got (%v, %v, %q)", prevent, blocking, msg)
	}

	q = append(q, "改查 SSO")
	block, msg, _ := h.PreToolUse(context.Background(), "TodoWrite", nil)
	if !block || !strings.Contains(msg, "【操作者实时对话提示】改查 SSO") {
		t.Fatalf("PreToolUse did not block with formatted steer: (%v, %q)", block, msg)
	}

	q = append(q, "请顺便回复当前队列状态")
	prevent, blocking, _ := h.Stop(context.Background(), nil)
	if prevent || len(blocking) != 1 || !strings.Contains(blocking[0], "【操作者实时对话提示】请顺便回复当前队列状态") {
		t.Fatalf("Stop did not inject continuation reason: (%v, %v)", prevent, blocking)
	}
}

func TestPlannerPlanStopHookAndPreToolUseSteer(t *testing.T) {
	t.Run("stop hook injects mid-round steer before returning", func(t *testing.T) {
		d := testDB(t)
		defer d.Close()
		task, err := d.CreateTaskWithOptions("steer stop", "验证登录面", db.TaskCreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer d.DeleteTask(task.ID)
		ts := d.Exploration(task.ExplorationID)

		var qMu sync.Mutex
		var q []string
		prov := &scriptedPlannerProvider{}
		prov.steps = []func(int, llm.CompletionRequest) (llm.Message, string, llm.Usage, error){
			func(int, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				qMu.Lock()
				q = append(q, "别结束，说明为什么优先验证 /admin")
				qMu.Unlock()
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("准备结束本轮。")}}, "end_turn", llm.Usage{}, nil
			},
			func(_ int, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				if !requestContainsText(req, "【操作者实时对话提示】别结束，说明为什么优先验证 /admin") {
					t.Fatalf("turn 2 did not receive Stop-hook steer message in %+v", req.Messages)
				}
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("因为 /admin 存在未鉴权表单。")}}, "end_turn", llm.Usage{}, nil
			},
		}

		tx := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
		p := NewPlanner(prov, "test-model", t.TempDir(), tx, 0, 6)
		p.SetNonStreaming(func() bool { return true })
		p.SetHumanSteerDrain(func(gotTaskID int64) (string, bool) {
			if gotTaskID != task.ID {
				return "", false
			}
			qMu.Lock()
			defer qMu.Unlock()
			if len(q) == 0 {
				return "", false
			}
			msg := q[0]
			q = q[1:]
			return msg, true
		})

		_, _, err = p.Plan(t.Context(), task.ID, d.Assets(), ts, "验证登录面", []TriggerEvent{{Kind: "timer"}}, nil)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if prov.calls != 2 {
			t.Fatalf("expected 2 LLM calls due to Stop hook steer, got %d", prov.calls)
		}
	})

	t.Run("pre-tool-use blocks TodoWrite and delivers steer", func(t *testing.T) {
		d := testDB(t)
		defer d.Close()
		task, err := d.CreateTaskWithOptions("steer pretool", "验证上传面", db.TaskCreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer d.DeleteTask(task.ID)
		ts := d.Exploration(task.ExplorationID)

		var qMu sync.Mutex
		var q []string
		rawTodo, _ := json.Marshal(map[string]any{"todos": []map[string]string{{"content": "旧待办", "status": "pending"}}})
		prov := &scriptedPlannerProvider{}
		prov.steps = []func(int, llm.CompletionRequest) (llm.Message, string, llm.Usage, error){
			func(int, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				qMu.Lock()
				q = append(q, "暂停写入旧待办，先回答上传约束")
				qMu.Unlock()
				return llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentBlock{{
						Type:  llm.BlockToolUse,
						ID:    "tool-1",
						Name:  "TodoWrite",
						Input: rawTodo,
					}},
				}, "tool_use", llm.Usage{}, nil
			},
			func(_ int, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				if !requestContainsText(req, "【操作者实时对话提示】暂停写入旧待办，先回答上传约束") {
					t.Fatalf("turn 2 did not receive PreToolUse steer block message")
				}
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("收到，已跳过旧待办。")}}, "end_turn", llm.Usage{}, nil
			},
		}

		tx := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
		p := NewPlanner(prov, "test-model", t.TempDir(), tx, 0, 6)
		p.SetNonStreaming(func() bool { return true })
		p.SetHumanSteerDrain(func(int64) (string, bool) {
			qMu.Lock()
			defer qMu.Unlock()
			if len(q) == 0 {
				return "", false
			}
			msg := q[0]
			q = q[1:]
			return msg, true
		})

		_, _, err = p.Plan(t.Context(), task.ID, d.Assets(), ts, "验证上传面", []TriggerEvent{{Kind: "timer"}}, nil)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if prov.calls != 2 {
			t.Fatalf("expected 2 LLM calls due to PreToolUse block, got %d", prov.calls)
		}
		state, _, err := ts.LoadPlannerState(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Todos) != 0 {
			t.Fatalf("blocked TodoWrite should not have persisted todos, got %+v", state.Todos)
		}
	})
}
