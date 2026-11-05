package db

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// 探索台账（Exploration Ledger）：把「哪个资产的哪类风险还没测 / 没测成 / 已测 / 在跑」
// 摊成一张可枚举的网格。覆盖矩阵（CoverageMatrix）已经有数据，这里只做**投影 + 豁免折叠 +
// 缺口排序**，不新增存储。台账的宇宙 = task_scope 基（含 root_domain/service）；同根兄弟
// 资产仍由 scope_surface 负责，两者不混算。
//
// 为什么需要它：此前的覆盖度只是一句"pct=40%"和 8 条 top_gaps，规划者每轮靠回忆决定
// 测什么；没有一张可核对的空间，也就没有"测完"的客观标准。

// 台账格子状态。tested 只代表"有一条 done/negative 结论"，不代表测透。
const (
	LedgerUntested    = "untested"
	LedgerInFlight    = "inflight"
	LedgerTested      = "tested"
	LedgerFailed      = "failed"
	LedgerOutOfScope  = "out_of_scope"
	ledgerGapLimit    = 12
	coverageScopeName = "coverage"
)

// CoverageWaiver 是一条豁免（completion_waivers 行）。scope=coverage 时按 asset/risk 匹配；
// 任一为空 = 该维度全放行。
type CoverageWaiver struct {
	ID        int64     `json:"id"`
	Scope     string    `json:"scope"`
	AssetID   *int64    `json:"asset_id,omitempty"`
	RiskArea  string    `json:"risk_area,omitempty"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// LedgerCell 是「资产 × 风险类」的一格。
type LedgerCell struct {
	AssetID      int64  `json:"asset_id"`
	AssetType    string `json:"asset_type"`
	Label        string `json:"label"`
	RiskArea     string `json:"risk_area"`
	Status       string `json:"status"`
	Tested       int    `json:"tested"`
	Failed       int    `json:"failed"`
	InFlight     int    `json:"in_flight"`
	OutOfScope   int    `json:"out_of_scope"`
	Waived       bool   `json:"waived,omitempty"`
	WaiverReason string `json:"waiver_reason,omitempty"`
}

// LedgerGap 是台账里的一个待处理缺口（untested / failed 且未豁免）。
type LedgerGap struct {
	AssetID  int64  `json:"asset_id"`
	Label    string `json:"label"`
	RiskArea string `json:"risk_area"`
	Status   string `json:"status"`
}

// LedgerReport 是一次台账投影。Cells 只给工具详情用；态势里只播 Counts + Gaps。
type LedgerReport struct {
	Assets     int            `json:"assets"`
	CellsTotal int            `json:"cells_total"`
	Counts     map[string]int `json:"counts"`
	Waived     int            `json:"waived"`
	Gaps       []LedgerGap    `json:"gaps,omitempty"`
	GapsTotal  int            `json:"gaps_total"`
	CellsList  []LedgerCell   `json:"cells,omitempty"`
}

// BuildCoverageLedger 把覆盖矩阵投影成台账（纯函数，单测不需要 PG）。
// waivers 只取 scope=coverage 的行；areas 是风险类全集（db.RiskAreas）。
func BuildCoverageLedger(cov []CoverageMatrixAsset, waivers []CoverageWaiver, areas []string) *LedgerReport {
	rep := &LedgerReport{Assets: len(cov), Counts: map[string]int{}}
	for _, a := range cov {
		byArea := make(map[string]CoverageMatrixCell, len(a.Cells))
		for _, c := range a.Cells {
			byArea[c.RiskArea] = c
		}
		for _, area := range areas {
			c, ok := byArea[area]
			cell := LedgerCell{AssetID: a.ID, AssetType: a.Type, Label: a.Label, RiskArea: area}
			switch {
			case ok && c.Tested > 0:
				cell.Status, cell.Tested, cell.Failed, cell.InFlight, cell.OutOfScope =
					LedgerTested, c.Tested, c.Failed, c.InFlight, c.OutOfScope
			case ok && c.InFlight > 0:
				cell.Status, cell.Tested, cell.Failed, cell.InFlight, cell.OutOfScope =
					LedgerInFlight, c.Tested, c.Failed, c.InFlight, c.OutOfScope
			case ok && c.Failed > 0:
				cell.Status, cell.Tested, cell.Failed, cell.InFlight, cell.OutOfScope =
					LedgerFailed, c.Tested, c.Failed, c.InFlight, c.OutOfScope
			case ok && c.OutOfScope > 0:
				cell.Status, cell.Tested, cell.Failed, cell.InFlight, cell.OutOfScope =
					LedgerOutOfScope, c.Tested, c.Failed, c.InFlight, c.OutOfScope
			default:
				cell.Status = LedgerUntested
			}
			if w, ok := matchCoverageWaiver(waivers, a.ID, area); ok {
				cell.Waived = true
				cell.WaiverReason = w.Reason
				rep.Waived++
			}
			rep.Counts[cell.Status]++
			rep.CellsTotal++
			rep.CellsList = append(rep.CellsList, cell)
			if !cell.Waived && (cell.Status == LedgerUntested || cell.Status == LedgerFailed) {
				rep.Gaps = append(rep.Gaps, LedgerGap{
					AssetID: a.ID, Label: a.Label, RiskArea: area, Status: cell.Status,
				})
			}
		}
	}
	// 失败格排在前面：它是"测过但没结论"，比"还没碰过"更接近真相，也更该被重派。
	sort.Slice(rep.Gaps, func(i, j int) bool {
		si, sj := ledgerGapRank(rep.Gaps[i].Status), ledgerGapRank(rep.Gaps[j].Status)
		if si != sj {
			return si < sj
		}
		if rep.Gaps[i].Label != rep.Gaps[j].Label {
			return rep.Gaps[i].Label < rep.Gaps[j].Label
		}
		return rep.Gaps[i].RiskArea < rep.Gaps[j].RiskArea
	})
	rep.GapsTotal = len(rep.Gaps)
	if len(rep.Gaps) > ledgerGapLimit {
		rep.Gaps = rep.Gaps[:ledgerGapLimit]
	}
	return rep
}

func ledgerGapRank(status string) int {
	if status == LedgerFailed {
		return 0
	}
	return 1
}

// matchCoverageWaiver 找一条覆盖该格的豁免（asset/risk 任一为空 = 该维度全放行）。
func matchCoverageWaiver(waivers []CoverageWaiver, assetID int64, area string) (CoverageWaiver, bool) {
	for _, w := range waivers {
		if w.Scope != coverageScopeName {
			continue
		}
		if w.AssetID != nil && *w.AssetID != assetID {
			continue
		}
		if strings.TrimSpace(w.RiskArea) != "" && w.RiskArea != area {
			continue
		}
		return w, true
	}
	return CoverageWaiver{}, false
}

// CoverageWaivers 读本探索的豁免。表由 schema.sql 保证存在。
func (s *ExplorationStore) CoverageWaivers() ([]CoverageWaiver, error) {
	rows, err := s.db.Query(`SELECT id, scope, asset_id, COALESCE(risk_area,''), reason, created_at
FROM completion_waivers WHERE exploration_id=$1 ORDER BY id`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CoverageWaiver{}
	for rows.Next() {
		var w CoverageWaiver
		var assetID *int64
		if err := rows.Scan(&w.ID, &w.Scope, &assetID, &w.RiskArea, &w.Reason, &w.CreatedAt); err != nil {
			return nil, err
		}
		w.AssetID = assetID
		out = append(out, w)
	}
	return out, rows.Err()
}

// AddCoverageWaiver 写一条豁免。scope=coverage 时 asset/risk 可空（任一为空 = 全放行）；
// 其它 scope 只记整类放行。reason 必填（由工具层校验长度）。
func (s *ExplorationStore) AddCoverageWaiver(scope string, assetID *int64, riskArea, reason string) (int64, error) {
	scope = strings.TrimSpace(scope)
	switch scope {
	case "coverage", "chains", "proof_gaps", "leads":
	default:
		return 0, fmt.Errorf("未知的豁免类别 %q（coverage/chains/proof_gaps/leads）", scope)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, fmt.Errorf("豁免必须写理由")
	}
	riskArea = strings.TrimSpace(riskArea)
	if riskArea != "" {
		riskArea = NormalizeRiskArea(riskArea)
	}
	if scope != "coverage" {
		riskArea = "" // 其它类别不看风险类，整类放行
	}
	var id int64
	err := s.db.QueryRow(`INSERT INTO completion_waivers(exploration_id, scope, asset_id, risk_area, reason)
VALUES ($1,$2,$3,NULLIF($4,''),$5) RETURNING id`, s.expID, scope, assetID, riskArea, reason).Scan(&id)
	return id, err
}
