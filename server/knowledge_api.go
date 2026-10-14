package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// P23 公司级知识的 HTTP 面：
//
//	GET   /api/companies/{id}/knowledge        某公司的全部知识（含已废弃；UI 列表用）
//	PATCH /api/companies/{id}/knowledge/{kid}  废弃/恢复（人做的动作；Agent 工具只能沉淀不能改）
//	POST  /api/findings/{id}/promote-knowledge 从发现详情页一键沉淀（与工具同一条写入路径）
//
// 只读性：Agent 侧只有 promote_knowledge（新增），没有改/删的工具——废弃与恢复是人的动作。

func (s *Server) listCompanyKnowledge(w http.ResponseWriter, r *http.Request) {
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "bad company id")
		return
	}
	rows, err := s.m.Assets().ListCompanyKnowledgeAll(id, 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	active := 0
	for _, k := range rows {
		if k.Status == "active" {
			active++
		}
	}
	writeJSON(w, 200, map[string]any{"knowledge": rows, "active": active, "total": len(rows)})
}

func (s *Server) patchCompanyKnowledge(w http.ResponseWriter, r *http.Request) {
	cid := parseIntDefault(r.PathValue("id"))
	kid := parseIntDefault(r.PathValue("kid"))
	if cid <= 0 || kid <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	status := strings.ToLower(strings.TrimSpace(req.Status))
	if status == "deprecated" && strings.TrimSpace(req.Reason) == "" {
		// 与发现撤回同款纪律：撤下一条知识必须写清"为什么不算数了"。
		writeErr(w, 400, "废弃必须给出理由（后来人最需要的就是这句）")
		return
	}
	cur, err := s.m.Assets().GetCompanyKnowledge(kid)
	if err != nil || cur == nil {
		writeErr(w, 404, "知识条目不存在")
		return
	}
	if cur.CompanyID != cid {
		writeErr(w, 404, "知识条目不属于该公司")
		return
	}
	row, err := s.m.Assets().SetCompanyKnowledgeStatus(kid, status, strings.TrimSpace(req.Reason))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, row)
}

// promoteFindingKnowledge 从发现详情页沉淀：与工具同一条判定（图里 confirmed 的节点 +
// 公司必须是该任务关联的企业之一），只是入口不同、由人直接触发。
func (s *Server) promoteFindingKnowledge(w http.ResponseWriter, r *http.Request) {
	fid := parseIntDefault(r.PathValue("id"))
	if fid <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	f, err := s.m.pg.GetFinding(fid)
	if err != nil || f == nil {
		writeErr(w, 404, "发现不存在")
		return
	}
	if f.TaskID == nil {
		writeErr(w, 400, "该发现没有归属任务，无法沉淀（公司知识要求来源可回溯）")
		return
	}
	var req struct {
		CompanyID  int64  `json:"company_id"`
		Kind       string `json:"kind"`
		Summary    string `json:"summary"`
		Detail     string `json:"detail"`
		Confidence string `json:"confidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	t, ok := s.m.Task(strconv.FormatInt(*f.TaskID, 10))
	if !ok || t.Store == nil {
		writeErr(w, 404, "归属任务不存在或已归档")
		return
	}
	// 公司必须是该任务关联的企业之一（与工具同一条规则）。
	allowed := taskCompanyIDsOf(s, t)
	okCompany := false
	for _, id := range allowed {
		if id == req.CompanyID {
			okCompany = true
			break
		}
	}
	if !okCompany {
		if len(allowed) == 0 {
			writeErr(w, 400, "该任务没有关联任何企业：先在任务里关联企业（或把公司加进任务范围）再沉淀")
			return
		}
		writeErr(w, 400, fmt.Sprintf("company_id %d 不是该任务关联的企业（可用：%v）——知识写错公司会误导后来人", req.CompanyID, allowed))
		return
	}
	// 只沉淀已确认的结论：与 promote_knowledge 工具同一条判定（唯一规则，两条入口共用）。
	if f.NodeID == nil {
		writeErr(w, 400, "该发现没有对应图节点，无法沉淀")
		return
	}
	node, err := t.Store.GetNode(*f.NodeID)
	if err != nil || node == nil {
		writeErr(w, 404, "发现对应的图节点不存在")
		return
	}
	if node.State != "confirmed" {
		writeErr(w, 400, fmt.Sprintf("该发现当前状态是 %q：只沉淀已确认（confirmed）的结论——被推翻/排除/待补证的结论沉淀出去会误导后来人", node.State))
		return
	}
	// triage 状态（撤回/误报/排除/重复）只翻 findings 行、不翻图节点，必须单独判一次。
	if err := db.FindingPromotableForKnowledge(f.Status); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if k := db.NormalizeKnowledgeKind(req.Kind); req.Kind != "" && k == "" {
		writeErr(w, 400, "未知的 kind（可用："+strings.Join(db.KnowledgeKinds, " | ")+"）")
		return
	} else if k != "" {
		req.Kind = k
	}
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = strings.TrimSpace(f.Summary)
	}
	if summary == "" {
		summary = strings.TrimSpace(f.Name)
	}
	if summary == "" && f.VulnClass != "" {
		summary = f.VulnClass
	}
	if summary == "" {
		writeErr(w, 400, "摘要不能为空")
		return
	}
	conf := strings.TrimSpace(req.Confidence)
	if conf == "" {
		conf = f.Confidence // 继承发现自己的置信度标定（没标就是空，不猜）
	}
	saved, err := s.m.Assets().CreateCompanyKnowledge(db.CompanyKnowledge{
		CompanyID: req.CompanyID, Kind: req.Kind, Summary: summary, Detail: strings.TrimSpace(req.Detail),
		SourceTaskID: strconv.FormatInt(*f.TaskID, 10), SourceNodeID: *f.NodeID, Confidence: conf,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, saved)
}

// taskCompanyIDsOf 解析一个任务关联的公司 id（与 agent 侧 taskCompanyIDs 同规则：
// task_scope 里的 company 行）。
func taskCompanyIDsOf(s *Server, t *Task) []int64 {
	if t == nil || s.m.Assets() == nil {
		return nil
	}
	rows, err := s.m.Assets().ListTaskScope(parseIntDefault(t.ID))
	if err != nil {
		return nil
	}
	seen := map[int64]bool{}
	var out []int64
	for _, r := range rows {
		if r.Kind == "company" && r.CompanyID != nil && *r.CompanyID > 0 && !seen[*r.CompanyID] {
			seen[*r.CompanyID] = true
			out = append(out, *r.CompanyID)
		}
	}
	return out
}
