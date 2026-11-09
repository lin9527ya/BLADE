package db

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 收官审计（Completion Gate）：目标达成只是"完成"的一半，另一半是"探索充分"。
//
// 这里只回答"还欠什么"，不决定停不停——调用方（prove_goal / 引擎）按设置的语气处理。
// 遗留物由 agent.cleanupgate 单独负责（它需要 ToolSet 的接受风险标记）；本函数覆盖
// 另外四类：未闭合候选、覆盖/深度缺口、未闭合通路、未处理线索。
//
// 豁免只有一张表：completion_waivers（scope=coverage 精确到资产×风险类；其它类别整类放行）。

const (
	GapProofGap = "proof_gap"
	GapCoverage = "coverage"
	GapChain    = "chain"
	GapLead     = "lead"
)

// CompletionGap 是一条收官缺口，Detail 直接给模型/人看。
type CompletionGap struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// CompletionOptions 是审计范围。CoverageEnabled=false 时跳过覆盖/深度类闸门
// （覆盖度关闭的任务没有分母，不能拿它卡人）。
type CompletionOptions struct {
	TaskID          int64
	CoverageEnabled bool
}

// CoverageGapSummary 是「覆盖/深度」缺口的**共用口径**：收官审计与穷尽复核都走这里，
// 避免两处各算一套（失败格/浅资产的口径一漂移，两个门就会互相打脸）。
type CoverageGapSummary struct {
	Failed         []LedgerCell    `json:"failed"`
	ShallowTotal   int             `json:"shallow_total"`
	ShallowSamples []AssetDepthGap `json:"shallow_samples,omitempty"`
}

// CoverageGapSummary 计算失败格（未豁免）与低于深度下限的资产。
func (s *ExplorationStore) CoverageGapSummary(taskID int64) (CoverageGapSummary, error) {
	var out CoverageGapSummary
	if taskID <= 0 {
		return out, nil
	}
	cov, err := s.CoverageMatrix(taskID)
	if err != nil {
		return out, err
	}
	waivers, err := s.CoverageWaivers()
	if err != nil {
		return out, err
	}
	rep := BuildCoverageLedger(cov, waivers, RiskAreas)
	for _, c := range rep.CellsList {
		if !c.Waived && c.Status == LedgerFailed {
			out.Failed = append(out.Failed, c)
		}
	}
	if gaps, total, gerr := s.DepthGapsWithWaivers(taskID, DefaultDepthAreas, 8, waivers); gerr == nil {
		out.ShallowTotal = total
		out.ShallowSamples = gaps
	} else {
		return out, gerr
	}
	return out, nil
}

