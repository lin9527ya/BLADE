package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// 探索台账（agent 侧）：把 db 层的台账投影接进 planner 的视野，并提供两个出口——
// 读全量（exploration_ledger）与逐格豁免（dismiss_coverage）。
//
// 纪律与 breadth 一致：只有在覆盖度开启、且有任务上下文时才存在；worker 视图不带它
// （worker 只做单条意图，广度是规划职责）。

// coverageSnapshot 返回本轮覆盖矩阵（每轮只查一次）。覆盖度关闭/无任务上下文时 nil。
func (t *ToolSet) coverageSnapshot() []db.CoverageMatrixAsset {
	if t.coverageDisabled || t.ts == nil || t.taskID <= 0 {
		return nil
	}
	if t.coverageLoaded {
		return t.coverageCached
	}
	t.coverageLoaded = true
	if cov, err := t.ts.CoverageMatrix(t.taskID); err == nil {
		t.coverageCached = cov
	}
	return t.coverageCached
}

// ledgerReport 把矩阵 + 豁免折叠成台账（每轮只算一次）。豁免写入后调用方要清缓存。
func (t *ToolSet) ledgerReport() *db.LedgerReport {
	if t.ledgerLoaded {
		return t.ledgerCached
	}
	cov := t.coverageSnapshot()
	if cov == nil {
		return nil
	}
	t.ledgerLoaded = true
	waivers, _ := t.ts.CoverageWaivers() // 读失败按"没有豁免"处理：宁可多报缺口，不可漏报
	t.ledgerCached = db.BuildCoverageLedger(cov, waivers, db.RiskAreas)
	return t.ledgerCached
}

// explorationLedger 读台账全量（可按资产/风险类过滤，cells 分页）。
func (t *ToolSet) explorationLedger() actool.CoreTool {
	return t.readExpTool("exploration_ledger",
		"查询本任务「探索台账」：范围内每个资产 × 14 个风险类的状态"+
			"（untested 未触及 / inflight 在跑 / tested 已有结论 / failed 没测成 / out_of_scope 越界），"+
			"以及豁免（waived）。untested 与 failed 是发散的来源；**tested 只代表有一条结论，不代表测透**。\n"+
			"顶部 gaps 是待处理缺口（failed 排在前面），gaps_total 是全部（gaps 只截前 12）。\n"+
			"对暂时不打算测的格子用 dismiss_coverage 写清理由；不要静默忽略。",
		obj(map[string]any{
			"asset_id":  idp("只看某个资产（可选）"),
			"risk_area": str("只看某个风险类（可选，如 sqli/rce/authz）"),
			"limit":     intp("cells 返回上限（默认 200，最多 500）"),
			"gaps_only": map[string]any{"type": "boolean", "description": "只返回缺口（untested/failed 且未豁免）"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if cov := t.coverageSnapshot(); cov == nil {
				return actool.Errorf("探索台账需要覆盖度开启且已有任务上下文"), nil
			}
			var a struct {
				AssetID  json.RawMessage `json:"asset_id"`
				RiskArea string          `json:"risk_area"`
				Limit    int             `json:"limit"`
				GapsOnly bool            `json:"gaps_only"`
			}
			_ = json.Unmarshal(in, &a)
			rep := t.ledgerReport()
			if rep == nil {
				return actool.Errorf("探索台账暂不可用"), nil
			}
			assetID := pid(a.AssetID)
			area := strings.ToLower(strings.TrimSpace(a.RiskArea))
			limit := a.Limit
			if limit <= 0 {
				limit = 200
			}
			if limit > 500 {
				limit = 500
			}
			cells := make([]db.LedgerCell, 0, len(rep.CellsList))
			for _, c := range rep.CellsList {
				if assetID > 0 && c.AssetID != assetID {
					continue
				}
				if area != "" && c.RiskArea != area {
					continue
				}
				if a.GapsOnly && (c.Waived || (c.Status != db.LedgerUntested && c.Status != db.LedgerFailed)) {
					continue
				}
				if len(cells) >= limit {
					break
				}
				cells = append(cells, c)
			}
			return jsonResult(map[string]any{
				"risk_areas": db.RiskAreas, "counts": rep.Counts, "assets": rep.Assets,
				"cells_total": rep.CellsTotal, "waived": rep.Waived,
				"gaps": rep.Gaps, "gaps_total": rep.GapsTotal, "cells": cells,
				"note": "failed 是没测成（blocked/exhausted/no_output），不是死路；tested 只代表有一条结论",
			})
		})
}

// dismissCoverage 对一格（或整个资产的风险类）写豁免。与 dismiss_leads 同构：
// 可以放行，但必须说得出理由；豁免进收官审计与报告的数据缺项。
func (t *ToolSet) dismissCoverage() actool.CoreTool {
	return t.writeExpTool("dismiss_coverage",
		"把台账里的一格标记为「接受不做」（豁免）：例如范围内但价值过低、需要客户授权、代价过高、"+
			"或已有充分证据表明不适用。\n"+
			"**可以放行，但必须写得出理由**（≥10 字）：豁免会进收官审计与报告的数据缺项。\n"+
			"risk_area 留空 = 该资产全部风险类放行（慎用）；asset_id 留空 = 范围内全部资产该类放行（更慎用）。",
		obj(map[string]any{
			"asset_id":  idp("资产 id（来自台账/覆盖矩阵；留空=全部范围内资产）"),
			"risk_area": str("风险类（sqli/xss/...；留空=该资产全部风险类）"),
			"reason":    str("【必填】接受不做的理由（≥10 字）"),
		}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.coverageDisabled || t.ts == nil || t.taskID <= 0 {
				return actool.Errorf("dismiss_coverage 需要覆盖度开启且有任务上下文"), nil
			}
			var a struct {
				AssetID  json.RawMessage `json:"asset_id"`
				RiskArea string          `json:"risk_area"`
				Reason   string          `json:"reason"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			reason := strings.TrimSpace(a.Reason)
			if len([]rune(reason)) < 10 {
				return actool.Errorf("reason 至少 10 个字：写清「接受的是什么风险、为什么不做」，否则收官时人和报告都看不懂"), nil
			}
			area := strings.ToLower(strings.TrimSpace(a.RiskArea))
			if area != "" && !validRiskArea(area) {
				return actool.Errorf(fmt.Sprintf("risk_area %q 不在风险类词表里（可用：%s）", area, strings.Join(db.RiskAreas, "/"))), nil
			}
			assetID := pid(a.AssetID)
			var ptr *int64
			if assetID > 0 {
				found := false
				for _, as := range t.coverageSnapshot() {
					if as.ID == assetID {
						found = true
						break
					}
				}
				if !found {
					return actool.Errorf(fmt.Sprintf("资产 %d 不在本任务范围内：只能对台账里的资产写豁免", assetID)), nil
				}
				ptr = &assetID
			}
			id, err := t.ts.AddCoverageWaiver("coverage", ptr, area, reason)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			t.ledgerLoaded = false // 让本轮后续读到的台账带上这条豁免
			t.coverageLoaded = false
			return actool.Text("coverage waiver recorded: " + strconv.FormatInt(id, 10) + "\n" +
				"它会在收官审计里放行对应格子；报告的数据缺项会列出理由。"), nil
		})
}

func validRiskArea(area string) bool {
	for _, a := range db.RiskAreas {
		if a == area {
			return true
		}
	}
	return false
}
