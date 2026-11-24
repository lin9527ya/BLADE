package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// 指纹打法库（全局）：列表 / 详情 / 启用-废弃。所有路由都在 registerRoutes 里挂载，
// 因此与其它 /api/* 一样走 requireAuth。
func (s *Server) registerPlaybookRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/playbooks", s.listPlaybooks)
	mux.HandleFunc("GET /api/playbooks/export", s.exportPlaybooks)
	mux.HandleFunc("POST /api/playbooks/import", s.importPlaybooks)
	mux.HandleFunc("POST /api/playbooks/import-md/preview", s.previewPlaybookMDImport)
	mux.HandleFunc("POST /api/playbooks/import-md/commit", s.commitPlaybookMDImport)
	mux.HandleFunc("GET /api/playbooks/{id}", s.getPlaybook)
	mux.HandleFunc("PATCH /api/playbooks/{id}", s.patchPlaybook)
	mux.HandleFunc("PUT /api/playbooks/{id}", s.updatePlaybook)
	mux.HandleFunc("POST /api/tasks/{id}/distill-playbook", s.distillTaskPlaybook) // 手动让打法沉淀 agent 复盘本任务
}

func (s *Server) listPlaybooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	list, total, err := s.m.Assets().ListPlaybooks(db.PlaybookFilter{
		Q: q.Get("q"), Status: q.Get("status"), Limit: limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"playbooks": list, "total": total})
}

func (s *Server) getPlaybook(w http.ResponseWriter, r *http.Request) {
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	p, err := s.m.Assets().GetPlaybook(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "打法不存在")
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) patchPlaybook(w http.ResponseWriter, r *http.Request) {
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
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
	if status == db.PlaybookStatusDeprecated && strings.TrimSpace(req.Reason) == "" {
		// 与公司知识/发现撤回同款纪律：撤下一张打法必须写清"为什么不能用了"。
		writeErr(w, 400, "废弃必须给出理由（后来人最需要的就是这句）")
		return
	}
	n, err := s.m.Assets().SetPlaybookStatus(id, status, strings.TrimSpace(req.Reason))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if n == 0 {
		writeErr(w, 404, "打法不存在")
		return
	}
	p, err := s.m.Assets().GetPlaybook(id)
	if err != nil || p == nil {
		writeErr(w, 500, "读取更新后的打法失败")
		return
	}
	writeJSON(w, 200, p)
}

// maxPlaybookBodyBytes 是人工编辑打法卡的上限：steps/evidence 可能较长，但不能无界。
const maxPlaybookBodyBytes = 2 << 20

