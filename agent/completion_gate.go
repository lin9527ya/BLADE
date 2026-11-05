package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
)

// acceptCompletionRisk 是收官审计的显式豁免入口：可以放行，但必须说得出理由。
// 豁免写进 completion_waivers（与 dismiss_coverage 同一张表），报告的数据缺项会展示 reason。
func (t *ToolSet) acceptCompletionRisk() actool.CoreTool {
	return t.writeExpTool("accept_completion_risk",
		"接受收官审计里的某类未完成项，让 prove_goal 可以收官。这不是\"跳过检查\"，而是**显式接受风险**："+
			"理由会进报告的数据缺项，客户/复核者看得到你在哪里放的水。\n"+
			"scope：coverage（覆盖/深度缺口，整类放行；要精确到格请用 dismiss_coverage）|"+
			" chains（未推进的据点/凭据）| proof_gaps（open_proof_gap 候选）| leads（未处理线索）。\n"+
			"能用测试和清理解决的就别用它——它是逃生阀，不是常规收尾手段。",
		obj(map[string]any{
			"scope":  str("coverage | chains | proof_gaps | leads"),
			"reason": str("【必填】≥10 字：接受的是什么风险、为什么不处理、对交付的影响"),
		}, "scope", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil || t.taskID <= 0 {
				return actool.Errorf("accept_completion_risk 需要任务上下文"), nil
			}
			var a struct {
				Scope  string `json:"scope"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			scope := strings.ToLower(strings.TrimSpace(a.Scope))
			switch scope {
			case "coverage", "chains", "proof_gaps", "leads":
			default:
				return actool.Errorf("scope 只能是 coverage / chains / proof_gaps / leads"), nil
			}
			reason := strings.TrimSpace(a.Reason)
			if len([]rune(reason)) < 10 {
				return actool.Errorf("reason 至少 10 个字：写清「接受的是什么风险、为什么、对交付的影响」"), nil
			}
			id, err := t.ts.AddCoverageWaiver(scope, nil, "", reason)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// 缓存失效：同一轮里后续读到的台账/通路要反映“已接受”。
			t.coverageLoaded, t.ledgerLoaded, t.chainsLoaded = false, false, false
			return actool.Text("completion waiver recorded: " + strconv.FormatInt(id, 10) +
				"\nscope=" + scope + "；报告的数据缺项会列出这条理由。"), nil
		})
}
