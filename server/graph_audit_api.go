package server

import "net/http"

// explorationInvariants 是图完整性体检的只读入口：GET /api/exploration/invariants?task=<id>。
//
// 结果与启动时写进日志的是同一份（db.ExplorationStore.AuditGraph），给人和后续 UI 看。
// 刻意不提供"一键修复"：修复动作（重派前置 / unblock_intent / 删意图）都是策略决定，
// 由规划者或操作者明确执行。
func (s *Server) explorationInvariants(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, http.StatusNotFound, "任务不存在")
		return
	}
	rep, err := t.Store.AuditGraph()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
