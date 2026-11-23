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
			"\n★ 防误删四维等价红线：仅当「目标对象/接口 + 载体/HTTP方法 + 身份/前置状态 + 验证机理/假设」四维全同或严格包含时才算重复；不同载体（Query/Body/Header/Cookie）、不同权限态或不同绕过机理拿不准先保留并用 priority 排序。合并类似方向须先把独特测试点补进保留意图再删，reason 写清「与 #X 四维等价」或「独特测试点已合入 #X」。"+
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

// setDependencyMode changes whether one queued intent's depends_on is advisory
// (ordering only) or strict (wait for a live prerequisite). It is an explicit
// recovery capability, not a new default gate.
func (t *ToolSet) setDependencyMode() actool.CoreTool {
	return t.writeExpTool("set_dependency_mode",
		"修改一条排队意图的依赖模式：advisory（默认，只排序不阻塞）或 strict（前置仍 open/running/paused 时暂缓派发）。只能改 open/paused 意图；必须写 reason。",
		obj(map[string]any{
			"intent_id": idp("要修改的意图 id（open 或 paused）"),
			"mode":      str("advisory | strict"),
			"reason":    str("为什么改（必填，进时间线）"),
		}, "intent_id", "mode", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_dependency_mode 需要任务上下文"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Mode     string          `json:"mode"`
				Reason   string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			mode := strings.ToLower(strings.TrimSpace(a.Mode))
			reason := strings.TrimSpace(a.Reason)
			if id <= 0 || (mode != db.DependencyModeAdvisory && mode != db.DependencyModeStrict) || reason == "" {
				return actool.Errorf("intent_id、mode=advisory|strict、reason 均必填"), nil
			}
			n, err := t.ts.GetNode(id)
			if err != nil || n == nil || n.Kind != db.KindIntent {
				return actool.Errorf(fmt.Sprintf("intent %d 不是本任务的意图", id)), nil
			}
			if isCleanupIntentPayload(n) {
				return actool.Errorf("痕迹清理意图不能由 planner 修改依赖模式"), nil
			}
			if n.State != "open" && n.State != "paused" {
				return actool.Errorf(fmt.Sprintf("只能修改 open/paused 意图，当前是 %s", n.State)), nil
			}
			changed, err := t.ts.SetIntentDependencyMode(id, mode, reason)
			if err != nil {
				return actool.Errorf("修改依赖模式失败：" + err.Error()), nil
			}
			if !changed {
				return actool.Errorf("意图状态已变化，未修改"), nil
			}
			t.auditLootExec("set_dependency_mode", fmt.Sprintf("意图 #%d 依赖模式改为 %s", id, mode), "理由："+reason)
			if t.notify != nil {
				t.notify()
			}
			return actool.Text(fmt.Sprintf("intent %d dependency_mode=%s", id, mode)), nil
		})
}

// dropDependency removes one dependency edge without deleting the intent. It is
// the precise alternative to dropping the whole downstream direction when a
// prerequisite is permanently unavailable but the rest of the hypothesis remains
// useful.
func (t *ToolSet) dropDependency() actool.CoreTool {
	return t.writeExpTool("drop_dependency",
		"拆除一条依赖边，不删除意图：用于 strict 前置失败/不再需要，但下游仍值得执行的情况。会同步移除 payload.depends_on 里的 id；只能改 open/paused 源意图；必须写 reason。",
		obj(map[string]any{
			"intent_id":       idp("源意图 id（open 或 paused）"),
			"prerequisite_id": idp("要移除的前置意图 id"),
			"reason":          str("为什么拆（必填，进时间线）"),
		}, "intent_id", "prerequisite_id", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("drop_dependency 需要任务上下文"), nil
			}
			var a struct {
				IntentID     json.RawMessage `json:"intent_id"`
				Prerequisite json.RawMessage `json:"prerequisite_id"`
				Reason       string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			id, pre := pid(a.IntentID), pid(a.Prerequisite)
			reason := strings.TrimSpace(a.Reason)
			if id <= 0 || pre <= 0 || reason == "" {
				return actool.Errorf("intent_id、prerequisite_id、reason 均必填"), nil
			}
			n, err := t.ts.GetNode(id)
			if err != nil || n == nil || n.Kind != db.KindIntent {
				return actool.Errorf(fmt.Sprintf("intent %d 不是本任务的意图", id)), nil
			}
			if isCleanupIntentPayload(n) {
				return actool.Errorf("痕迹清理意图不能由 planner 修改依赖"), nil
			}
			if n.State != "open" && n.State != "paused" {
				return actool.Errorf(fmt.Sprintf("只能修改 open/paused 意图，当前是 %s", n.State)), nil
			}
			changed, err := t.ts.DropIntentDependency(id, pre, reason)
			if err != nil {
				return actool.Errorf("拆依赖失败：" + err.Error()), nil
			}
			if !changed {
				return actool.Errorf(fmt.Sprintf("意图 #%d 没有指向 #%d 的 depends_on 边", id, pre)), nil
			}
			t.auditLootExec("drop_dependency", fmt.Sprintf("意图 #%d 拆除前置 #%d", id, pre), "理由："+reason)
			if t.notify != nil {
				t.notify()
			}
			return actool.Text(fmt.Sprintf("dropped dependency: intent %d -> prerequisite %d", id, pre)), nil
		})
}

// reopenIntent re-queues a failed/blocked local intent. This is deliberately
// narrower than a generic "unblock": it only resumes intents whose state records
// a failed or incomplete run, and leaves done/negative/out-of-scope untouched.
func (t *ToolSet) reopenIntent() actool.CoreTool {
	return t.writeExpTool("reopen_intent",
		"把本任务里 blocked/exhausted/stopped/no_output 的意图重新置为 open，让 worker 从原 transcript 续跑。不能重开 done/negative/out-of-scope/running，也不能动痕迹清理意图；必须写 reason。",
		obj(map[string]any{
			"intent_id": idp("要重开的意图 id"),
			"reason":    str("为什么重开（必填，进时间线）"),
		}, "intent_id", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("reopen_intent 需要任务上下文"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Reason   string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			reason := strings.TrimSpace(a.Reason)
			if id <= 0 || reason == "" {
				return actool.Errorf("intent_id 和 reason 必填"), nil
			}
			n, err := t.ts.GetNode(id)
			if err != nil || n == nil || n.Kind != db.KindIntent {
				return actool.Errorf(fmt.Sprintf("intent %d 不是本任务的意图", id)), nil
			}
			if isCleanupIntentPayload(n) {
				return actool.Errorf("痕迹清理意图不能由 planner 重开"), nil
			}
			switch n.State {
			case "blocked", "exhausted", "stopped", "no_output":
			default:
				return actool.Errorf(fmt.Sprintf("只能重开 blocked/exhausted/stopped/no_output，当前是 %s", n.State)), nil
			}
			changed, err := t.ts.ReopenIntent(id)
			if err != nil {
				return actool.Errorf("重开意图失败：" + err.Error()), nil
			}
			if !changed {
				return actool.Errorf("意图状态已变化，未重开"), nil
			}
			t.auditLootExec("reopen_intent", fmt.Sprintf("重开意图 #%d", id), "理由："+reason)
			if t.notify != nil {
				t.notify()
			}
			return actool.Text(fmt.Sprintf("intent %d reopened", id)), nil
		})
}
