package agent

import (
	"testing"
)

// 热会话复用（agentcore.Session 的工具注册表在 NewSession 时固定）让第 2 轮起继续调用
// 第 1 轮的 ToolSet 闭包。轮级状态必须每轮显式重置，否则 GoalMet/写回统计会跨轮残留，
// 上一轮判定达成后本轮就再也派不出意图。
func TestBeginRoundResetsRoundScopedState(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	tools.GoalMet = true
	tools.writes.Facts = 3
	tools.BeginRound(TaskClock{}, 0)
	if tools.GoalMet {
		t.Fatal("BeginRound 应清掉上一轮留下的 GoalMet")
	}
	if tools.writes.Total() != 0 {
		t.Fatalf("BeginRound 应清空轮级写回统计，当前 %+v", tools.writes)
	}
}
