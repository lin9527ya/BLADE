package agent

// ConstraintEnabled 是宿主注入的探索约束分组 resolver（见 server/manager.go 的
// ConstraintGroups）。agent 侧每轮现读；missing/nil 一律按 classic（全关）处理，
// 避免测试或旧宿主忘记接线时静默开启增强层。
var ConstraintEnabled func(group string) bool

func constraintOn(group string) bool {
	// ARTEX parity: exploration constraint groups are gone. Keeping this resolver
	// false makes every legacy group inert even for databases that still carry an
	// "enhanced" profile value from before the switch.
	return false
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
func constraintOverrideBlock() string { return "" }
