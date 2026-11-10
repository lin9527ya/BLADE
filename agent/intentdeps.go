package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// dropIntent 丢弃一条**还没被领走**的排队意图（软删除，保留节点与血缘）。
//
// 它是队列背压的配套出口：上限顶住之后，planner 仍需要能清掉"已被新事实覆盖/方向已过时"
// 的排队项，否则队列会被旧方向占满。删除会**一并解锁依赖它的下游**——否则下游永远等不到
// "前置有结论"，在 frontier 外静默卡死（UI 取消路径原先就有这个坑）。
func (t *ToolSet) dropIntent() actool.CoreTool {
	return t.writeExpTool("drop_intent",
		"丢弃一条**还没被领走**的排队意图（软删除：保留节点与全部产出/血缘，理由进时间线）。"+
			"\n★ 用途：队列已满或方向已被新事实覆盖时腾位置。**先看再删**——确已无价值才删，不要为\"整齐\"删。"+
			"\n★ 只能删 open / paused；正在跑的意图先用 kill_work 停掉。"+
			"\n★ 依赖它的下游会被一并解锁并在这里列出——那些步骤需要你重新规划（补前置或改方向）。",
		obj(map[string]any{
			"intent_id": idp("要丢弃的排队意图 id（graph_overview 的 open_intents）"),
			"reason":    str("为什么丢弃（必填，进 delete_reason 与时间线）"),
		}, "intent_id", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("drop_intent 需要任务上下文"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Reason   string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("drop_intent: intent_id 必填"), nil
			}
			reason := strings.TrimSpace(a.Reason)
			if reason == "" {
				return actool.Errorf("drop_intent: reason 必填——删掉一条方向是复盘时要交代的决定"), nil
			}
			n, err := t.ts.GetNode(id)
			if err != nil || n == nil || n.Kind != db.KindIntent {
				return actool.Errorf(fmt.Sprintf("drop_intent: %d 不是本任务的意图", id)), nil
			}
			if isCleanupIntentPayload(n) {
				return actool.Errorf(fmt.Sprintf("drop_intent: 意图 %d 是操作者提交的痕迹清理意图，不能由规划者丢弃；请在任务详情「痕迹」页签人工处理（标记已清理/接受风险）", id)), nil
			}
			if n.State == "running" {
				return actool.Errorf(fmt.Sprintf("drop_intent: 意图 %d 正在跑——先 kill_work 停掉再删", id)), nil
			}
			if n.State != "open" && n.State != "paused" {
				return actool.Errorf(fmt.Sprintf("drop_intent: 意图 %d 当前状态是 %q，只有排队中(open)/已暂停(paused)的可以丢", id, n.State)), nil
			}
			var rp struct {
				ResumeAfterRestart bool `json:"resume_after_restart"`
			}
			_ = json.Unmarshal(n.Payload, &rp)
			if rp.ResumeAfterRestart {
				return actool.Errorf(fmt.Sprintf("drop_intent: 意图 %d 是重启中断后等待续跑的意图，不能直接丢弃；先让 worker 按原 transcript 续跑，确需放弃请等它领取后再处理", id)), nil
			}
			summary, dependents, derr := t.ts.SoftDeleteIntent(id, reason)
			if derr != nil {
				return actool.Errorf("drop_intent: " + derr.Error()), nil
			}
			t.auditLootExec("drop_intent",
				fmt.Sprintf("丢弃排队意图 #%d「%s」", id, clipLine(summary, 40)), "理由："+reason)
			msg := fmt.Sprintf("dropped: intent %d（%s）", id, summary)
			if len(dependents) > 0 {
				ids := make([]string, 0, len(dependents))
				for _, d := range dependents {
					ids = append(ids, fmt.Sprintf("#%d", d))
				}
				msg += fmt.Sprintf("\n已解锁下游 %s：它们不再等本条，请重新规划（补前置或改方向）。", strings.Join(ids, "、"))
			}
			return actool.Text(msg), nil
		})
}
