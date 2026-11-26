package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// SetWorkerConcurrency injects the task's configured worker concurrency so queue
// soft/hard backpressure budgets scale proportionally with available execution slots.
func (t *ToolSet) SetWorkerConcurrency(n int) {
	if t != nil {
		t.workerConcurrency = n
	}
}

func (t *ToolSet) effectiveWorkerConcurrency() int {
	if t != nil && t.workerConcurrency > 0 {
		return t.workerConcurrency
	}
	return 4
}

func (t *ToolSet) frontierBudgetCaps() (softCap, hardCap int) {
	workers := t.effectiveWorkerConcurrency()
	softCap = workers * 3
	if softCap < 12 {
		softCap = 12
	}
	hardCap = workers * 5
	if hardCap < 24 {
		hardCap = 24
	}
	return softCap, hardCap
}

type frontierBudgetInfo struct {
	Claimable      int  `json:"claimable"`
	OpenTotal      int  `json:"open_total"`
	Running        int  `json:"running"`
	PausedDeferred int  `json:"paused_deferred"`
	Workers        int  `json:"workers"`
	SoftCap        int  `json:"soft_cap"`
	HardCap        int  `json:"hard_cap"`
	Overloaded     bool `json:"overloaded"`
	AtHardCap      bool `json:"at_hard_cap"`
}

func (t *ToolSet) frontierBudgetSnapshot() (frontierBudgetInfo, error) {
	softCap, hardCap := t.frontierBudgetCaps()
	info := frontierBudgetInfo{
		Workers: t.effectiveWorkerConcurrency(),
		SoftCap: softCap,
		HardCap: hardCap,
	}
	if t == nil || t.ts == nil {
		return info, nil
	}
	snap, err := t.ts.IntentDispatchSnapshot()
	if err != nil {
		return info, err
	}
	info.Claimable = snap.Claimable
	info.OpenTotal = snap.Open
	info.Running = snap.Running
	info.Overloaded = snap.Claimable >= softCap
	info.AtHardCap = snap.Claimable >= hardCap
	if parked, perr := t.ts.CountDeferredParkedIntents(); perr == nil {
		info.PausedDeferred = parked
	}
	return info, nil
}

func (b frontierBudgetInfo) toMap() map[string]any {
	m := map[string]any{
		"claimable":       b.Claimable,
		"open_total":      b.OpenTotal,
		"running":         b.Running,
		"paused_deferred": b.PausedDeferred,
		"workers":         b.Workers,
		"soft_cap":        b.SoftCap,
		"hard_cap":        b.HardCap,
		"overloaded":      b.Overloaded,
		"at_hard_cap":     b.AtHardCap,
	}
	switch {
	case b.AtHardCap:
		m["recommendation"] = "队列已达硬上限：必须先用 reprioritize_intents 降权/暂缓(park)无关支线或 drop_intent 清理低效分支；仅高价值突破方向(priority>=8 或直接派生自发现/战果)允许入队。"
	case b.Overloaded:
		m["recommendation"] = "活跃队列已过载：停止扩撒泛侦察；优先切入 set_exploration_focus(deepen) 并用 reprioritize_intents 集中打穿最优路线，边缘线索留在 leads。"
	}
	return m
}

func (t *ToolSet) populatePortfolioSignals(out map[string]any) {
	if t == nil || t.ts == nil || out == nil {
		return
	}
	if focus, err := t.ts.GetExplorationFocus(); err == nil {
		out["exploration_focus"] = focus
	}
	if budget, err := t.frontierBudgetSnapshot(); err == nil {
		out["frontier_budget"] = budget.toMap()
		out["deferred_parked_total"] = budget.PausedDeferred
	}
	if card, err := t.ts.RouteScorecard(12); err == nil && len(card) > 0 {
		out["route_scorecard"] = card
	}
}

// openIntentWindow merges top-ranked claimable intents first (preserving real
// Best-First dispatch order) followed by any remaining open intents (e.g. strict
// blocked or tail items) up to limit.
func (t *ToolSet) openIntentWindow(limit int) []*db.Node {
	if t == nil || t.ts == nil || limit <= 0 {
		return nil
	}
	fr, _ := t.ts.FrontierForSlot("", nil, limit)
	openNodes, _ := t.ts.ListByKindState(db.KindIntent, "open", limit*2)
	seen := make(map[int64]bool, len(fr))
	merged := make([]*db.Node, 0, limit)
	for _, n := range fr {
		if n == nil || seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		merged = append(merged, n)
		if len(merged) >= limit {
			return merged
		}
	}
	for _, n := range openNodes {
		if n == nil || seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		merged = append(merged, n)
		if len(merged) >= limit {
			break
		}
	}
	return merged
}

