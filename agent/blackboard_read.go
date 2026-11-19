package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// compactBlackboardNode keeps a node useful for an agent without returning the
// full payload (commands/outputs can be large). It preserves the fields that
// drive follow-up exploration: summary, evidence, hypothesis, contract, deps.
func compactBlackboardNode(n *db.Node) map[string]any {
	if n == nil {
		return nil
	}
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	out := map[string]any{"id": n.ID, "kind": n.Kind, "state": n.State, "priority": n.Priority}
	for _, k := range []string{"summary", "detail", "evidence", "confidence", "polarity", "hypothesis", "success_criteria", "stop_condition", "risk_area", "asset_ids", "depends_on"} {
		if v, ok := p[k]; ok && v != nil {
			if s, ok := v.(string); ok {
				out[k] = clipLine(s, 300)
			} else {
				out[k] = v
			}
		}
	}
	if c, ok := p["contract"]; ok {
		out["contract"] = c
	}
	if n.BlockedReason != "" {
		out["blocked_reason"] = clipLine(n.BlockedReason, 200)
	}
	if n.LogicalSessionID != "" {
		out["logical_session_id"] = n.LogicalSessionID
	}
	return out
}

// assetDossier is a read-only per-asset view: asset metadata, direct neighbors,
// and the intents/facts/findings anchored to it (including direct source tasks).
// It answers "what do we already know/test about this host?" without paging the
// whole blackboard.
func (t *ToolSet) assetDossier() actool.CoreTool {
	return t.readExpTool("asset_dossier",
		"查看一个资产的完整探索档案：资产元数据、直接邻域（父域/子资产/同根/同 IP/同 C 段），以及本任务和直接关联任务里锚定到它的意图/事实/发现。用于深挖前先看已有结论和未解线索。\n"+
			"asset_id 是 list_assets / asset_neighbors 返回的资产 id，不是探索节点 id。",
		obj(map[string]any{
			"asset_id": idp("资产 id（必填）"),
			"limit":    intp("每个分节最多返回几条（默认 20，上限 100）"),
		}, "asset_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil || t.ts == nil {
				return actool.Errorf("asset_dossier 未启用: 需要资产库与任务上下文"), nil
			}
			var a struct {
				AssetID json.RawMessage `json:"asset_id"`
				Limit   int             `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.AssetID)
			if id <= 0 {
				return actool.Errorf("asset_dossier: asset_id 必填（list_assets 返回的资产 id）"), nil
			}
			limit := a.Limit
			if limit <= 0 {
				limit = 20
			}
			if limit > 100 {
				limit = 100
			}
			var assets []*db.Asset
			var err error
			if t.taskID > 0 {
				assets, err = t.as.GetByIDsInScope(t.taskID, []int64{id})
			} else {
				assets, err = t.as.GetByIDs([]int64{id})
			}
			if err != nil {
				return actool.Errorf("asset_dossier: " + err.Error()), nil
			}
			if len(assets) == 0 {
				return actool.Errorf(fmt.Sprintf("asset_dossier: 资产 %d 不存在或不在本任务范围", id)), nil
			}
			neighbors, nerr := t.as.AssetNeighbors(t.taskID, id, limit)
			if nerr != nil {
				neighbors = nil
			}
			refs, rerr := t.ts.AssetRefsWithSources(id)
			if rerr != nil {
				return actool.Errorf("asset_dossier: 读取锚定节点失败：" + rerr.Error()), nil
			}
			nodes := make([]map[string]any, 0, minInt(limit, len(refs)))
			for _, ref := range refs {
				if len(nodes) >= limit {
					break
				}
				n, err := t.ts.GetNodeWithSources(ref.ID)
				if err != nil || n == nil {
					continue
				}
				nodes = append(nodes, compactBlackboardNode(n))
			}
			return jsonResult(map[string]any{
				"asset":      assets[0],
				"neighbors":  neighbors,
				"nodes":      nodes,
				"node_total": len(refs),
				"filters":    map[string]any{"asset_id": id, "limit": limit},
			})
		})
}

// blackboardQuery is a filtered view over the task's exploration graph for
// questions the bounded situation window cannot answer: a route's history, all
// facts about one asset, open hypotheses, or a keyword search.
func (t *ToolSet) blackboardQuery() actool.CoreTool {
	return t.readExpTool("blackboard_query",
		"按过滤条件查询本任务黑板（只读）：kind/state/route/q/asset_id/limit/offset。\n"+
			"route 匹配意图 contract.route 或 risk_area；q 搜索 summary/detail/hypothesis；asset_id 按锚点过滤。\n"+
			"用于查看窗口外的历史、某条路线的全部意图/事实、某资产相关的结论，而不是翻流水账。",
		obj(map[string]any{
			"kind":     str("可选：intent/fact/finding/goal/...；留空=全部"),
			"state":    str("可选：open/running/done/negative/confirmed/..."),
			"route":    str("可选：contract.route 或 risk_area"),
			"q":        str("可选：summary/detail/hypothesis 关键词"),
			"asset_id": idp("可选：按资产锚点过滤"),
			"limit":    intp("每页条数，默认 20，上限 200"),
			"offset":   intp("偏移，默认 0"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("blackboard_query 需要任务上下文（探索图）"), nil
			}
			var a struct {
				Kind    string          `json:"kind"`
				State   string          `json:"state"`
				Route   string          `json:"route"`
				Q       string          `json:"q"`
				AssetID json.RawMessage `json:"asset_id"`
				Limit   int             `json:"limit"`
				Offset  int             `json:"offset"`
			}
			_ = json.Unmarshal(in, &a)
			nodes, total, err := t.ts.QueryBlackboard(db.BlackboardQuery{
				Kind: a.Kind, State: a.State, Route: a.Route, Q: a.Q,
				AssetID: pid(a.AssetID), Limit: a.Limit, Offset: a.Offset,
			})
			if err != nil {
				return actool.Errorf("blackboard_query: " + err.Error()), nil
			}
			out := make([]map[string]any, 0, len(nodes))
			for _, n := range nodes {
				out = append(out, compactBlackboardNode(n))
			}
			return jsonResult(map[string]any{
				"nodes": out, "total": total, "limit": a.Limit, "offset": a.Offset,
				"note": "只读过滤结果；完整正文用 node_detail(id)。",
			})
		})
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// proposeIntents lets a worker submit child directions discovered mid-run without
// mutating the frontier directly. The proposal becomes a fact, which wakes the
// planner; the planner can dedupe, add dependencies and decide what to dispatch.
func (t *ToolSet) proposeIntents() actool.CoreTool {
	return t.writeExpTool("propose_intents",
		"把执行中发现、但当前意图范围外的下一步方向作为【提案】交给 planner。它只写一条事实，不会直接创建意图；planner 复核后会决定是否 add_intent。\n"+
			"用于：利用链的下一步、发现的新入口/参数、值得复核的否定结论、需要扩面的新资产。\n"+
			"不要用它重复当前意图已经做过的事；提案要写清依据和最小下一步。",
		obj(map[string]any{
			"proposals": map[string]any{
				"type": "array",
				"items": obj(map[string]any{
					"summary":    str("一句话方向（必填）"),
					"hypothesis": str("可选假设"),
					"mechanism":  str("可选稳定打法键"),
					"route":      str("可选路线名"),
					"depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "可选前置意图 id"},
					"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "可选目标资产 id"},
					"reason":     str("为什么值得下一步（证据/观察）"),
				}, "summary"),
			},
		}, "proposals"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Proposals []struct {
					Summary    string            `json:"summary"`
					Hypothesis string            `json:"hypothesis"`
					Mechanism  string            `json:"mechanism"`
					Route      string            `json:"route"`
					DependsOn  []json.RawMessage `json:"depends_on"`
					AssetIDs   []json.RawMessage `json:"asset_ids"`
					Reason     string            `json:"reason"`
				} `json:"proposals"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if len(a.Proposals) == 0 {
				return actool.Errorf("proposals 不能为空"), nil
			}
			type proposal struct {
				Summary    string  `json:"summary"`
				Hypothesis string  `json:"hypothesis,omitempty"`
				Mechanism  string  `json:"mechanism,omitempty"`
				Route      string  `json:"route,omitempty"`
				DependsOn  []int64 `json:"depends_on,omitempty"`
				AssetIDs   []int64 `json:"asset_ids,omitempty"`
				Reason     string  `json:"reason,omitempty"`
			}
			out := make([]proposal, 0, len(a.Proposals))
			for _, p := range a.Proposals {
				if strings.TrimSpace(p.Summary) == "" {
					continue
				}
				out = append(out, proposal{
					Summary: strings.TrimSpace(p.Summary), Hypothesis: strings.TrimSpace(p.Hypothesis),
					Mechanism: strings.TrimSpace(p.Mechanism), Route: strings.TrimSpace(p.Route),
					DependsOn: pidList(p.DependsOn), AssetIDs: pidList(p.AssetIDs),
					Reason: strings.TrimSpace(p.Reason),
				})
			}
			if len(out) == 0 {
				return actool.Errorf("至少一条提案需要 summary"), nil
			}
			detail, _ := json.Marshal(map[string]any{"proposals": out})
			id, err := t.recordOneFact(factItem{
				Summary:    fmt.Sprintf("[意图提案] %d 个下一步方向", len(out)),
				Detail:     string(detail),
				Confidence: "observed",
				Polarity:   "positive",
				Enables:    []string{"info"},
			}, 0)
			if err != nil {
				return actool.Errorf("记录意图提案失败：" + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"fact_id": id, "proposals": len(out), "status": "pending_planner_review",
				"note": "提案只写入黑板，不会自动创建意图；planner 复核后决定。",
			})
		})
}

