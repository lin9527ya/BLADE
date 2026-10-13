package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// P18 约束可执行化（HTTP 面）：
//
//	POST /api/tasks/{id}/constraints/{cid}/compile  草稿 + 命中预览（只读，不落库）
//	POST /api/tasks/{id}/constraints/{cid}/apply    真的落库：deny → 任务级拦截规则；
//	                                                allow → 本任务授权范围（RoE 允许面）
//
// 编译是确定性的（intercept.CompileConstraint）：只抽目标与已知工具名。预览拿本任务
// **真实发生过的调用**当样本，回答"这条规则会命中什么、命中几次"，让人在落库前核对。

// maxTaskRulesPerConstraint 是单条约束能编译出的规则上限（防止一条长文本炸出几百条规则）。
const maxTaskRulesPerConstraint = 20

type compilePreview struct {
	Pattern string   `json:"pattern"`
	Matched int      `json:"matched"`
	Total   int      `json:"total"`
	Samples []string `json:"samples"`
}

type compileResponse struct {
	intercept.ConstraintDraft
	Preview []compilePreview   `json:"preview"`
	Applied []db.InterceptRule `json:"applied,omitempty"`
	Scopes  []db.TaskScope     `json:"scopes,omitempty"`
}

func (s *Server) compileConstraint(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	cid, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil || cid <= 0 {
		writeErr(w, 400, "bad constraint id")
		return
	}
	rows, err := t.Store.ListConstraints()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var constraint *db.Constraint
	for i := range rows {
		if rows[i].ID == cid {
			constraint = &rows[i]
			break
		}
	}
	if constraint == nil {
		writeErr(w, 404, "约束不存在")
		return
	}
	draft := intercept.CompileConstraint(cid, constraint.Kind, constraint.Text)
	writeJSON(w, 200, compileResponse{ConstraintDraft: draft, Preview: previewDraft(t.Store, draft)})
}

// previewDraft 拿本任务最近真实发生过的工具调用（活动日志里每一次调用都有），统计每条
// 草案规则会命中多少条——让人在落库前就能看出"这条约束编译出来会不会误伤/能不能拦住"。
func previewDraft(st *db.ExplorationStore, draft intercept.ConstraintDraft) []compilePreview {
	if len(draft.Rules) == 0 || st == nil {
		return []compilePreview{}
	}
	samples, err := st.RecentToolUseInputs(100)
	if err != nil || len(samples) == 0 {
		return []compilePreview{}
	}
	out := make([]compilePreview, 0, len(draft.Rules))
	for _, rule := range draft.Rules {
		p := compilePreview{Pattern: rule.Pattern, Total: len(samples), Samples: []string{}}
		for _, sm := range samples {
			matched, text, _, _, err := intercept.PreviewRule(sm.Tool, []byte(sm.Input),
				rule.MatchTarget, rule.MatchType, rule.Pattern, rule.Action, rule.Name, rule.Message)
			if err != nil || !matched {
				continue
			}
			p.Matched++
			if len(p.Samples) < 3 {
				p.Samples = append(p.Samples, clipRunesLocal(text, 120))
			}
		}
		out = append(out, p)
	}
	return out
}

func clipRunesLocal(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// applyConstraint 把编译结果落库。规则/范围都属于这个任务：规则带 task_id，匹配时
// 只在本任务内生效；allow 约束进 RoE 允许面。重复应用 = 以当前约束原文为准重建
// （先删该约束生成的旧规则再插新的），所以改完约束再点一次就能收敛。
func (s *Server) applyConstraint(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	tid, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || tid <= 0 {
		writeErr(w, 500, "bad task id")
		return
	}
	cid, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil || cid <= 0 {
		writeErr(w, 400, "bad constraint id")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法应用约束")
		return
	}
	defer s.engine.decInflight(t.ID)

	rows, err := t.Store.ListConstraints()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var constraint *db.Constraint
	for i := range rows {
		if rows[i].ID == cid {
			constraint = &rows[i]
			break
		}
	}
	if constraint == nil {
		writeErr(w, 404, "约束不存在")
		return
	}
	// 服务端重新编译：不信任客户端回传的草案。
	draft := intercept.CompileConstraint(cid, constraint.Kind, constraint.Text)
	if len(draft.Rules) > maxTaskRulesPerConstraint {
		draft.Rules = draft.Rules[:maxTaskRulesPerConstraint]
		draft.Note += "（超出单条上限，已截断）"
	}

	resp := compileResponse{ConstraintDraft: draft}
	if existing, err := s.m.pg.CountTaskRules(t.ID); err == nil {
		if existing+len(draft.Rules) > maxInterceptRules {
			writeErr(w, 400, "本任务的任务级规则已达上限，请先清理（删除约束或对应规则）")
			return
		}
	}
	// 以当前约束原文为准重建：先清掉这条约束此前生成的规则。
	if _, err := s.m.pg.DeleteTaskRulesByConstraint(t.ID, cid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, rule := range draft.Rules {
		created, err := s.m.pg.CreateTaskRule(db.InterceptRule{
			Name: rule.Name, Enabled: true, Priority: rule.Priority,
			MatchTarget: rule.MatchTarget, MatchType: rule.MatchType, Pattern: rule.Pattern,
			Action: rule.Action, Message: rule.Message,
			TimeoutEnabled: false, TimeoutSeconds: 0, TimeoutAction: "deny",
			TaskID:             t.ID,
			SourceConstraintID: &cid,
		})
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		resp.Applied = append(resp.Applied, created)
	}
	for _, sc := range draft.Scope {
		// source=manual：这条路是人从任务页点"应用"走的，与手工加范围同一语义
		// （task_scope.source 只接受 auto|agent|manual）。
		added, err := s.m.Assets().AddAgentScope(tid, sc.Kind, sc.Value, sc.Note, "manual")
		if err != nil {
			// 单个目标加不进去（例如根域解析失败）不该让整次应用失败：报告出来即可。
			resp.Note += "；有目标未能加入授权范围：" + sc.Value + "（" + err.Error() + "）"
			continue
		}
		resp.Scopes = append(resp.Scopes, added)
	}
	// 规则缓存必须失效，否则新规则要等到下次重载才生效。
	s.m.interceptor.Invalidate()
	writeJSON(w, 200, resp)
}

// dropConstraintRules 删除某条约束编译出的规则（约束被删/被改写时调用），并失效规则缓存。
func (s *Server) dropConstraintRules(taskID string, constraintID int64) int64 {
	n, err := s.m.pg.DeleteTaskRulesByConstraint(taskID, constraintID)
	if err == nil && n > 0 {
		s.m.interceptor.Invalidate()
	}
	return n
}
