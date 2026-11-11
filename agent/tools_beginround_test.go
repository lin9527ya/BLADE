package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/policy"
)

// 热会话复用（agentcore.Session 的工具注册表在 NewSession 时固定）让第 2 轮起继续调用
// 第 1 轮的 ToolSet 闭包。轮级状态必须每轮显式重置，否则 GoalMet/写回统计会跨轮残留，
// 上一轮判定达成后本轮就再也派不出意图。
func TestBeginRoundResetsRoundScopedState(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	tools.GoalMet = true
	tools.writes.Facts = 3
	tools.BeginRound(&policy.Effective{}, TaskClock{}, 0)
	if tools.GoalMet {
		t.Fatal("BeginRound 应清掉上一轮留下的 GoalMet")
	}
	if tools.writes.Total() != 0 {
		t.Fatalf("BeginRound 应清空轮级写回统计，当前 %+v", tools.writes)
	}
}

// planner/worker 的策略段只保留内容边界；ARTEX 探索路径没有每轮意图额度。
func TestPolicyBlockRendersContentBoundaryOnly(t *testing.T) {
	eff := &policy.Effective{Techniques: []string{"bruteforce"}}
	got := policyBlock(eff)
	if !strings.Contains(got, "禁用手段") {
		t.Fatalf("策略块缺内容边界：%s", got)
	}
	if strings.Contains(got, "每轮最多") {
		t.Fatalf("策略块不应再出现每轮额度：%s", got)
	}
}

// 内容策略仍在意图写入前硬拦；被拒方向不落图，回执要说明原因。
func TestAddIntentHonorsContentPolicy(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	tools.BeginRound(&policy.Effective{Techniques: []string{"bruteforce"}}, TaskClock{}, 0)
	add := func(summary string) string {
		return callTool(t, tools.PlannerTools(), "add_intent",
			fmt.Sprintf(`{"summary":%q,"priority":9}`, summary))
	}
	if out := add("对登录口做口令爆破，尝试常见弱口令"); strings.HasPrefix(out, "intent created: ") ||
		!strings.Contains(out, "未写入探索图") {
		t.Fatalf("命中禁用手段的意图应被拒：%s", out)
	}
	if out := add("解析登录接口返回结构"); !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("未命中策略的方向应放行：%s", out)
	}
}
