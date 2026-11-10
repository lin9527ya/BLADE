package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/policy"
)

// 热会话复用（agentcore.Session 的工具注册表在 NewSession 时固定）让第 2 轮起继续调用
// 第 1 轮的 ToolSet 闭包。轮级状态必须每轮显式重置，否则：
//   - intentsRound 停在上一轮 → pace:round 把本轮每条意图都拒掉；
//   - GoalMet 残留 → 上一轮判定达成后本轮再也派不出意图。
//
// 任务 18 正是这样连续 3 个空轮，被停滞保护判成 policy_blocked。
func TestBeginRoundResetsRoundScopedState(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	eff := &policy.Effective{Version: 1, MaxIntentsPerRound: 1}
	tools.BeginRound(eff, 0, TaskClock{}, 0)
	_ = fmt.Sprintf

	add := func(summary string) string {
		return callTool(t, tools.PlannerTools(), "add_intent",
			fmt.Sprintf(`{"summary":%q,"priority":9}`, summary))
	}
	// ARTEX parity: per-round intent caps are gone; both directions are created.
	if out := add("解析登录接口返回结构"); !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("本轮首条意图应放行：%s", out)
	}
	if out := add("枚举静态资源目录"); !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("ARTEX parity 不应有每轮意图上限：%s", out)
	}

	// GoalMet 同理：上一轮判定全部达成，不该压住下一轮的新方向。
	tools.GoalMet = true
	tools.BeginRound(eff, 0, TaskClock{}, 0)
	if tools.GoalMet {
		t.Fatal("BeginRound 应清掉上一轮留下的 GoalMet")
	}
	if tools.writes.Total() != 0 {
		t.Fatalf("BeginRound 应清空轮级写回统计，当前 %+v", tools.writes)
	}
}