func (t *ToolSet) reprioritizeIntents() actool.CoreTool {
	return t.writeExpTool("reprioritize_intents",
		"批量动态调整排队中(open)或已暂缓(paused)意图的优先级与执行状态，让 Worker 集中算力打穿当前最优突破链，或暂缓/恢复外围广度支线。\n"+
			"★ action='park'：将暂时无关的泛探索置为 paused(deferred_for_focus)，聚焦链跑完后可恢复或自动唤醒；\n"+
			"★ action='resume'：将暂缓意图恢复回 open；action='keep'(默认)：保留原状态并更新 priority(0-10)。\n"+
			"★ reason 必填，说明重排依据（如命中漏洞/战果聚焦，或清理低收益支线）。",
		obj(map[string]any{
			"items": map[string]any{
				"type":        "array",
				"description": "要调整的意图列表，至少 1 条。",
				"items": obj(map[string]any{
					"intent_id": idp("目标意图 id（open 或 paused）"),
					"priority":  intp("可选：新优先级 0-10（8-10=高危突破/深挖，5-7=常规，1-4=低优备用）"),
					"action":    str("可选：keep（仅调优优先级，默认）| park（暂缓让路）| resume（恢复排队）"),
				}, "intent_id"),
			},
			"reason": str("为什么调整这批队列权重/状态（必填，进时间线审计）"),
		}, "items", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("reprioritize_intents 需要任务上下文"), nil
			}
			var a struct {
				Items []struct {
					IntentID json.RawMessage `json:"intent_id"`
					Priority *int            `json:"priority"`
					Action   string          `json:"action"`
				} `json:"items"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("reprioritize_intents 参数解析失败: " + err.Error()), nil
			}
			reason := strings.TrimSpace(a.Reason)
			if reason == "" {
				return actool.Errorf("reprioritize_intents: reason 必填"), nil
			}
			if len(a.Items) == 0 {
				return actool.Errorf("reprioritize_intents: items 不能为空"), nil
			}
			updates := make([]db.ReprioritizeIntentUpdate, 0, len(a.Items))
			for _, it := range a.Items {
				updates = append(updates, db.ReprioritizeIntentUpdate{
					IntentID: pid(it.IntentID),
					Priority: it.Priority,
					Action:   it.Action,
				})
			}
			res, err := t.ts.BatchUpdateIntents(reason, updates)
			if err != nil {
				return actool.Errorf("reprioritize_intents: " + err.Error()), nil
			}
			changed := 0
			for _, r := range res {
				if r.Changed {
					changed++
				}
			}
			if changed > 0 {
				t.auditLootExec("reprioritize_intents",
					fmt.Sprintf("动态重排 %d 条意图权重/暂缓状态", changed), "理由："+reason)
				if t.notifyWorkers != nil {
					t.notifyWorkers()
				}
			}
			out := map[string]any{
				"updated": changed,
				"results": res,
			}
			if budget, berr := t.frontierBudgetSnapshot(); berr == nil {
				out["frontier_budget"] = budget.toMap()
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) setExplorationFocus() actool.CoreTool {
	return t.writeExpTool("set_exploration_focus",
		"显式切换任务的探索模式与当前焦点路线/资产：\n"+
			"★ mode='explore'：广度探索期，按预算拓展不同机理与未测入口；\n"+
			"★ mode='deepen'：收敛深挖期，要求至少传入 focus_routes 或 focus_asset_ids 之一。调度器会大幅提高焦点路线/资产得分并压低无关泛侦察，集中全部 Worker 算力把已发现的突破口打穿。",
		obj(map[string]any{
			"mode":            str("explore | deepen"),
			"focus_routes":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "聚焦路线名列表（对应 contract.route / risk_area / asset:<id>）"},
			"focus_asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "聚焦资产 id 列表"},
			"focus_note":      str("可选：一句话说明当前为什么集中打穿这条链，或下一步到哪停止"),
			"reason":          str("切换模式或聚焦目标的依据（必填，进时间线审计）"),
		}, "mode", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_exploration_focus 需要任务上下文"), nil
			}
			var a struct {
				Mode          string            `json:"mode"`
				FocusRoutes   []string          `json:"focus_routes"`
				FocusAssetIDs []json.RawMessage `json:"focus_asset_ids"`
				FocusNote     string            `json:"focus_note"`
				Reason        string            `json:"reason"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("set_exploration_focus 参数解析失败: " + err.Error()), nil
			}
			reason := strings.TrimSpace(a.Reason)
			mode := strings.ToLower(strings.TrimSpace(a.Mode))
			if reason == "" {
				return actool.Errorf("set_exploration_focus: reason 必填"), nil
			}
			if mode != db.ExploreModeExplore && mode != db.ExploreModeDeepen {
				return actool.Errorf("set_exploration_focus: mode 必须是 explore 或 deepen"), nil
			}
			focus := db.NormalizeExplorationFocus(db.ExplorationFocus{
				Mode:          mode,
				FocusRoutes:   a.FocusRoutes,
				FocusAssetIDs: pidList(a.FocusAssetIDs),
				FocusNote:     a.FocusNote,
				Reason:        reason,
			})
			if focus.Mode == db.ExploreModeDeepen && !focus.HasTargets() {
				return actool.Errorf("set_exploration_focus: mode='deepen' 时必须指定 focus_routes 或 focus_asset_ids"), nil
			}
			if err := t.ts.SetExplorationFocus(focus); err != nil {
				return actool.Errorf("set_exploration_focus: " + err.Error()), nil
			}
			t.auditLootExec("set_exploration_focus",
				fmt.Sprintf("探索策略切换为 %s（routes=%v assets=%v）", focus.Mode, focus.FocusRoutes, focus.FocusAssetIDs),
				"理由："+reason)
			if t.notifyWorkers != nil {
				t.notifyWorkers()
			}
			out := map[string]any{"focus": focus}
			if budget, berr := t.frontierBudgetSnapshot(); berr == nil {
				out["frontier_budget"] = budget.toMap()
			}
			return jsonResult(out)
		})
}
