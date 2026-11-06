package agent

import "strings"

// ConstraintEnabled 是宿主注入的探索约束分组 resolver（见 server/manager.go 的
// ConstraintGroups）。agent 侧每轮现读；missing/nil 一律按 classic（全关）处理，
// 避免测试或旧宿主忘记接线时静默开启增强层。
var ConstraintEnabled func(group string) bool

func constraintOn(group string) bool {
	return ConstraintEnabled != nil && ConstraintEnabled(group)
}

// gated 返回 block 当且仅当该探索约束分组开启；用于代码持有的提示尾段。
func gated(group, block string) string {
	if constraintOn(group) {
		return block
	}
	return ""
}

// constraintOn 是 ToolSet 内的快捷方式；分组 resolver 由宿主通过 agent.ConstraintEnabled
// 注入。nil/未接线一律 classic（全关）。
func (t *ToolSet) constraintOn(group string) bool { return constraintOn(group) }

// constraintOverrideBlock 在任一分组关闭时追加到 planner/worker 系统提示末尾。
// 正文（DB/任务快照/模板）里仍可能写着“必须补满槽位/硬底线必须派/广度债/台账”等
// 增强措辞；本段显式压过它们，避免开关关了、模型仍按正文自我限流。
func constraintOverrideBlock() string {
	var off []string
	if !constraintOn("opening") {
		off = append(off, "开局摸底/粗粒度硬拒/record_probe_fact")
	}
	if !constraintOn("dispatch") {
		off = append(off, "槽位补满/空闲补派/空转暂停/队列背压/防打转/每轮意图上限")
	}
	if !constraintOn("breadth") {
		off = append(off, "广度债/横向推进/scope_surface")
	}
	if !constraintOn("ledger") {
		off = append(off, "探索台账/通路台账/coverage_matrix/chain_detail")
	}
	if !constraintOn("review_endgame") {
		off = append(off, "复盘轮/复盘 3 条/升级阶梯/收官期禁扩面")
	}
	if !constraintOn("completion") {
		off = append(off, "收官审计（prove_goal 缺口）")
	}
	if !constraintOn("exhaustion") {
		off = append(off, "穷尽复核/mark_strategy_exhausted")
	}
	if !constraintOn("supervisor") {
		off = append(off, "执行审核员 supervisor")
	}
	if !constraintOn("depth") {
		off = append(off, "否定结论变体门槛/线索写回/线索分诊")
	}
	if len(off) == 0 {
		return ""
	}
	return "\n\n【探索约束开关（代码持有；本段压过正文中的增强措辞）】\n" +
		"- 当前关闭：" + strings.Join(off, "、") + "。正文中与这些开关冲突的“必须/至少/每轮/不得 0 条/必须补到”等措辞在本轮不适用；按目标与已有事实自主决定本轮派 0 条或多条意图。\n" +
		"- 安全、授权范围、资产拦截、操作约束、出口代理、不可信数据、execution 终态门仍然有效。\n" +
		"- 不要调用未注册的工具（如 record_probe_fact / exploration_ledger / chain_detail / coverage_matrix / mark_strategy_exhausted / accept_completion_risk / dismiss_leads）。"
}
