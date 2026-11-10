package server

import (
	"net/http"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// 漏洞案例（P2-7）：把同一根因/同一资产的重复上报归并成一个独立缺陷。
// 原始 findings 保留为成员；案例持有统一报告与版本（按实际读取的 version 做 CAS，
// report_version 记录保存时的依据版本）。
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

// listFindingCaseRows 是「按漏洞」视图的数据源：案例文件夹 + 未归并发现混排。
// 与全局发现页共用 FindingFilter（severity/status/task/query/sort/asset_scope），
// 默认排除 ruled_out / open_proof_gap 候选闭合，只展示真实漏洞。
func (s *Server) listFindingCaseRows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := findingPaginationParam(q.Get("page"), 1, 0)
	size := findingPaginationParam(q.Get("limit"), 20, 200)
	rows, total, err := s.m.pg.ListFindingCaseRows(findingFilterFromQuery(q), page, size)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// DBFinding 没有 JSON 标签（内部结构），这里统一转 FindingDTO，前端才能按
	// id/name/status/ts 消费；案例成员报告正文不在列表里返回。
	var findings []*db.DBFinding
	for _, row := range rows {
		if row.Finding != nil {
			findings = append(findings, row.Finding)
		}
	}
	assets := s.resolveFindingAssets(findings)
	items := make([]findingCaseRowDTO, 0, len(rows))
	for _, row := range rows {
		dto := findingCaseRowDTO{MatchedIDs: row.MatchedIDs}
		dto.Case = row.Case
		if row.Finding != nil {
			f := findingFromDB(row.Finding, assets)
			dto.Finding = &f
		}
		items = append(items, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": size,
	})
}

type findingCaseRowDTO struct {
	Case       *db.FindingCase `json:"case,omitempty"`
	Finding    *FindingDTO     `json:"finding,omitempty"`
	MatchedIDs []int64         `json:"matched_ids,omitempty"`
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

type findingCaseDTO struct {
	db.FindingCase
	Members []FindingDTO `json:"members,omitempty"`
}

// findingCaseDTOWithMembers 把案例成员从内部 DBFinding 转成前端 FindingDTO
// （id/name/status/ts 等小写字段）。案例详情对话框和「按漏洞」成员列表共用。
func (s *Server) findingCaseDTOWithMembers(c *db.FindingCase) *findingCaseDTO {
	if c == nil {
		return nil
	}
	d := &findingCaseDTO{FindingCase: *c}
	d.FindingCase.Members = nil // 外层 Members 用 DTO 形态，避免内部结构把字段盖掉
	if len(c.Members) == 0 {
		return d
	}
	assets := s.resolveFindingAssets(c.Members)
	d.Members = make([]FindingDTO, 0, len(c.Members))
	for _, m := range c.Members {
		d.Members = append(d.Members, findingFromDB(m, assets))
	}
	return d
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
	writeJSON(w, http.StatusOK, s.findingCaseDTOWithMembers(c))
}

func (s *Server) updateFindingCaseReport(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad case id")
		return
	}
	var req struct {
		Report          string `json:"report"`
		Title           string `json:"title"`
		Severity        string `json:"severity"`
		SeverityReason  string `json:"severity_reason"`
		Version         int64  `json:"version"`
		ExpectedVersion int64  `json:"expected_version"` // 旧客户端：此前实际是 report_version
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	version := req.Version
	if version <= 0 {
		version = req.ExpectedVersion
	}
	c, err := s.m.pg.UpdateFindingCaseReport(r.Context(), id, version, req.Title, req.Report, req.Severity, req.SeverityReason)
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
