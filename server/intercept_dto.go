package server

import (
	"encoding/json"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/intercept"
)

// interceptPendingDTO adds display-only metadata to a pending approval: a
// deterministic risk score and, for a simple Bash command, the prefix an
// "always allow" rule would use. Neither changes the decision.
type interceptPendingDTO struct {
	db.InterceptPending
	Risk              intercept.RiskReport `json:"risk"`
	AlwaysAllowPrefix string               `json:"always_allow_prefix,omitempty"`
}

func pendingApprovalDTO(p *db.InterceptPending) interceptPendingDTO {
	d := interceptPendingDTO{InterceptPending: *p, Risk: intercept.ScoreRisk(p.ToolName, p.ToolInput)}
	if p.Status == "pending" && p.ToolName == "Bash" {
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(p.ToolInput, &in) == nil {
			if prefix, err := intercept.AlwaysAllowPrefix(in.Command); err == nil {
				d.AlwaysAllowPrefix = prefix
			}
		}
	}
	return d
}
