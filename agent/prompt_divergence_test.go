package agent

import (
	"strings"
	"testing"
)

// TestPlannerPromptDivergenceContract 钉住 planner 提示词的四条语义，防止将来
// 为了「更克制」把它们改回去——黑板书架构的核心就是「拿到目标前可以持续发散」：
//
//  1. 「不重复」只指不重复已有意图，不是「能少派就少派」；
//  2. 目标未达成时，发散优先于收敛；
//  3. 判定死路必须有证据——「试过一次没成」不算；
//  4. 授权边界（操作约束）优先于多样性，且必须排在发散段**之后**，
//     否则模型可能把「发散」读成「可以越界」。
func TestPlannerPromptDivergenceContract(t *testing.T) {
	p := renderSystem("planner", plannerDefaultTmpl, PlannerVars{
		Goal: "g", Scope: "s", AssetSummary: "", DataDir: "/d", Now: "t",
	})

	for _, want := range []struct{ needle, why string }{
		{`不是"能少派就少派"`, "「不重复」必须被限定为不重复，而不是少派"},
		{"发散优先于收敛", "目标未达成时发散优先"},
		{"都**不算**死路", "「试过一次没成」不能当成死路"},
		{"所有已知方向都已被证明为死路", "收敛必须有证据门槛"},
		{"多样性永远服从顶部【操作约束】", "授权边界是硬约束，不能被发散冲掉"},
	} {
		if !strings.Contains(p, want.needle) {
			t.Fatalf("提示词缺少关键语义 %q（%s）", want.needle, want.why)
		}
	}

	div := strings.Index(p, "发散优先于收敛")
	con := strings.Index(p, "多样性永远服从顶部【操作约束】")
	if div < 0 || con < 0 || con < div {
		t.Fatalf("操作约束应排在发散段之后（divergence@%d constraint@%d）", div, con)
	}
}
