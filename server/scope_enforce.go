package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/intercept"
)

// 停机开关与作业时间窗的**执行侧**（P35）。
//
// 拦截层（intercept.CheckScope）管的是"这一次调用放不放行"；这里管的是"整个平台还该不该跑"。
// 两层都要有，因为它们拦的不是同一件事：一个 worker 可能正卡在一轮 LLM 请求里，
// 它的下一次工具调用会被拒（拦截层管得住），但它仍然占着并发槽、仍在花 token、仍在推进——
// 所以到点/停机时必须把**任务本身**停下来（暂停 + 取消在跑的 run/长任务）。
//
// 三层一起才算"停住了"：
//   ① 任务层：暂停所有在跑任务（本文件），排队任务保持排队但不再补位；
//   ② 入口层：禁止新任务开始（reconcileConcurrency 早退）；
//   ③ 调用层：拦截层拒绝一切动作类工具（intercept/killswitch.go）。
//
// 恢复一律**手动**：停机与窗口关闭都是一次明确的停止，自动恢复会把"停"变成"稍后自己动"。

// settingScopeWindowState 存窗口执行状态（当前在不在窗口内、上次关闭/开启的时间、因窗口暂停了几条）。
// 存下来是为了让页面能回答"为什么都停了"——只写日志的话，人得去翻日志才知道。
const settingScopeWindowState = "scope_window_state"

// scopeWindowState 是窗口执行状态的快照。
type scopeWindowState struct {
	InWindow     bool   `json:"in_window"`
	Window       string `json:"window"` // 原样回显用户配的窗口串（空 = 不限时）
	At           string `json:"at"`
	PausedNow    int    `json:"paused_now"`   // 本次动作暂停了几条
	PausedTotal  int    `json:"paused_total"` // 因窗口而暂停、当前仍未恢复的累计
	LastClosedAt string `json:"last_closed_at,omitempty"`
	LastOpenedAt string `json:"last_opened_at,omitempty"`
	// ParseError 非空 = 窗口设置解析不了。此时**不暂停**（配置写错不该让全平台停工），
	// 但要在页面上醒目地说出来：拦截层已经对每次调用转人工，那一条条待审批会很难查。
	ParseError string `json:"parse_error,omitempty"`
}

func readScopeWindowState(d *db.DB) scopeWindowState {
	var st scopeWindowState
	if d == nil {
		return st
	}
	raw, ok, err := d.GetSetting(settingScopeWindowState)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return st
	}
	_ = json.Unmarshal([]byte(raw), &st)
	return st
}

func writeScopeWindowState(d *db.DB, st scopeWindowState) {
	if d == nil {
		return
	}
	if b, err := json.Marshal(st); err == nil {
		_ = d.SetSetting(settingScopeWindowState, string(b))
	}
}

// safetyStop 回答"现在该不该停"，并给出该用的取消原因与一句人话理由。
// cause 放在最后返回（staticcheck ST1008：error 一律最后）——这个签名由 lint 定，不是审美。
//
// 停机开关优先于窗口：两条同时成立时，理由要说是停机（那是人的直接动作），
// 而不是"到点了"——事后查"为什么停的"时，这两个答案指向完全不同的处置。
func (s *Server) safetyStop() (blocked bool, reason string, cause error) {
	if s.m == nil || s.m.pg == nil {
		return false, "", nil
	}
	if on, why := interceptForcedOrStored(s.m.pg); on {
		return true, "停机开关已启用（" + why + "）", agent.AbortKillSwitch
	}
	raw, _, _ := s.m.pg.GetSetting(intercept.SettingScopeHours)
	in, err := intercept.WindowAllows(raw, time.Now())
	if err != nil {
		return false, "", nil // 解析不了：不因配置写错而全平台停工（拦截层已转人工），页面会提示
	}
	if !in {
		return true, "已离开授权作业时间窗（允许 " + strings.TrimSpace(raw) + "）", agent.AbortScopeWindowClosed
	}
	return false, "", nil
}

// interceptForcedOrStored 读开关状态（环境变量或库设置），与拦截层同一份判定。
func interceptForcedOrStored(d *db.DB) (bool, string) {
	if intercept.KillSwitchForcedByEnv() {
		return true, "环境变量 " + intercept.EnvKillSwitch + " 强制停机"
	}
	st := intercept.ReadKillSwitch(d)
	if !st.On {
		return false, ""
	}
	if st.Reason != "" {
		return true, st.Reason
	}
	return true, "未填写理由"
}

