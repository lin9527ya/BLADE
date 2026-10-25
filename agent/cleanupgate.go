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

// cleanupGate 在任务即将收官时检查「我们留在目标上的东西」有没有销账。
//
// 遗留物台账此前只是**建议**：落了 webshell 记一条，不记也没人拦。可没清掉的 webshell
// 就是留在客户系统上的后门——这正是收官时最不该静默放过的一件事。返回 "" 表示放行，
// 否则返回一段给模型看的说明（列出物件 + 两条出路）。
//
// 读不出来时不阻断收尾：与其它门同一条纪律——门坏了不能把人锁在里面，但要留日志。
func (t *ToolSet) cleanupGate() string {
	if t.ts == nil {
		return "" // 无任务上下文（域工具被绑到非任务 agent）：没有台账可查，不阻断
	}
	arts, err := t.ts.ListByKind(db.KindArtifact, 200)
	if err != nil {
		return ""
	}
	var active []*db.Node
	for _, n := range arts {
		if n.State == db.StateArtifactActive {
			active = append(active, n)
		}
	}
	if len(active) == 0 || t.artifactRiskAccepted() {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "任务即将收官，但目标上还留着 %d 件**没销账的遗留物**：", len(active))
	for i, n := range active {
		if i >= 5 {
			fmt.Fprintf(&b, "\n  · …还有 %d 件（用 node_detail 逐条查完整信息）", len(active)-5)
			break
		}
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		fmt.Fprintf(&b, "\n  · #%d %s — %s（%s）", n.ID,
			payloadString(p, "kind"), payloadString(p, "locator"), payloadString(p, "host"))
	}
	b.WriteString("\n收尾前二选一：\n" +
		"  ① 清掉它们，然后用 mark_artifact_cleared 销账（推荐——没清掉的 webshell 就是留在客户系统上的后门）；\n" +
		"  ② 确认要带着它们交付（客户要求保留 / 已失去访问无法清理）：调用 accept_artifact_risk 写明理由。")
	return b.String()
}

// artifactRiskAccepted 报告本任务是否已经显式接受过遗留物风险。
func (t *ToolSet) artifactRiskAccepted() bool {
	facts, err := t.ts.ListByKind(db.KindFact, 500)
	if err != nil {
		return false
	}
	for _, n := range facts {
		var p map[string]any
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		if accepted, _ := p[ArtifactRiskAcceptedKey].(bool); accepted {
			return true
		}
	}
	return false
}

// cleanupGateForCompletion 只在"这次 prove_goal 会让所有目标都达成"时才拦——
// 中途证明某个目标不该被遗留物挡住。
func (t *ToolSet) cleanupGateForCompletion(goalID int64) string {
	if t.ts == nil {
		return ""
	}
	goals, err := t.ts.ListByKind(db.KindGoal, 1000)
	if err != nil || len(goals) == 0 {
		return ""
	}
	for _, g := range goals {
		if g.ID == goalID {
			continue
		}
		if g.State != "met" {
			return ""
		}
	}
	return t.cleanupGate()
}

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
