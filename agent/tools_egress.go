package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
)

// rotate_egress 是「确认出口 IP 被 WAF 封禁」时的受控出口切换：调 Clash 外部控制器，
// 在同一 Selector 组里换一个非 DIRECT 节点，并验证新出口 IP 确实变化。
// 它不用于绕过授权边界，也不会退回直连（服务端在无法验证新出口时拒绝切换）。
func (t *ToolSet) rotateEgressTool() actool.CoreTool {
	return writeTool("rotate_egress",
		"当确认**当前出口 IP 被目标 WAF 封禁**时调用：切换 Clash 出口节点并验证新出口 IP。\n"+
			"只在有 WAF 证据时用（连续 403/429/挑战页/厂商拦截页）；普通鉴权失败、目标故障、参数错误都不算。\n"+
			"切换只会在 Clash 的 Selector 组内选非 DIRECT 节点，服务端验证出口 IP 变化；不配置/失败时不会直连。\n"+
			"切完先做一次最小请求复测；仍被拦就停这条方向换入口，不要重放同一 payload。",
		obj(map[string]any{
			"reason": str("【必填】为什么判定是出口 IP 被封：目标、现象（403/429/挑战页/厂商指纹）、已排除目标自身故障"),
		}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			reason := strings.TrimSpace(a.Reason)
			if reason == "" {
				return actool.Errorf("reason 必填：写明目标、拦截现象、为什么判断是出口 IP 被封"), nil
			}
			if t.rotateEgressFn == nil {
				return actool.Errorf("出口切换未配置：请先在系统设置 → 网络与代理 → Clash 出口切换里填写控制器地址与代理组"), nil
			}
			msg, err := t.rotateEgressFn(reason)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("出口切换失败：%v", err)), nil
			}
			return actool.Text("egress rotated: " + msg), nil
		})
}
