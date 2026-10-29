package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// planner 模板库：多套 planner 正文（外网/内网/代码审计），任务创建时快照。
// 试渲染校验与 agent/prompt.go 的 renderTmpl 同模式（missingkey=error），
// 引用了目录外变量的正文在写入时就被拒，而不是等到任务启动才静默回退。

const maxPlannerTemplateRequestBytes = 64 << 10

var plannerTemplateVarsHint = "{{.Goal}} {{.Scope}} {{.AssetSummary}} {{.DataDir}} {{.Now}}"

type plannerTemplateReq struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

func decodePlannerTemplateRequest(w http.ResponseWriter, r *http.Request, req *plannerTemplateReq) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPlannerTemplateRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return false
	}
	return true
}

// validatePlannerTemplateBody parses and试渲染s the body with a full PlannerVars
// sample so out-of-catalog variables fail here.
func validatePlannerTemplateBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("模板正文不能为空")
	}
	if len(body) > db.MaxPlannerTemplateBodyBytes {
		return fmt.Errorf("模板正文最多 %d 字节（32KB），当前 %d 字节", db.MaxPlannerTemplateBodyBytes, len(body))
	}
	t, err := template.New("p").Option("missingkey=error").Parse(body)
	if err != nil {
		return fmt.Errorf("模板语法错误：%w", err)
	}
	vars := agent.PlannerVars{
		Goal: "拿下 example.com 后台权限", Scope: "example.com",
		AssetSummary: "站点 https://example.com", DataDir: "/tmp/blade-validate", Now: "2026-10-29 12:00",
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return fmt.Errorf("试渲染失败（可能引用了目录外变量；合法变量：%s）：%w", plannerTemplateVarsHint, err)
	}
	return nil
}

func writePlannerTemplateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrPlannerTemplateNotFound):
		writeErr(w, 404, "planner template not found")
	case errors.Is(err, db.ErrPlannerTemplateNameConflict):
		writeErr(w, 409, "模板名称已存在")
	case errors.Is(err, db.ErrPlannerTemplateDefaultLocked):
		writeErr(w, 409, "默认模板不能删除，请先把默认让给别的模板")
	case errors.Is(err, db.ErrPlannerTemplateInvalid):
		writeErr(w, 400, err.Error())
	default:
		writeErr(w, 500, err.Error())
	}
}

func (s *Server) pgListPlannerTemplates(w http.ResponseWriter, _ *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	items, err := pg.ListPlannerTemplates()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"templates": items})
}

func (s *Server) pgCreatePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req plannerTemplateReq
	if !decodePlannerTemplateRequest(w, r, &req) {
		return
	}
	if err := validatePlannerTemplateBody(req.Body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	item, err := pg.CreatePlannerTemplate(req.Name, req.Body)
	if err != nil {
		writePlannerTemplateErr(w, err)
		return
	}
	writeJSON(w, 201, item)
}

func (s *Server) pgUpdatePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	var req plannerTemplateReq
	if !decodePlannerTemplateRequest(w, r, &req) {
		return
	}
	if err := validatePlannerTemplateBody(req.Body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	item, err := pg.UpdatePlannerTemplate(id, req.Name, req.Body)
	if err != nil {
		writePlannerTemplateErr(w, err)
		return
	}
	writeJSON(w, 200, item)
}

func (s *Server) pgDeletePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	if err := pg.DeletePlannerTemplate(id); err != nil {
		writePlannerTemplateErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

func (s *Server) pgSetDefaultPlannerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	item, err := pg.SetDefaultPlannerTemplate(id)
	if err != nil {
		writePlannerTemplateErr(w, err)
		return
	}
	writeJSON(w, 200, item)
}

// pgValidatePlannerTemplate dry-runs a body without saving it.
func (s *Server) pgValidatePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	var req plannerTemplateReq
	if !decodePlannerTemplateRequest(w, r, &req) {
		return
	}
	if err := validatePlannerTemplateBody(req.Body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// snapshotPlannerPrompt resolves the task's planner prompt at creation time.
// An explicit body wins over a template id; both are snapshotted so later
// template edits cannot change a running task.
func (s *Server) snapshotPlannerPrompt(explicitBody string, templateID int64) (string, error) {
	if body := strings.TrimSpace(explicitBody); body != "" {
		if err := validatePlannerTemplateBody(body); err != nil {
			return "", err
		}
		return body, nil
	}
	if templateID <= 0 {
		return "", nil
	}
	tpl, err := s.m.pg.GetPlannerTemplate(templateID)
	if err != nil {
		return "", err
	}
	if tpl == nil {
		return "", fmt.Errorf("planner 模板 #%d 不存在", templateID)
	}
	if err := validatePlannerTemplateBody(tpl.Body); err != nil {
		return "", fmt.Errorf("planner 模板 #%d 不可用：%w", templateID, err)
	}
	return tpl.Body, nil
}
