package server

import (
	"errors"
	"testing"

	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/blade/agent"
)

// 空跑收笔：正常结束但什么都没写回，必须落 no_output 而不是 done——
// 否则 planner 当成"已覆盖"不再派、覆盖度当成"已测"，整条方向静默消失。
func TestSettleIntentState(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		reason  harness.TerminalReason
		wrote   agent.WriteCounts
		outcome string
		want    string
	}{
		{"有写回的正常结束 → done", nil, harness.ReasonCompleted, agent.WriteCounts{Facts: 1}, "", "done"},
		{"只写了资产也算有产出", nil, harness.ReasonCompleted, agent.WriteCounts{Assets: 3}, "", "done"},
		{"什么都没写回 → no_output", nil, harness.ReasonCompleted, agent.WriteCounts{}, "", "no_output"},
		{"语义 negative 压过空跑（否定结论本身就是产出）", nil, harness.ReasonCompleted, agent.WriteCounts{}, "negative", "negative"},
		{"语义 out-of-scope 压过空跑", nil, harness.ReasonCompleted, agent.WriteCounts{}, "out-of-scope", "out-of-scope"},
		{"出错 → blocked（不被空跑覆盖）", errors.New("boom"), harness.ReasonCompleted, agent.WriteCounts{}, "", "blocked"},
		{"撞步数上限 → exhausted（即使写了点东西）", nil, harness.ReasonMaxTurns, agent.WriteCounts{Facts: 1}, "", "exhausted"},
		{"超时 → exhausted", nil, harness.ReasonTimeout, agent.WriteCounts{}, "", "exhausted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := settleIntentState(tc.err, tc.reason, tc.wrote, tc.outcome); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
