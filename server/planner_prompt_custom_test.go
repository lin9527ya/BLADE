package server

import (
	"os"
	"testing"

	"github.com/lin9527ya/blade/agent"
)

// 包含旧默认词语不能证明“正文属于平台”，精确内置正文之外都应当保留。
func TestPlannerPromptIsCustom(t *testing.T) {
	for _, cur := range []string{"…深度优先于覆盖度…", "…广度保底与深度并行…", "…本方向由哪些上游节点综合得出（可选，0/1/多个）…", "…【槽位利用率（代码持有）】…", "用户自定义规划要求"} {
		if !plannerPromptIsCustom(cur) {
			t.Errorf("custom text matched as builtin: %q", cur)
		}
	}
	old, err := os.ReadFile("../docs/prompts/planner/default.body.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, cur := range []string{string(old), agent.PlannerPromptBody(), "   "} {
		if plannerPromptIsCustom(cur) {
			t.Error("exact builtin or empty prompt classified as custom")
		}
	}
}