// enforceScopeEnforcement 是 scheduler tick 的一步：到点/停机就把该停的停掉，并把状态记下来。
//
// 每一步都**幂等**：已经在暂停的任务不会被再暂停一次（applyTaskControl 会拒绝），
// 所以每 5 秒跑一遍不会重复记账；状态设置里只在"边沿"（进/出窗口）时更新关闭/开启时间。
func (s *Server) enforceScopeEnforcement() {
	if s.m == nil || s.m.pg == nil {
		return
	}
	raw, _, _ := s.m.pg.GetSetting(intercept.SettingScopeHours)
	raw = strings.TrimSpace(raw)
	now := time.Now()

	st := readScopeWindowState(s.m.pg)
	st.Window = raw
	st.At = now.Format(time.RFC3339)

	inWindow, err := intercept.WindowAllows(raw, now)
	st.ParseError = ""
	if err != nil {
		st.ParseError = err.Error()
	}
	wasIn := st.InWindow
	st.InWindow = inWindow && err == nil

	blocked, reason, cause := s.safetyStop()
	if !blocked {
		if err == nil && !wasIn && inWindow {
			// 窗口刚开：**不自动恢复**，只记一笔让页面能提示"可以继续了"。
			st.LastOpenedAt = st.At
			log.Printf("[scope] 已进入授权作业时间窗（%s）：暂停中的任务需要手动继续", raw)
		}
		writeScopeWindowState(s.m.pg, st)
		return
	}

	if err == nil && wasIn && !inWindow {
		st.LastClosedAt = st.At
	}
	paused, errs := s.pauseRunningForSafety(cause, reason)
	if paused > 0 {
		st.PausedNow = paused
		st.PausedTotal += paused
		log.Printf("[scope] %s：已暂停 %d 个在跑任务", reason, paused)
	}
	for _, e := range errs {
		log.Printf("[scope] 暂停失败：%s", e)
	}
	writeScopeWindowState(s.m.pg, st)
}

// pauseRunningForSafety 暂停所有**在跑**的非终态任务（排队任务不动：它们没在跑，
// 而且暂停会丢掉队列位置；入口层已经不让它们补位了）。
//
// 走的是与人工暂停**同一条**路径（applyTaskControlWithCause）：引擎取消在跑的 run 与长任务、
// 落库 paused、取消本轮主 Agent 对话——"停"只有一种实现，系统停与人工停不该有两套行为。
func (s *Server) pauseRunningForSafety(cause error, reason string) (int, []string) {
	if cause == nil {
		cause = agent.AbortKillSwitch
	}
	paused := 0
	var errs []string
	for _, t := range s.m.List() {
		lc := t.lifecycleSnapshot()
		if isTerminalStatus(lc.Status) || lc.Paused || lc.Queued || s.engine.IsDeleting(t.ID) {
			continue
		}
		if _, err := s.applyTaskControlWithCause(t, "pause", cause); err != nil {
			errs = append(errs, "task#"+t.ID+"："+err.Error())
			continue
		}
		// 任务时间线上留一句为什么停的——恢复之后回头看，这一行是唯一能解释中断的东西。
		s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text", Summary: reason})
		paused++
	}
	return paused, errs
}

// resumeAllPaused 继续所有暂停中的非终态任务（停机解除 / 窗口重开之后的那一下手动恢复）。
// 逐条走人工恢复的同一条路径；单条失败不连坐，失败的如实回报。
func (s *Server) resumeAllPaused() (int, []string) {
	resumed := 0
	var errs []string
	for _, t := range s.m.List() {
		lc := t.lifecycleSnapshot()
		if isTerminalStatus(lc.Status) || !lc.Paused {
			continue
		}
		if _, err := s.applyTaskControlWithCause(t, "resume", nil); err != nil {
			errs = append(errs, "task#"+t.ID+"："+err.Error())
			continue
		}
		resumed++
	}
	return resumed, errs
}

// safetyAdmissionBlocked 是入口层的判据（reconcileConcurrency 每 tick 问一次）。
func (s *Server) safetyAdmissionBlocked() bool {
	blocked, _, _ := s.safetyStop()
	return blocked
}

// ---------- HTTP ----------

