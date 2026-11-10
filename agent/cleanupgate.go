package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// ArtifactRiskAcceptedKey 是"接受遗留物风险"这条结论在 fact payload 里的标记。
const ArtifactRiskAcceptedKey = "artifact_risk_accepted"

// acceptArtifactRisk 显式接受"带着遗留物交付"的风险：写一条可核对、出现在图里的结论。
//
// 它不是橡皮图章：理由必填，且这条 fact 会进报告语境——客户复盘时能看到"我们知道自己
// 留了什么、为什么没清"。
func (t *ToolSet) acceptArtifactRisk() actool.CoreTool {
	return t.writeExpTool("accept_artifact_risk",
		"显式接受「带着遗留物交付」的风险（仅在确实清不掉、或客户要求保留时用）：写一条结论，任务随后可以正常收官。\n"+
			"**优先清理**：能清就用 mark_artifact_cleared 销账——没清掉的 webshell 是留在客户系统上的后门。",
		obj(map[string]any{
			"reason": str("为什么不能清理（必填）：客户要求保留 / 已失去访问 / 清理会造成业务中断 …"),
		}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			if t.ts == nil {
				return actool.Errorf("accept_artifact_risk 需要任务上下文"), nil
			}
			reason := strings.TrimSpace(a.Reason)
			if reason == "" {
				return actool.Errorf("reason 必填：接受遗留物风险是一条要进报告的结论，不能没有理由"), nil
			}
			id, err := t.ts.AddNode(db.KindFact, map[string]any{
				"summary":               "接受遗留物风险：" + reason,
				ArtifactRiskAcceptedKey: true,
				"confidence":            "observed",
			}, 0, "confirmed", "planner", nil)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			t.writes.Facts++
			return actool.Text(fmt.Sprintf("risk accepted: %d\n任务可以正常收官；这条结论会进报告语境。", id)), nil
		})
}
