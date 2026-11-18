package server

// 任务级 token 预算护栏：用量到 70/85/95% 各向 planner 投递一次收敛提示。
// 达到 100% 时默认**仅告警并继续**；设置 `task_token_budget_pause=true` 才暂停任务
// （取消在跑的 Planner/Worker、停止派发），由人决定是否继续。暂停不终止任务、不出终局报告
// ——预算是"停下来问一句"，不是"替人做决定"。用户继续后，同一档预算不再重复打断；
// 改了预算值会重新生效。预算存 setting `task_token_budget`（单位 token，0=关闭）；
// 提示经 hint 通道进入 planner 输入（与人工提示同路）。

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
		return "❗【预算告急 95%】本任务 token 用量 " + head + "。尽快让在跑方向收尾并把关键结论写成事实/漏洞——预算耗尽后的处理由系统设置决定：可能暂停等人工确认，也可能仅告警继续。"
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

// tokenBudgetPauseEnabled reports whether exhaustion should pause the task. The
// default is false (warn-only) so a configured budget never hard-stops a deep chain
// unless the operator explicitly opts in.
func (e *Engine) tokenBudgetPauseEnabled() bool {
	if e == nil || e.m == nil || e.m.pg == nil {
		return false
	}
	return e.m.pg.GetBool(settingTokenBudgetPause, false)
}

// budgetWarnOnly records the exhaustion event without pausing. The ack is stored by
// the caller, so this fires once per budget value.
func (e *Engine) budgetWarnOnly(t *Task, used, budget int64) {
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
		Summary: fmt.Sprintf("Token 预算耗尽（%d/%d），仅告警继续", used, budget),
		Detail:  "按设置：预算耗尽不暂停任务，也不生成终局报告。平台继续派发；如需硬停，请在「系统 → 执行与并发」开启“预算耗尽时暂停任务”，或调整任务 token 预算。"})
	log.Printf("[budget] task %s 预算耗尽 (%d/%d)，仅告警（未启用暂停）", t.ID, used, budget)
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

// SetBudgetPauseHandler wires the server's full pause path (persist paused flag,
// cancel the main-agent turn, reconcile concurrency). Without it the engine still
// pauses in memory as a fallback, but the paused state would not survive a restart.
func (e *Engine) SetBudgetPauseHandler(fn func(*Task) error) { e.budgetPause = fn }

// AckExhaustedBudget marks the current budget value as user-acknowledged when the
// task is already over it. Called on admission/resume: after a budget pause, a
// human pressing "继续" means "I have seen the overrun and choose to continue", so
// the same budget value must not pause the task again. Changing the budget value
// naturally re-arms the guard (the stored value no longer matches).
func (e *Engine) AckExhaustedBudget(t *Task) {
	if t == nil || e.m == nil || e.m.pg == nil {
		return
	}
	budget := e.tokenBudget()
	if budget <= 0 {
		return
	}
	if used, err := e.m.pg.TokenUsedByTask(t.ID); err == nil && used >= budget {
		e.budgetAck.Store(t.ID, budget)
	}
}

// pauseForBudget is the exhaustion action: pause the task instead of settling it.
// It deliberately does NOT call settleTask — no terminal planner round, no final
// status, no report. The coordinator keeps running so a later resume needs no
// re-arm; budgetAck prevents the same budget from pausing it again.
func (e *Engine) pauseForBudget(t *Task, used, budget int64) {
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
		Summary: fmt.Sprintf("Token 预算耗尽（%d/%d），任务已暂停", used, budget),
		Detail: "按策略：预算耗尽不终止任务、不生成终局报告。平台已停止派发新意图并取消在跑的 Planner/Worker；" +
			"在任务页调整预算或确认后点「继续」即可接着跑。继续后同一档预算不再重复打断（修改预算值会重新生效）。"})
	log.Printf("[budget] task %s 预算耗尽 (%d/%d)，暂停任务（不终止、不出终局报告）", t.ID, used, budget)
	if e.budgetPause == nil {
		e.Pause(t.ID, agent.AbortBudgetExhausted)
		return
	}
	if err := e.budgetPause(t); err != nil {
		log.Printf("[budget] task %s 暂停失败: %v（先内存暂停，避免继续消耗）", t.ID, err)
		e.Pause(t.ID, agent.AbortBudgetExhausted)
	}
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
