package server

import (
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"github.com/lin9527ya/Norma/skill"
	"github.com/lin9527ya/blade/agent"
)

type previewSkill struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Text  string `json:"text,omitempty"`
}
type observedModel struct {
	Model   string    `json:"model"`
	Profile string    `json:"profile"`
	Status  string    `json:"status"`
	At      time.Time `json:"at"`
}

func (s *Server) taskModelPreview(w http.ResponseWriter, r *http.Request) {
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "任务不存在")
		return
	}
	role := r.URL.Query().Get("role")
	if role != "mainagent" && role != "worker" && role != "planner" {
		writeErr(w, 400, "无效角色")
		return
	}
	pg := s.m.pg
	expected, err := s.resolveTaskRoleLLM(task, role)
	if err != nil {
		writeErr(w, 500, "解析模型失败")
		return
	}
	a, err := pg.GetAgentByKey(role)
	if err != nil || a == nil {
		writeErr(w, 500, "读取角色失败")
		return
	}
	names, err := pg.AgentSkillNames(a.ID)
	if err != nil {
		writeErr(w, 500, "读取技能可见性失败")
		return
	}
	all, err := skill.LoadDir(s.skillDir)
	if err != nil {
		writeErr(w, 500, "无法读取技能目录")
		return
	}
	entries := map[string]previewSkill{}
	for _, name := range names {
		entries[name] = previewSkill{Name: name, State: "absent"}
	}
	if all != nil {
		for _, item := range all.List() {
			name := filepath.Base(item.Dir)
			entries[name] = previewSkill{Name: name, State: "absent"}
		}
		if reg := visibleSkillRegistry(all, names); reg != nil {
			for _, item := range reg.List() {
				d := indexSkillDescription(item.Description, item.WhenToUse)
				state := "name_only"
				if d != "" {
					state = "described"
				}
				entries[filepath.Base(item.Dir)] = previewSkill{Name: item.Name, State: state, Text: d}
			}
		}
	}
	list := make([]previewSkill, 0, len(entries))
	for _, v := range entries {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	tmpl, err := pg.CurrentPrompt(a.ID)
	if err != nil {
		writeErr(w, 500, "读取提示词失败")
		return
	}
	src := "角色模板"
	if tmpl == "" {
		tmpl = agent.BuiltinPromptSeeds()[role]
		src = "内置模板"
	}
	vars, err := pg.PromptVars(a.ID)
	if err != nil {
		writeErr(w, 500, "读取模板变量失败")
		return
	}
	rendered, renderErr := renderPrompt(tmpl, withGlobalVars(vars), nil)
	appendText, err := pg.CurrentPromptAppend(a.ID)
	if err != nil {
		writeErr(w, 500, "读取追加提示失败")
		return
	}
	if appendText != "" {
		rendered += "\n\n" + appendText
	}
	var last observedModel
	err = pg.QueryRowContext(r.Context(), `SELECT coalesce(model,''),coalesce(profile_name,''),status,ts FROM llm_usage
 WHERE task_id=$1 AND (worker=$2 OR ($2='mainagent' AND worker='main')) ORDER BY ts DESC,id DESC LIMIT 1`, task.ID, role).Scan(&last.Model, &last.Profile, &last.Status, &last.At)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 500, "读取调用记录失败")
		return
	}
	out := map[string]any{"expected": expected, "skills": list, "prompt": rendered, "prompt_source": src}
	if err == nil {
		out["last_used"] = last
	}
	if renderErr != nil {
		out["prompt_error"] = renderErr.Error()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
