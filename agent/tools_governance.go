package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"

	"github.com/lin9527ya/blade/db"
)

// 规划层的两个「收口」工具：
//
//   - mark_strategy_exhausted：planner 在授权范围/操作约束内确认已无可行方向时，
//     写一条结论事实并把任务交给人工（引擎在本轮结束后暂停任务）。
//   - abandon_goal：把一个不可达/不再适用的目标标记为 abandoned（理由必填）。
//
// 为什么归 planner 而不是 worker：worker 只对自己的意图负责；"整个策略是否已经
// 穷尽"和"这个目标还要不要追"是规划层的判断。此前两个出口都不存在——硬底线要求
// planner 写结论 fact，但它没有通用 record_fact（只有 record_probe_fact 写开局侦察观察），于是只能无限派边缘意图。

func (t *ToolSet) markStrategyExhausted() actool.CoreTool {
	return t.writeExpTool("mark_strategy_exhausted",
		"当你在【授权范围/操作约束内】已确认没有任何可行方向时调用：写一条结论事实，并把任务暂停等待人工决定。\n"+
			"它**不会**伪造目标达成，也不会让任务结束；人工确认后可继续，planner 会重新评估。\n"+
			"只在真正穷尽后使用——「暂时想不出」「在等某条 work 的产出」「还有空闲 worker」都不算。\n"+
			"调用前会被三道硬门检查：①同根子域还没发现过（资产图里只有目标主机本身）；"+
			"②还有同主体资产一条意图都没派过；③还有资产只终结了 1~2 类风险（深度不足）。"+
			"被拒时会点名具体资产与缺失的风险类——按它去派意图，不要改写措辞重试。",
		obj(map[string]any{
			"reason": str("【必填】为什么策略内已无可行方向：已穷尽哪些方向、各自被什么挡住（授权范围/操作约束/目标侧防御/事实否定）"),
			"evidence_ids": map[string]any{
				"type": "array", "items": map[string]any{"type": "integer"},
				"description": "【必填】支撑这个判断的节点 id（本任务的事实/漏洞/意图，至少 1 个）：没有依据的“穷尽”只是一句口号",
			},
		}, "reason", "evidence_ids"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Reason      string            `json:"reason"`
				EvidenceIDs []json.RawMessage `json:"evidence_ids"`
			}
			_ = json.Unmarshal(in, &a)
			reason := strings.TrimSpace(a.Reason)
			if reason == "" {
				return actool.Errorf("reason 必填：写清已穷尽哪些方向、各自被什么挡住"), nil
			}
			// 复核冷却：上一轮刚被复核否决时，先按复核点名的缺口补测，别再消耗一轮
			// （任务 28 在 25 分钟里连声明 5 次，每次都要走一遍硬门 + 复核）。
			if n := t.exhaustCooldown(); n > 0 {
				return actool.Errorf(fmt.Sprintf(
					"穷尽复核否决后的冷却期（还剩 %d 轮）：本轮先补复核点名的缺口——缺证据的方向派一条能产出具体观察的意图，"+
						"外部阻断（需登录/被 WAF 拦）如实标成 blocked 而不是 negative；冷却结束后再谈穷尽。", n)), nil
			}
			// 未闭合候选门：open_proof_gap 的字面意思就是「还缺证据才能下结论」。带着它们
			// 宣告穷尽，等于把「没测完」说成「没得测」。要求 reason 逐条点名（#id）：
			// 点名说明真的看过它，而不是漏在折叠区里。
			if open := t.openProofGaps(reason); len(open) > 0 {
				return actool.Errorf(fmt.Sprintf(
					"还有 %d 条候选停在 open_proof_gap（缺证据才能下结论）：\n%s\n"+
						"带着未闭合的候选宣告穷尽，等于把「没测完」说成「没得测」。先派意图补它们（或让 worker 用 close_candidate 以真实反证收场）；"+
						"确实无法闭合的（需要管理员会话/外部条件），在 reason 里逐条点名该 id 并写明阻断原因。",
					len(open), strings.Join(open, "\n"))), nil
			}
			ids := pidList(a.EvidenceIDs)
			if len(ids) == 0 {
				return actool.Errorf("evidence_ids 必填：至少给一个支撑判断的事实/漏洞/意图 id"), nil
			}
			// 范围内还有完全没试过的同主体资产时不许宣告穷尽：那是「正面受阻」，
			// 不是「无路可走」。这一条把「换个资产横向推进」从建议变成硬约束——
			// 任务 19 就是正面（Gambio 后台）打不动后直接想收工，而范围里还有
			// webmail/mail/smtp 等 6 个同根子域一条意图都没派过。
			if t.as != nil && t.taskID > 0 {
				if roots, err := t.as.ScopeRootDomains(t.taskID); err == nil && len(roots) > 0 {
					// 同根子域还没发现过（资产图里只有目标自己）→ 也先别穷尽：
					// "没找到别的资产"不等于"没有别的资产"。
					if known, kerr := t.as.CountSiblingsInGraph(roots); kerr == nil && known < siblingDiscoveryThreshold {
						return actool.Errorf(fmt.Sprintf(
							"范围内的注册域在资产图里只登记了 %d 个具体资产（很可能只有目标主机本身）：还没做过同根子域发现。\n"+
								"先 intel_search(domain=\"%s\") / cert=\"%s\" / ip=<目标 IP> 把同根子域找出来，再用 insert_assets 登记（登记即自动入范围），然后才谈穷尽。\n"+
								"确属该主体只有这一个站点，请在 reason 里写明依据。",
							known, roots[0], roots[0])), nil
					}
					if pending, total, err := t.as.UntouchedSiblings(t.taskID, roots, 8); err == nil && total > 0 {
						samples := make([]string, 0, len(pending))
						for _, p := range pending {
							samples = append(samples, fmt.Sprintf("%s(id=%d)", p.Label, p.ID))
						}
						return actool.Errorf(fmt.Sprintf(
							"范围内还有 %d 个同主体资产一条意图都没派过（样本：%s）：这不是「无路可走」，是正面受阻。\n"+
								"先派意图去试它们（横向：换子域/换入口/用已有凭据或据点向内推），再谈穷尽。\n"+
								"确属不可达/超出授权，请逐个写清原因（可在意图里标 out-of-scope 收场）。",
							total, strings.Join(samples, "、"))), nil
					}
				}
			}
			// 深度门：范围内还有「测过但只终结了 1~2 类风险」的资产时也不许穷尽。
			// 这是"广度达标、深度很浅"的直接对策：一个资产只要有一类终态就从 debt 里
			// 消失，pct 于是 100%，但另外十几类根本没碰过。要求「穷尽」先面对它们。
			if t.ts != nil && t.taskID > 0 {
				if gaps, total, err := t.ts.DepthGaps(t.taskID, db.DefaultDepthAreas, 8); err == nil && total > 0 {
					lines := make([]string, 0, len(gaps))
					for _, g := range gaps {
						missing := g.Missing
						if len(missing) > 6 {
							missing = missing[:6]
						}
						lines = append(lines, fmt.Sprintf("%s(id=%d，已终结 %s；未碰 %s)",
							g.Label, g.AssetID, strings.Join(g.DoneAreas, "/"), strings.Join(missing, "/")))
					}
					return actool.Errorf(fmt.Sprintf(
						"范围内还有 %d 个资产只测了很少几类风险（阈值：每个资产至少终结 %d 类）：\n%s\n"+
							"「广度 pct=100%%」只说明每个资产都被碰过，不说明测透了。先补这些资产的其余风险类"+
							"（按面挑最贴合的一类，带 asset_ids 派意图），再谈穷尽。\n"+
							"确属该资产上不适用（无该类型入口），请在意图里以 out-of-scope 收场并写明原因。",
						total, db.DefaultDepthAreas, strings.Join(lines, "\n"))), nil
				}
			}
			// 台账门：还有「测过但没结论」的格子（blocked/exhausted/no_output）且没重派、没豁免时，
			// 不许穷尽——失败不是否定，这是复核员最常纠正的误判。口径与收官审计共用
			// db.CoverageGapSummary，避免两处各算一套。
			if t.taskID > 0 {
				if sum, err := t.ts.CoverageGapSummary(t.taskID); err == nil && len(sum.Failed) > 0 {
					lines := make([]string, 0, 6)
					for _, c := range sum.Failed {
						if len(lines) >= 6 {
							break
						}
						lines = append(lines, fmt.Sprintf("%s(id=%d) × %s", c.Label, c.AssetID, c.RiskArea))
					}
					return actool.Errorf(fmt.Sprintf(
						"台账里还有 %d 个「测过但没结论」的格子（failed：blocked/exhausted/no_output）：%s\n"+
							"失败只说明这次没测成，不说明这条路不通。先重派这些格子（换角度/补变体），"+
							"或用 dismiss_coverage 写清为什么不做，再谈穷尽。",
						len(sum.Failed), strings.Join(lines, "、"))), nil
				}
			}
			for _, id := range ids {
				n, err := t.ts.GetNode(id)
				if err != nil || n == nil {
					return actool.Errorf(fmt.Sprintf("evidence_ids 里的 %d 不存在于本任务", id)), nil
				}
				switch n.Kind {
				case db.KindFact, db.KindFinding, db.KindIntent:
				default:
					return actool.Errorf(fmt.Sprintf("evidence_ids 里的 %d 是 %q 节点：只能用事实/漏洞/意图作为依据", id, n.Kind)), nil
				}
			}
			payload := map[string]any{
				"summary":      "策略内已无可行方向：" + firstLine(reason, 120),
				"detail":       reason,
				"confidence":   "observed",
				"source":       "planner",
				"exhausted":    true,
				"evidence_ids": ids,
				"by":           "planner",
			}
			id, err := t.ts.AddNode(db.KindFact, payload, 0, "confirmed", "planner", nil)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			t.writes.Facts++
			if t.exhaustedFn != nil {
				t.exhaustedFn(reason)
			}
			return actool.Text(fmt.Sprintf("strategy exhausted recorded: fact %d（任务将在本轮结束后暂停，等待人工决定）", id)), nil
		})
}

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

// openProofGaps 列出仍停在 open_proof_gap、且没被 reason 点名的候选。
// 返回的每一行带 id 与摘要，直接拼进拒绝回执让 planner 逐条处理。
func (t *ToolSet) openProofGaps(reason string) []string {
	if t.ts == nil {
		return nil
	}
	nodes, err := t.ts.ListByKind(db.KindFinding, 500)
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range nodes {
		if n.State != db.FindingOpenProofGap {
			continue
		}
		if mentionsNodeID(reason, n.ID) {
			continue
		}
		var p struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		out = append(out, fmt.Sprintf("#%d %s", n.ID, firstLine(p.Summary, 90)))
	}
	return out
}

// mentionsNodeID 判断 reason 里是否点到了某个节点 id。按"数字边界"匹配，
// 避免 id=4 被 "4 条" 之类的文字误命中。
func mentionsNodeID(text string, id int64) bool {
	if id <= 0 {
		return false
	}
	pat := regexp.MustCompile(`(^|[^0-9])` + strconv.FormatInt(id, 10) + `([^0-9]|$)`)
	return pat.MatchString(text)
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
