package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// cloneTask creates a new task from an existing task's configuration. Runtime
// state (status, intents, findings, traffic) is deliberately not copied; only
// the reproducible setup is.
func (s *Server) cloneTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	t, err := s.m.PG().GetTask(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	rules, err := s.m.PG().Assets().ListTaskInterceptRules(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	inputs := make([]db.TaskInterceptRuleInput, 0, len(rules))
	for _, rule := range rules {
		inputs = append(inputs, db.TaskInterceptRuleInput{
			Enabled: rule.Enabled,
			Action:  rule.Action,
			Kind:    rule.Kind,
			Pattern: rule.Pattern,
			Note:    rule.Note,
		})
	}
	name := strings.TrimSpace(t.Name)
	if name != "" {
		name += "（副本）"
	}
	coverage := t.CoverageEnabled
	clone, err := s.m.CreateTaskWithOptions(t.Description, t.Goal, db.TaskCreateOptions{
		Name:                 name,
		CategoryID:           t.CategoryID,
		CompanyIDs:           t.CompanyIDs,
		LLMProfileIDs:        t.LLMProfileIDs,
		TimeoutSeconds:       t.TimeoutSeconds,
		PlanHeartbeatSeconds: t.PlanHeartbeatSeconds,
		CoverageEnabled:      &coverage,
		InterceptRules:       inputs,
		WorkerCount:          t.WorkerCount,
		Remark:               t.Remark,
		AssetScopePolicy:     t.AssetScopePolicy,
	})
	if err != nil {
		if errors.Is(err, db.ErrTaskCategoryInvalid) || errors.Is(err, db.ErrTaskCategoryNotFound) {
			writeErr(w, 400, "任务分类不存在或无效")
			return
		}
		if errors.Is(err, db.ErrTaskCompanyIDsInvalid) || errors.Is(err, db.ErrTaskCompanyNotFound) {
			writeErr(w, 400, "关联企业不存在或无效")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, taskDTO(clone, s.resolvedTaskStatus(clone)))
}

// taskToTemplate snapshots a task's setup into the reusable template library.
func (s *Server) taskToTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	t, err := s.m.PG().GetTask(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	rules, err := s.m.PG().Assets().ListTaskInterceptRules(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	inputs := make([]db.TaskInterceptRuleInput, 0, len(rules))
	for _, rule := range rules {
		inputs = append(inputs, db.TaskInterceptRuleInput{
			Enabled: rule.Enabled,
			Action:  rule.Action,
			Kind:    rule.Kind,
			Pattern: rule.Pattern,
			Note:    rule.Note,
		})
	}
	name := strings.TrimSpace(t.Name)
	if name == "" {
		name = "任务 #" + strconv.FormatInt(id, 10) + " 模板"
	} else {
		name += " 模板"
	}
	template, err := s.m.PG().CreateTaskTemplate(db.TaskTemplateInput{
		Name:           name,
		Description:    t.Description,
		Goal:           t.Goal,
		CategoryID:     t.CategoryID,
		InterceptRules: inputs,
	})
	if err != nil {
		writeTaskTemplateErr(w, err)
		return
	}
	writeJSON(w, 201, template)
}

type TaskCompareRow struct {
	Task          TaskDTO `json:"task"`
	AssetsTotal   int     `json:"assets_total"`
	FindingsTotal int     `json:"findings_total"`
}

// compareTasks returns a side-by-side summary. It intentionally reports only
// bounded aggregates (no reports/findings bodies) so it stays cheap even for
// large tasks.
func (s *Server) compareTasks(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("ids"))
	if raw == "" {
		writeErr(w, 400, "ids required")
		return
	}
	parts := strings.Split(raw, ",")
	if len(parts) < 2 || len(parts) > 10 {
		writeErr(w, 400, "对比需要 2–10 个任务")
		return
	}
	seen := map[string]bool{}
	rows := make([]TaskCompareRow, 0, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		t, ok := s.m.Task(id)
		if !ok {
			continue
		}
		dto := s.taskDTOsFor(r.Context(), []*Task{t})
		if len(dto) == 0 {
			continue
		}
		numericID, _ := strconv.ParseInt(t.ID, 10, 64)
		counts, _ := s.m.PG().Assets().CountsByTypeForTask(numericID)
		assetsTotal := 0
		for _, n := range counts {
			assetsTotal += n
		}
		findingsTotal := dto[0].Findings.Critical + dto[0].Findings.High + dto[0].Findings.Medium + dto[0].Findings.Low
		rows = append(rows, TaskCompareRow{Task: dto[0], AssetsTotal: assetsTotal, FindingsTotal: findingsTotal})
	}
	if len(rows) == 0 {
		writeErr(w, 404, "没有可对比的任务")
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}
