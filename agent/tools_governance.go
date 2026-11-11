package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"

	"github.com/lin9527ya/blade/db"
)

// 规划层的两个「收口」工具：
//
//   - abandon_goal：把一个不可达/不再适用的目标标记为 abandoned（理由必填）。
//
// 为什么归 planner 而不是 worker：worker 只对自己的意图负责；"整个策略是否已经
// 穷尽"和"这个目标还要不要追"是规划层的判断。此前两个出口都不存在——硬底线要求
// planner 写结论 fact，但它没有通用 record_fact（只有 record_probe_fact 写开局侦察观察），于是只能无限派边缘意图。

func (t *ToolSet) abandonGoal() actool.CoreTool {
	return t.writeExpTool("abandon_goal",
		"把一个**不可达或不再适用**的目标标记为「已放弃」（理由必填）。放弃后该目标不再阻断任务收官；它不是「已达成」，报告会如实区分。\n"+
			"只在有证据/明确业务原因时使用：客户取消该目标、目标已下线、授权范围不再覆盖。",
		obj(map[string]any{
			"goal_id": idp("要放弃的目标 id（graph_overview 的 goals）"),
			"reason":  str("【必填】为什么放弃：不可达/已下线/客户取消/超出授权范围，写清依据"),
		}, "goal_id", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				GoalID json.RawMessage `json:"goal_id"`
				Reason string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.GoalID)
			if id <= 0 {
				return actool.Errorf("goal_id 必填"), nil
			}
			reason := strings.TrimSpace(a.Reason)
			if reason == "" {
				return actool.Errorf("reason 必填：放弃一个目标必须交代为什么它不成立/不可达"), nil
			}
			n, err := t.ts.GetNode(id)
			if err != nil || n == nil || n.Kind != db.KindGoal {
				return actool.Errorf("goal_id 必须是本任务的目标节点"), nil
			}
			if n.State != "open" {
				return actool.Errorf(fmt.Sprintf("目标 %d 当前状态是 %q：只有未达成(open)的目标可以放弃", id, n.State)), nil
			}
			changed, err := t.ts.AbandonGoal(id, reason)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if changed == 0 {
				return actool.Errorf(fmt.Sprintf("目标 %d 已不是 open 状态，未放弃", id)), nil
			}
			t.auditLootExec("abandon_goal", fmt.Sprintf("放弃目标 #%d", id), "理由："+reason)
			if t.notify != nil {
				t.notify()
			}
			return actool.Text(fmt.Sprintf("goal abandoned: %d（%s）", id, reason)), nil
		})
}

func (t *ToolSet) mergeFindings() actool.CoreTool {
	return t.writeExpTool("merge_findings",
		"把重复登记的漏洞合并到一条保留项：重复项的处置状态置为 duplicate（墓碑保留，报告会列在「已关闭/重复」）、资产锚点并入保留项。\n"+
			"只在确认是同一条漏洞时使用；理由必填。合并后 findings_total / list_findings 只保留有效项，不再重复计数。",
		obj(map[string]any{
			"canonical_id": idp("保留项的漏洞节点 id（list_findings / graph_overview 里的 finding id）"),
			"duplicate_ids": map[string]any{
				"type": "array", "items": map[string]any{"type": "integer"},
				"description": "【必填】要并入保留项的重复漏洞节点 id 列表",
			},
			"reason": str("【必填】为什么这些是同一条漏洞（同资产/同入口/同根因），写清依据"),
		}, "canonical_id", "duplicate_ids", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				CanonicalID  json.RawMessage   `json:"canonical_id"`
				DuplicateIDs []json.RawMessage `json:"duplicate_ids"`
				Reason       string            `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			canonical := pid(a.CanonicalID)
			if canonical <= 0 {
				return actool.Errorf("canonical_id 必填：给保留项的漏洞节点 id"), nil
			}
			dups := pidList(a.DuplicateIDs)
			if len(dups) == 0 {
				return actool.Errorf("duplicate_ids 必填：至少给一条要合并的重复漏洞 id"), nil
			}
			reason := strings.TrimSpace(a.Reason)
			if reason == "" {
				return actool.Errorf("reason 必填：说明为什么这些是同一条漏洞（同资产/同入口/同根因）"), nil
			}
			// 两个 id 都必须是本任务的 finding 节点。
			for _, id := range append([]int64{canonical}, dups...) {
				n, err := t.ts.GetNode(id)
				if err != nil || n == nil || n.Kind != db.KindFinding {
					return actool.Errorf(fmt.Sprintf("%d 不是本任务的漏洞节点", id)), nil
				}
			}
			merged, err := t.ts.MergeFindings(canonical, dups, reason)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			t.auditLootExec("merge_findings", fmt.Sprintf("合并漏洞 #%d", canonical), "重复项："+fmt.Sprint(dups)+"\n理由："+reason)
			if t.notify != nil {
				t.notify()
			}
			return actool.Text(fmt.Sprintf("findings merged: %d 条并入 #%d（重复项已标记 duplicate，资产锚点已合并）", merged, canonical)), nil
		})
}
