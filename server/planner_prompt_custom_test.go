package server

import "testing"

// 这组用例钉住一次真实的迁移事故：判断"正文是否被用户改过"时把 && 与 || 平铺混写，
// 优先级让一份刚刷成新默认的正文被判成自定义，此后一次性迁移再也刷不动（planner 永远
// 停在旧口径）。判据必须是"含任一出厂特征句 → 不是自定义"。
func TestPlannerPromptIsCustom(t *testing.T) {
	// 出厂旧正文：含旧特征句 → 允许刷新。
	for _, cur := range []string{
		"…深度优先于覆盖度…",
		"…广度保底与深度并行…",
		"…本方向由哪些上游节点综合得出（可选，0/1/多个）…",
		"…【槽位利用率（代码持有）】…",
	} {
		if plannerPromptIsCustom(cur) {
			t.Errorf("出厂正文被判成自定义，迁移将永远刷不动：%q", cur)
		}
	}
	// 用户真改过的正文 → 保留。
	if !plannerPromptIsCustom("你是一个自定义规划者，只派我自己写的方向。") {
		t.Error("用户自定义正文应被保留")
	}
	// 空正文不算自定义（首插入路径会 seed 最新默认）。
	if plannerPromptIsCustom("   ") {
		t.Error("空正文不该算自定义")
	}
}
