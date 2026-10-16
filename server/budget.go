package server

// 任务级 token 预算护栏：用量到 70/85/95% 各向 planner 投递一次收敛提示，
// 达到 100% 复用超时收尾时序（停派/领新意图 → drain → 终局 planner 轮 → 定终态）。
// 预算存 setting `task_token_budget`（单位 token，0=关闭）；提示经 hint 通道
// 进入 planner 输入（与人工提示同路）。

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// BudgetStage classifies token usage against a task budget:
// "" (off / under 70%) → "notice" (≥70%) → "urgent" (≥85%) → "critical" (≥95%) → "exhausted" (≥100%).
func BudgetStage(used, budget int64) string {
	if budget <= 0 || used <= 0 {
		return ""
	}
	switch {
	case used >= budget:
		return "exhausted"
	case used*100 >= budget*95:
		return "critical"
	case used*100 >= budget*85:
		return "urgent"
	case used*100 >= budget*70:
		return "notice"
	}
	return ""
}

// budgetStagePercent renders a stage's threshold for display.
func budgetStagePercent(stage string) string {
	switch stage {
	case "notice":
		return "70%"
	case "urgent":
		return "85%"
	case "critical":
		return "95%"
	}
	return stage
}

// BudgetNoticeText renders the planner-facing notice for one stage.
func BudgetNoticeText(stage string, used, budget int64) string {
	head := strconv.FormatInt(used, 10) + "/" + strconv.FormatInt(budget, 10)
	switch stage {
	case "notice":
		return "⏳【预算提示 70%】本任务 token 用量 " + head + "。开始收敛：优先把已开方向推进到结论，谨慎新开探索面。"
	case "urgent":
		return "⚠【预算提示 85%】本任务 token 用量 " + head + "。只保留高价值方向，不再新增低优先级探索面。"
	case "critical":
		return "❗【预算告急 95%】本任务 token 用量 " + head + "。尽快让在跑方向收尾并把关键结论写成事实/漏洞——任务即将因预算耗尽进入收尾。"
	}
	return ""
}

// taskTokenBudget resolves the global task token budget setting (0 = off).
func (s *Server) taskTokenBudget() int64 {
	v, ok, err := s.m.pg.GetSetting(settingTokenBudget)
	if err != nil || !ok {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// tokenBudget is the engine-side view of the same setting.
func (e *Engine) tokenBudget() int64 {
	v, ok, err := e.m.pg.GetSetting(settingTokenBudget)
	if err != nil || !ok {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// budgetNotice delivers one notice per threshold crossing to the planner through
// the hint channel (humans use the same path); the planner wakes on Notify.
func (e *Engine) budgetNotice(t *Task, stage string, used, budget int64) {
	if stage == "" || stage == "exhausted" {
		return
	}
	if last, ok := e.budgetStage.Load(t.ID); ok && last.(string) == stage {
		return
	}
	e.budgetStage.Store(t.ID, stage)
	text := BudgetNoticeText(stage, used, budget)
	if text == "" {
		return
	}
	// 时间线留痕先落：档位提示既进 planner（hint），也落一条 system 活动，让人在
	// 会话/图谱时间线上看到"何时触发了哪一档"，而不是只存在于 planner 上下文里。
	// 顺序有意是"活动先落、hint 后投"：谁看到提示（含验收断言）时，时间线一定已有痕，
	// 不会出现"提示到了、时间线还没写"的观察窗口。
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
		Summary: fmt.Sprintf("Token 预算 %s（%d/%d）", budgetStagePercent(stage), used, budget),
		Detail:  text})
	t.trigMu.Lock()
	t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "hint", Hints: []string{text}})
	t.trigMu.Unlock()
	t.Notify()
	log.Printf("[budget] task %s %s (%d/%d)，已向 planner 投递预算提示", t.ID, stage, used, budget)
}
