package server

import (
	"net/http"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// 漏洞案例（P2-7）：把同一根因/同一资产的重复上报归并成一个独立缺陷。
// 原始 findings 保留为成员；案例持有统一报告与版本（report_version 做 CAS）。
// 路由：/api/exploration/finding-cases...

func (s *Server) listFindingCases(w http.ResponseWriter, r *http.Request) {
	taskID := int64(atoiDefault(r.URL.Query().Get("task_id"), 0))
	if taskID <= 0 {
		writeErr(w, http.StatusBadRequest, "task_id 必填")
		return
	}
	cases, err := s.m.pg.ListFindingCases(taskID, 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cases == nil {
		cases = []*db.FindingCase{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": cases, "count": len(cases)})
}

func (s *Server) createFindingCase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID     int64   `json:"task_id"`
		Title      string  `json:"title"`
		Reason     string  `json:"reason"`
		Severity   string  `json:"severity"`
		FindingIDs []int64 `json:"finding_ids"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TaskID <= 0 || len(req.FindingIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "task_id 与 finding_ids 必填")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeErr(w, http.StatusBadRequest, "title 必填")
		return
	}
	if req.Severity != "" && !db.ValidSeverity(strings.ToLower(strings.TrimSpace(req.Severity))) {
		writeErr(w, http.StatusBadRequest, "severity 取值必须是 critical|high|medium|low")
		return
	}
	c, err := s.m.pg.CreateFindingCase(req.TaskID, req.TaskID, req.Title, req.Reason, strings.ToLower(req.Severity), req.FindingIDs, "user")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) getFindingCase(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad case id")
		return
	}
	c, err := s.m.pg.GetFindingCase(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "finding case not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) updateFindingCaseReport(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad case id")
		return
	}
	var req struct {
		Report          string `json:"report"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := s.m.pg.UpdateFindingCaseReport(id, req.Report, req.ExpectedVersion)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) addFindingCaseMembers(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad case id")
		return
	}
	var req struct {
		FindingIDs []int64 `json:"finding_ids"`
		Reason     string  `json:"reason"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	added, err := s.m.pg.AddFindingCaseMembers(id, req.FindingIDs, req.Reason, "user")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added})
}

func (s *Server) removeFindingCaseMember(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	findingID := int64(atoiDefault(r.PathValue("finding_id"), 0))
	if id <= 0 || findingID <= 0 {
		writeErr(w, http.StatusBadRequest, "bad case/finding id")
		return
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	if err := s.m.pg.RemoveFindingCaseMember(id, findingID, reason, "user"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) findingCaseCandidates(w http.ResponseWriter, r *http.Request) {
	findingID := int64(atoiDefault(r.PathValue("id"), 0))
	taskID := int64(atoiDefault(r.URL.Query().Get("task_id"), 0))
	if findingID <= 0 || taskID <= 0 {
		writeErr(w, http.StatusBadRequest, "finding id 与 task_id 必填")
		return
	}
	items, err := s.m.pg.FindingCaseCandidates(taskID, findingID, 10)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if items == nil {
		items = []db.FindingCaseCandidate{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) findingCaseStats(w http.ResponseWriter, r *http.Request) {
	taskID := int64(atoiDefault(r.URL.Query().Get("task_id"), 0))
	if taskID <= 0 {
		writeErr(w, http.StatusBadRequest, "task_id 必填")
		return
	}
	st, err := s.m.pg.FindingCaseStats(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) listFindingCaseSuggestions(w http.ResponseWriter, r *http.Request) {
	taskID := int64(atoiDefault(r.URL.Query().Get("task_id"), 0))
	if taskID <= 0 {
		writeErr(w, http.StatusBadRequest, "task_id 必填")
		return
	}
	items, err := s.m.pg.ListFindingCaseSuggestions(taskID, r.URL.Query().Get("state"), 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []*db.FindingCaseSuggestion{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) generateFindingCaseSuggestions(w http.ResponseWriter, r *http.Request) {
	taskID := int64(atoiDefault(r.URL.Query().Get("task_id"), 0))
	if taskID <= 0 {
		writeErr(w, http.StatusBadRequest, "task_id 必填")
		return
	}
	created, err := s.m.pg.GenerateFindingCaseSuggestions(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created})
}

func (s *Server) acceptFindingCaseSuggestion(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad suggestion id")
		return
	}
	c, err := s.m.pg.AcceptFindingCaseSuggestion(id, "user")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) dismissFindingCaseSuggestion(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad suggestion id")
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = decode(r, &req)
	if err := s.m.pg.DismissFindingCaseSuggestion(id, req.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) blockFindingCasePair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID     int64   `json:"task_id"`
		FindingIDs []int64 `json:"finding_ids"`
		Reason     string  `json:"reason"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TaskID <= 0 || len(req.FindingIDs) != 2 {
		writeErr(w, http.StatusBadRequest, "task_id 与两条 finding_ids 必填")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeErr(w, http.StatusBadRequest, "reason 必填：说明为什么它们不是同一漏洞")
		return
	}
	if err := s.m.pg.BlockFindingCasePair(req.TaskID, req.FindingIDs[0], req.FindingIDs[1], req.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
