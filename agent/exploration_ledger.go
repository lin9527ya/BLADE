package agent

import (
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
