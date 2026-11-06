package server

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/agent"
)

// 复核判"证据不足"时不暂停，并把结论交给 planner 下一轮；判"充分"才放行暂停。
func TestReviewExhaustionGatesPause(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("exhaustion review", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(tid)
	e := NewEngine(m)
	e.SetConstraintResolver(allConstraints)

	calls := 0
	e.SetExhaustionReviewer(func(_ context.Context, claim agent.ExhaustionClaim) (agent.ExhaustionVerdict, error) {
		calls++
		if claim.Reason == "" {
			t.Error("复核应带上 planner 的原话")
		}
		if calls == 1 {
			return agent.ExhaustionVerdict{Accurate: false, Reason: "多个方向只有工具失败记录",
				Gaps: []string{"#12 是超时，不是否定结论"}}, nil
		}
		return agent.ExhaustionVerdict{Accurate: true, Reason: "证据充分"}, nil
	})

	allowed, _ := e.reviewExhaustion(t.Context(), task, "策略内已无可行方向")
	if allowed {
		t.Fatal("复核判证据不足时不应放行暂停")
	}
	note := task.TakeExhaustionNote()
	if !strings.Contains(note, "超时") {
		t.Fatalf("未通过的结论应交给 planner：%q", note)
	}
	if again := task.TakeExhaustionNote(); again != "" {
		t.Fatalf("反馈应一次消费，实际 %q", again)
	}

	allowed, _ = e.reviewExhaustion(t.Context(), task, "策略内已无可行方向")
	if !allowed {
		t.Fatal("复核通过时应放行暂停")
	}
}

// 复核员不可用时按「证据不足」处理：看门狗挂了就放行穷尽，等于它最需要的时候不在。
func TestReviewExhaustionFailsOpenTowardContinuing(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("exhaustion review err", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(tid)
	e := NewEngine(m)
	e.SetConstraintResolver(allConstraints)
	e.SetExhaustionReviewer(func(context.Context, agent.ExhaustionClaim) (agent.ExhaustionVerdict, error) {
		return agent.ExhaustionVerdict{}, errors.New("model unavailable")
	})
	if allowed, _ := e.reviewExhaustion(t.Context(), task, "无方向"); allowed {
		t.Fatal("复核失败时不应放行暂停（失败策略是继续跑）")
	}
}

// 连续被判不准确达到上限后放行，避免看门狗自己把任务永久卡住。
func TestReviewExhaustionCapsRejections(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("exhaustion review cap", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(tid)
	e := NewEngine(m)
	e.SetConstraintResolver(allConstraints)
	e.SetExhaustionReviewer(func(context.Context, agent.ExhaustionClaim) (agent.ExhaustionVerdict, error) {
		return agent.ExhaustionVerdict{Accurate: false, Reason: "证据不足"}, nil
	})
	for i := 0; i < exhaustionReviewMaxAttempts; i++ {
		if allowed, _ := e.reviewExhaustion(t.Context(), task, "无方向"); allowed {
			t.Fatalf("第 %d 次不应放行", i+1)
		}
	}
	if allowed, note := e.reviewExhaustion(t.Context(), task, "无方向"); !allowed {
		t.Fatalf("达到上限后应放行，note=%q", note)
	}
}
