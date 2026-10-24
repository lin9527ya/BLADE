package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// unblockIntent 拆掉一条意图的全部前置依赖。
//
// 串行链的前置一旦失败（blocked/exhausted/no_output），下游会一直停在 frontier 外——
// 这是刻意的（下游拿不到前置只会空转），但 planner 需要一个明确的出口：要么重派前置，
// 要么判定"不等它了、我现在自己接管这一步"。后者就是本工具。
func (t *ToolSet) unblockIntent() actool.CoreTool {
	return t.writeExpTool("unblock_intent",
		"拆掉一条意图的**全部前置依赖**，让它马上可以被派发。\n"+
			"用于：前置判定跑不通、而你决定不等它的情形。拆掉之后这条意图会以前置失败为前提开跑——"+
			"确保它自己能拿到需要的输入（比如把前置该产出的结论先用 record_fact 写下来）。",
		obj(map[string]any{
			"intent_id": idp("要拆链的意图 id（graph_overview 里带 blocked_by 的那条）"),
			"reason":    str("为什么不等前置了（必填，进时间线）"),
		}, "intent_id", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("unblock_intent 需要任务上下文"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Reason   string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("unblock_intent: intent_id 必填"), nil
			}
			reason := strings.TrimSpace(a.Reason)
			if reason == "" {
				return actool.Errorf("unblock_intent: reason 必填——拆链是一个要进复盘的决定，不能没有理由"), nil
			}
			n, err := t.ts.GetNode(id)
			if err != nil || n == nil || n.Kind != db.KindIntent {
				return actool.Errorf(fmt.Sprintf("unblock_intent: %d 不是本任务的意图", id)), nil
			}
			if n.State != "open" {
				return actool.Errorf(fmt.Sprintf("unblock_intent: 意图 %d 当前状态是 %q——只有还没被领走（open）的意图需要拆链", id, n.State)), nil
			}
			removed, err := t.ts.ClearIntentDependencies(id)
			if err != nil {
				return actool.Errorf("unblock_intent: " + err.Error()), nil
			}
			t.auditLootExec("unblock_intent",
				fmt.Sprintf("拆链：意图 #%d 不再等前置", id), "理由："+reason)
			if removed == 0 {
				return actool.Text(fmt.Sprintf("intent %d 本来就没有前置依赖（可能已被别的路径拆过）。", id)), nil
			}
			return actool.Text(fmt.Sprintf("unblocked: intent %d（拆掉 %d 条前置），下一轮即可被派发。", id, removed)), nil
		})
}