// updatePlaybook 人工编辑卡片内容。状态仍走 PATCH（废弃/恢复）；这里不改统计与
// 「最近验证」时间——编辑文字不等于重新验证过。
func (s *Server) updatePlaybook(w http.ResponseWriter, r *http.Request) {
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPlaybookBodyBytes)
	var req struct {
		Fingerprint  db.PlaybookFingerprint `json:"fingerprint"`
		TechniqueKey string                 `json:"technique_key"`
		Title        string                 `json:"title"`
		Summary      string                 `json:"summary"`
		Steps        string                 `json:"steps"`
		Evidence     string                 `json:"evidence"`
		Confidence   string                 `json:"confidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	p, err := s.m.Assets().UpdatePlaybook(id, db.Playbook{
		Fingerprint: req.Fingerprint, TechniqueKey: req.TechniqueKey,
		Title: req.Title, Summary: req.Summary, Steps: req.Steps,
		Evidence: req.Evidence, Confidence: req.Confidence,
	})
	if errors.Is(err, db.ErrPlaybookConflict) {
		writeErr(w, 409, "同指纹+手法的打法卡已存在：请调整指纹或 technique_key")
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "打法不存在")
		return
	}
	writeJSON(w, 200, p)
}

// distillTaskPlaybook 手动触发一次「打法沉淀」agent 复盘本任务。
//
// 自动触发依赖 scheduler 的 goal/tool-call 水位：迁移前已达成的目标不会补发。
// 这个入口让操作者对已完成的任务补沉淀，也让“沉淀失败/漏了”有可重试的出口。
func (s *Server) distillTaskPlaybook(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, 409, "任务正在删除，无法沉淀打法")
		return
	}
	a, err := s.m.pg.GetAgentByKey("playbook")
	if err != nil || a == nil {
		writeErr(w, 409, "打法沉淀 agent 不存在（可能已被删除）")
		return
	}
	if !a.Enabled {
		writeErr(w, 409, "打法沉淀 agent 已停用，请在 Agent 管理里启用后再试")
		return
	}
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil {
		writeErr(w, 400, "bad task id")
		return
	}
	msg := playbookManualMessage + "\n\n（手动触发：即使目标尚未全部达成，也按已完成的部分判断；没有可复用的完整方法就说明“无新打法”。）"
	if !s.startTriggeredRunAs(principalFrom(r.Context()), "playbook", "手动沉淀 · task#"+t.ID, msg, taskID, true, t.Description, t.Goal) {
		writeErr(w, 503, "Agent 暂不可用，未加入触发队列")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "queued"})
}

const maxPlaybookMDRequestBytes = 512 << 10

type playbookMDPreviewReq struct {
	Filename     string `json:"filename"`
	Content      string `json:"content"`
	LLMProfileID *int64 `json:"llm_profile_id"`
}

type playbookMDCommitReq struct {
	Drafts []agent.PlaybookDraft `json:"drafts"`
}

// previewPlaybookMDImport turns an admin-supplied Markdown document into
// low-confidence card drafts. It deliberately does not write anything: the
// external document is untrusted and the model may summarize it imperfectly,
// so a human reviews the drafts before commit.
func (s *Server) previewPlaybookMDImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPlaybookMDRequestBytes)
	var req playbookMDPreviewReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "Markdown 文件过大（最多 512KB）")
		} else {
			writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		}
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeErr(w, http.StatusBadRequest, "Markdown 内容为空")
		return
	}
	if utf8.RuneCountInString(content) > 200000 {
		writeErr(w, http.StatusBadRequest, "Markdown 内容最多 20 万字")
		return
	}
	var prov llm.Provider
	if req.LLMProfileID != nil && *req.LLMProfileID > 0 {
		if p, _, ok := s.providerForProfile(*req.LLMProfileID); ok {
			prov = p
		}
	} else if active, err := s.m.pg.ActiveProfile(); err == nil && active != nil {
		if p, _, ok := s.providerForProfile(active.ID); ok {
			prov = p
		}
	}
	if prov == nil {
		writeErr(w, http.StatusServiceUnavailable, "没有可用的 LLM 配置：请先在「LLM 配置」中设置激活配置")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	drafts, warnings, err := agent.ExtractPlaybooksFromMarkdown(ctx, prov, strings.TrimSpace(req.Filename), content)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			writeErr(w, http.StatusGatewayTimeout, "模型总结超时，请缩短文档或稍后重试")
			return
		}
		writeErr(w, http.StatusBadGateway, "总结失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"drafts": drafts, "warnings": warnings})
}

// commitPlaybookMDImport writes only the drafts the human kept. It uses an
// insert-only path so importing a document can never overwrite an existing
// verified card, and imported cards always remain low-confidence.
func (s *Server) commitPlaybookMDImport(w http.ResponseWriter, r *http.Request) {
	as := s.m.Assets()
	if as == nil {
		writeErr(w, http.StatusServiceUnavailable, "资产库未启用")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var req playbookMDCommitReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "导入内容过大")
		} else {
			writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		}
		return
	}
	if len(req.Drafts) == 0 {
		writeErr(w, http.StatusBadRequest, "没有要导入的卡片")
		return
	}
	if len(req.Drafts) > 20 {
		writeErr(w, http.StatusBadRequest, "单次最多导入 20 张卡片")
		return
	}
	created, skipped, failed := 0, 0, 0
	var failures []string
	for _, d := range req.Drafts {
		_, inserted, err := as.CreateImportedPlaybook(db.Playbook{
			Title: d.Title, Summary: d.Summary, TechniqueKey: d.TechniqueKey,
			Steps: d.Steps, Evidence: d.Evidence, Confidence: "low",
		})
		if err != nil {
			failed++
			failures = append(failures, clipPlaybookImportLabel(d.Title)+": "+err.Error())
			continue
		}
		if inserted {
			created++
		} else {
			skipped++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created, "skipped": skipped, "failed": failed, "errors": failures,
	})
}

func clipPlaybookImportLabel(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}
