package agent

import (
	"context"
	"encoding/json"
	"fmt"

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
