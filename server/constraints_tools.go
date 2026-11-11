package server

import (
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// dropRetiredConstraintTools removes the retired BLADE exploration-constraint
// tools unconditionally from every agent. The DB tools table may still carry legacy bindings for
// databases upgraded from pre-ARTEX versions; profile/group switches must never
// be able to bring them back into the planner. BLADE-only capabilities are not
// in this list and remain available.
func dropRetiredConstraintTools(_ *db.DB, out []actool.CoreTool) []actool.CoreTool {
	return dropToolByName(out,
		"record_probe_fact", "exploration_ledger", "chain_detail", "dismiss_coverage",
		"coverage_matrix", "accept_completion_risk", "dismiss_leads",
	)
}