// scopeEnforcementResponse 是 RoE 执行状态（页面用）。
type scopeEnforcementResponse struct {
	KillSwitch  intercept.KillSwitchReceipt `json:"kill_switch"`
	ForcedByEnv bool                        `json:"kill_switch_forced_by_env"`
	Window      scopeWindowState            `json:"window"`
	PausedTasks int                         `json:"paused_tasks"`
}

func (s *Server) scopeEnforcementStatus(w http.ResponseWriter, r *http.Request) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	paused := 0
	for _, t := range s.m.List() {
		lc := t.lifecycleSnapshot()
		if !isTerminalStatus(lc.Status) && lc.Paused {
			paused++
		}
	}
	writeJSON(w, 200, scopeEnforcementResponse{
		KillSwitch:  intercept.ReadKillSwitch(s.m.pg),
		ForcedByEnv: intercept.KillSwitchForcedByEnv(),
		Window:      readScopeWindowState(s.m.pg),
		PausedTasks: paused,
	})
}

// setKillSwitch 是停机开关的动作入口：开 = 立刻停（不等下一个 tick），关 = 解除但**不自动恢复**。
//
// by 记的是请求来源地址：本平台是单操作者模式（一个口令），没有"用户名"可记；
// 记下"从哪儿发起的"已经够回答回执里那个问题（"这是谁停的"）。
func (s *Server) setKillSwitch(w http.ResponseWriter, r *http.Request) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req struct {
		On     bool   `json:"on"`
		Reason string `json:"reason"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	by := "operator@" + clientAddr(r)
	reason := strings.TrimSpace(req.Reason)

	cur := intercept.ReadKillSwitch(s.m.pg)
	if req.On && cur.On && !intercept.KillSwitchForcedByEnv() {
		writeErr(w, 400, "停机开关已经是启用状态")
		return
	}
	if !req.On && !cur.On {
		writeErr(w, 400, "停机开关本来就没启用")
		return
	}

	paused := 0
	var errs []string
	if req.On {
		paused, errs = s.pauseRunningForSafety(agent.AbortKillSwitch, "停机开关已启用："+reasonOr(reason, "未填写理由"))
	}
	ev := intercept.NewKillSwitchEvent(req.On, by, reason, paused)
	cur.On = req.On
	cur.At, cur.By, cur.Reason, cur.TasksPaused = ev.At, by, reason, paused
	cur.History = intercept.AppendKillSwitchHistory(cur.History, ev)
	if err := intercept.WriteKillSwitch(s.m.pg, cur); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if req.On {
		log.Printf("[scope] 停机开关已启用（%s，理由：%s）：暂停 %d 个在跑任务", by, reasonOr(reason, "未填写"), paused)
	} else {
		log.Printf("[scope] 停机开关已解除（%s）", by)
		// 入口层立刻放行（不必等下一个 tick）；**不**顺手恢复任务。
		goSafe("server/reconcile-concurrency", nil, func() { s.reconcileConcurrency() })
	}
	resp := map[string]any{
		"kill_switch": cur, "tasks_paused": paused,
		"forced_by_env": intercept.KillSwitchForcedByEnv(),
		"note":          map[bool]string{true: "已停机：所有任务暂停、动作被拒。解除后需手动继续任务。", false: "已解除：可以开始新工作；之前暂停的任务不会自动恢复。"}[req.On],
	}
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	writeJSON(w, 200, resp)
}

// resumeAllTasks 继续所有暂停中的任务。停机解除后由人点一下——不自动做，
// 因为"恢复"意味着平台重新对目标动手，那一下该由人按下。
func (s *Server) resumeAllTasks(w http.ResponseWriter, r *http.Request) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	if blocked, reason, _ := s.safetyStop(); blocked {
		writeErr(w, 409, "当前处于停止状态（"+reason+"）：先解除停机开关或等作业时间窗开启，再继续任务")
		return
	}
	resumed, errs := s.resumeAllPaused()
	log.Printf("[scope] 手动继续：%d 个任务", resumed)
	resp := map[string]any{"resumed": resumed}
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	writeJSON(w, 200, resp)
}

func reasonOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// clientAddr 取请求来源（回执里记"谁停的"用）。
func clientAddr(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		if i := strings.Index(fwd, ","); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return fwd
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if host == "" {
		return "unknown"
	}
	return host
}
