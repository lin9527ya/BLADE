package server

import (
	"net/http"

	"github.com/lin9527ya/blade/db"
)

// explorationLedger 是探索台账的只读入口：GET /api/exploration/ledger?task=<id>。
//
// 返回台账投影（状态计数 + 缺口）与通路台账（unused/open/advanced）。给人和后续 UI 用；
// 与 agent 侧的工具同一份 db 实现，口径不会漂移。
func (s *Server) explorationLedger(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, http.StatusNotFound, "任务不存在")
		return
	}
	taskID := tidOfTask(t)
	cov, err := t.Store.CoverageMatrix(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	waivers, err := t.Store.CoverageWaivers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ledger := db.BuildCoverageLedger(cov, waivers, db.RiskAreas)
	out := map[string]any{
		"task_id": taskID,
		"ledger": map[string]any{
			"assets": ledger.Assets, "cells_total": ledger.CellsTotal,
			"counts": ledger.Counts, "waived": ledger.Waived,
			"gaps": ledger.Gaps, "gaps_total": ledger.GapsTotal,
		},
	}
	if chains, cerr := t.Store.ChainLedger(); cerr == nil {
		out["chains"] = chains
	}
	writeJSON(w, http.StatusOK, out)
}