// verifyEvidence is an independent, read-only evidence check. It does not ask the
// worker whether it succeeded; it inspects the stored facts, their confidence,
// evidence text, and contradicts/supersedes edges. The planner uses it before
// proving a goal or trusting a negative conclusion.
func (t *ToolSet) verifyEvidence() actool.CoreTool {
	return t.readExpTool("verify_evidence",
		"独立核对一个意图产出的证据（或指定事实）：检查 evidence 是否为空、confidence、polarity，以及是否被 contradicts/supersedes 边推翻。只返回判断与缺口，不改图、不替 worker 补证据。",
		obj(map[string]any{
			"intent_id": idp("要核对的意图 id（与 fact_id 二选一）"),
			"fact_id":   idp("要核对的事实 id（与 intent_id 二选一）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("verify_evidence 需要任务上下文"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				FactID   json.RawMessage `json:"fact_id"`
			}
			_ = json.Unmarshal(in, &a)
			var ids []int64
			intentID := pid(a.IntentID)
			factID := pid(a.FactID)
			if intentID > 0 {
				var err error
				ids, err = t.ts.FactsYielded(intentID)
				if err != nil {
					return actool.Errorf("读取意图产出失败：" + err.Error()), nil
				}
			} else if factID > 0 {
				ids = []int64{factID}
			} else {
				return actool.Errorf("intent_id 或 fact_id 至少给一个"), nil
			}
			if len(ids) == 0 {
				return jsonResult(map[string]any{"verdict": "insufficient", "reason": "没有可核对的事实产出", "intent_id": intentID})
			}
			edges, _ := t.ts.EdgesTouching(ids)
			contradicted := map[int64]string{}
			superseded := map[int64]bool{}
			for _, e := range edges {
				if e.Rel == db.RelContradicts {
					contradicted[e.To] = fmt.Sprintf("%d --contradicts--> %d", e.From, e.To)
				}
				if e.Rel == db.RelSupersedes {
					superseded[e.To] = true
				}
			}
			type check struct {
				ID         int64  `json:"id"`
				Summary    string `json:"summary"`
				Verdict    string `json:"verdict"`
				Confidence string `json:"confidence,omitempty"`
				Evidence   string `json:"evidence,omitempty"`
				Note       string `json:"note,omitempty"`
			}
			checks := make([]check, 0, len(ids))
			overall := "supported"
			for _, id := range ids {
				n, err := t.ts.GetNode(id)
				if err != nil || n == nil || n.Kind != db.KindFact {
					checks = append(checks, check{ID: id, Verdict: "insufficient", Note: "不是可核对的事实节点"})
					overall = "insufficient"
					continue
				}
				var p map[string]any
				_ = json.Unmarshal(n.Payload, &p)
				summary, _ := p["summary"].(string)
				evidence, _ := p["evidence"].(string)
				confidence, _ := p["confidence"].(string)
				c := check{ID: id, Summary: clipLine(summary, 160), Confidence: confidence, Evidence: clipLine(evidence, 160)}
				switch {
				case contradicted[id] != "":
					c.Verdict, c.Note = "contradicted", "存在矛盾边："+contradicted[id]
					overall = "contradicted"
				case superseded[id]:
					c.Verdict, c.Note = "superseded", "已被后续事实替代"
					if overall == "supported" {
						overall = "insufficient"
					}
				case strings.TrimSpace(evidence) == "":
					c.Verdict, c.Note = "insufficient", "缺少 evidence 原文"
					if overall == "supported" {
						overall = "insufficient"
					}
				case confidence == "inferred":
					c.Verdict, c.Note = "weak", "只有推断 confidence=inferred"
					if overall == "supported" {
						overall = "weak"
					}
				default:
					c.Verdict = "supported"
				}
				checks = append(checks, c)
			}
			return jsonResult(map[string]any{
				"verdict": overall, "intent_id": intentID, "fact_id": factID,
				"checks": checks, "note": "verdict=supported 仅表示证据形式满足；真实可复现仍需人工/独立复测。",
			})
		})
}