// CompletionGaps 计算当前探索的收官缺口（只读）。
func (s *ExplorationStore) CompletionGaps(as *AssetStore, opt CompletionOptions) ([]CompletionGap, error) {
	if !s.InReadSnapshot() {
		var out []CompletionGap
		err := s.ReadSnapshot(context.Background(), func(d *DB, view *ExplorationStore) error {
			var err error
			var assets *AssetStore
			if as != nil {
				assets = d.Assets()
			}
			out, err = view.CompletionGaps(assets, opt)
			return err
		})
		return out, err
	}
	waivers, err := s.CoverageWaivers()
	if err != nil {
		return nil, err
	}
	waived := map[string]bool{}
	for _, w := range waivers {
		// 指定了资产/风险类的 coverage waiver 只在对应格子折叠，不能跳过整任务审计。
		if w.Scope == "coverage" && (w.AssetID != nil || strings.TrimSpace(w.RiskArea) != "") {
			continue
		}
		waived[w.Scope] = true
	}
	var gaps []CompletionGap

	// ① 未闭合候选：open_proof_gap 不是"已排除"，收官前必须给出结论 or 显式豁免。
	states, err := s.CountKindStates(KindFinding)
	if err != nil {
		return nil, err
	}
	{
		if n := states["open_proof_gap"]; n > 0 && !waived["proof_gaps"] {
			gaps = append(gaps, CompletionGap{Kind: GapProofGap, Detail: fmt.Sprintf(
				"%d 条候选还是 open_proof_gap：用 close_candidate 收成 ruled_out（写清阻断点），"+
					"或 accept_completion_risk(scope=\"proof_gaps\") 说明为什么带着存疑收尾", n)})
		}
	}

	// ② 覆盖/深度缺口（覆盖度开启时）。
	if opt.CoverageEnabled && opt.TaskID > 0 && !waived["coverage"] {
		sum, err := s.CoverageGapSummary(opt.TaskID)
		if err != nil {
			return nil, err
		}
		{
			var parts []string
			if len(sum.Failed) > 0 {
				parts = append(parts, fmt.Sprintf("%d 个格子测过但没结论（failed）", len(sum.Failed)))
			}
			if sum.ShallowTotal > 0 {
				parts = append(parts, fmt.Sprintf("%d 个资产终结的风险类少于 %d 类", sum.ShallowTotal, DefaultDepthAreas))
			}
			if len(parts) > 0 {
				gaps = append(gaps, CompletionGap{Kind: GapCoverage, Detail: strings.Join(parts, "；") +
					"。先补测（exploration_ledger 看缺口），或 accept_completion_risk(scope=\"coverage\") 写明接受的代价"})
			}
		}
	}

	// ③ 未闭合通路：从没被任何意图动用、或动过没产出且最后一次没善终。
	rep, err := s.ChainLedger()
	if err != nil {
		return nil, err
	}
	if !waived["chains"] {
		var open []string
		totalOpen := 0
		for _, r := range rep.Rows {
			if r.Status != ChainStatusUnused && r.Status != ChainStatusOpen {
				continue
			}
			totalOpen++
			if len(open) < 5 {
				open = append(open, fmt.Sprintf("%s#%d（%s）", r.Kind, r.LootID, r.Status))
			}
		}
		if len(open) > 0 {
			gaps = append(gaps, CompletionGap{Kind: GapChain, Detail: fmt.Sprintf(
				"%d 条据点/凭据还没推进：%s。用 chain_detail 看下一步，或 accept_completion_risk(scope=\"chains\")",
				totalOpen, strings.Join(open, "、"))})
		}
	}

	// ④ 未处理线索：worker 已经把下一步写出来了，不处理等于丢掉。
	n, err := s.OpenLeadsCount()
	if err != nil {
		return nil, err
	}
	if n > 0 && !waived["leads"] {
		gaps = append(gaps, CompletionGap{Kind: GapLead, Detail: fmt.Sprintf(
			"%d 条线索还没处理：用 add_intent(converts_lead_ids) 转成意图，或 dismiss_leads 写清理由；"+
				"整类接受用 accept_completion_risk(scope=\"leads\")", n)})
	}

	sort.SliceStable(gaps, func(i, j int) bool { return gaps[i].Kind < gaps[j].Kind })
	return gaps, nil
}

// OpenLeadsCount 数还没被转换/驳回的线索。口径与 graph_overview 的 leads_open 一致：
// converted 只看意图 payload 的 converted_lead_ids，dismissed 只看事实 payload 的 dismissed_leads。
func (s *ExplorationStore) OpenLeadsCount() (int, error) {
	facts, err := s.AllByKindState(KindFact, "")
	if err != nil {
		return 0, err
	}
	intents, err := s.AllByKindState(KindIntent, "")
	if err != nil {
		return 0, err
	}
	converted := map[string]bool{}
	for _, n := range intents {
		var p struct {
			Converted []string `json:"converted_lead_ids"`
		}
		if json.Unmarshal(n.Payload, &p) == nil {
			for _, id := range p.Converted {
				converted[strings.ToUpper(strings.TrimSpace(id))] = true
			}
		}
	}
	open := 0
	for _, n := range facts {
		if n.State == "origin" || n.State == "dismissed" {
			continue
		}
		var fp map[string]any
		if json.Unmarshal(n.Payload, &fp) != nil {
			continue
		}
		leads, _ := fp["leads"].([]any)
		if len(leads) == 0 {
			continue
		}
		dismissed := map[string]bool{}
		if arr, ok := fp["dismissed_leads"].([]any); ok {
			for _, v := range arr {
				if sv, ok := v.(string); ok {
					dismissed[strings.ToUpper(strings.TrimSpace(sv))] = true
				}
			}
		}
		for i := range leads {
			// lead id 与 agent 侧 leadID 同构：L<事实id>-<下标(0-based)>。
			key := strings.ToUpper(fmt.Sprintf("L%d-%d", n.ID, i))
			if converted[key] || dismissed[key] {
				continue
			}
			open++
		}
	}
	return open, nil
}
