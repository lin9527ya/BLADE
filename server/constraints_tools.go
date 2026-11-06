package server

import (
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// dropPlannerConstraintTools removes tools whose group is off. planner 的 base 工具面
// 已经不带这些工具，但 DB tools 表可能仍把旧绑定重新注入（resolveTools）；这里兜底。
func dropPlannerConstraintTools(pg *db.DB, out []actool.CoreTool) []actool.CoreTool {
	if !constraintGroupEnabled(pg, "opening") {
		out = dropToolByName(out, "record_probe_fact")
	}
	if !constraintGroupEnabled(pg, "dispatch") {
		// dispatch 关闭只影响硬门，不单独删工具；留空便于以后扩展。
	}
	if !constraintGroupEnabled(pg, "ledger") {
		out = dropToolByName(out, "exploration_ledger", "chain_detail", "dismiss_coverage", "coverage_matrix")
	}
	if !constraintGroupEnabled(pg, "completion") {
		out = dropToolByName(out, "accept_completion_risk")
	}
	if !constraintGroupEnabled(pg, "exhaustion") {
		out = dropToolByName(out, "mark_strategy_exhausted")
	}
	if !constraintGroupEnabled(pg, "depth") {
		out = dropToolByName(out, "dismiss_leads")
	}
	return out
}
