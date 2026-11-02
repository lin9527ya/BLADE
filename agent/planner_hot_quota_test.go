package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/policy"
)

// intentRoundProvider 每轮第一次调用返回一批 add_intent 工具调用，工具结果回灌后的
// 第二次调用返回结论文本（结束本轮）。
type intentRoundProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *intentRoundProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(req.Messages); n > 0 {
		for _, b := range req.Messages[n-1].Content {
			if b.Type == llm.BlockToolResult {
				return assistantMsg("结论：本轮已派发方向"), "end_turn", llm.Usage{}, nil
			}
		}
	}
	p.calls++
	items := ""
	for i := 0; i < 4; i++ {
		if i > 0 {
			items += ","
		}
		items += fmt.Sprintf(`{"risk_area":"other","summary":"热会话第 %d 次调用方向 %d","priority":9}`, p.calls, i)
	}
	return toolUseMsg(fmt.Sprintf("tu-%d", p.calls), "add_intent", `{"intents":[`+items+`]}`),
		"tool_use", llm.Usage{}, nil
}

func (p *intentRoundProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("test provider is non-streaming only"))
	}
}

// 热会话复用时，模型调用的是**首轮那个** ToolSet 的闭包（Session 的 registry 在
// NewSession 时固定）。每轮额度必须重置在那个对象上；只重置每轮新建的 ToolSet 会让
// 第 2 轮起的 add_intent 全被 pace:round 拒掉，最终连续空轮被判策略封死。
func TestHotSessionQuotaResetsEachRound(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("hot quota", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	p := NewPlanner(&intentRoundProvider{}, "m", t.TempDir(), nil, 0, 8)
	p.SetNonStreaming(func() bool { return true })
	p.SetPolicyResolver(func() *policy.Effective {
		return &policy.Effective{Version: 1, MaxIntentsPerRound: 4}
	})
	p.SetWorkersResolver(func() int { return 0 })
	for r := 0; r < 3; r++ {
		if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "g", nil, nil); err != nil {
			t.Fatalf("第 %d 轮 Plan: %v", r+1, err)
		}
	}
	intents, err := ts.ListByKind(db.KindIntent, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 12 {
		t.Fatalf("热身 3 轮×4 条应建 12 条意图，实际 %d（热会话里额度没有按轮重置）", len(intents))
	}
	if p.hotRounds < 2 {
		t.Fatalf("应复用热会话，rounds=%d", p.hotRounds)
	}
}
