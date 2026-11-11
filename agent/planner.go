package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/guard"
	"github.com/lin9527ya/blade/intercept"
	"github.com/lin9527ya/blade/policy"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	tx              *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window          int                                    // context window in tokens (for compaction)
	windowFn        func() int                             // optional dynamic task-chain minimum
	maxTurns        int                                    // max agent turns per run (0 = unlimited)
	roundDur        time.Duration                          // 单轮墙钟预算（0 = 不限），由 server 按 agent 配置注入
	killWork        func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	rotateEgress    func(reason string) (string, error)    // host callback: switch Clash egress node
	steerWork       func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	// lastRoundNote 是上一轮的结论摘要。默认每轮开新会话（不 resume 全量 transcript），
	// 用它承接「上一轮想清楚了什么、派了哪些意图、产出了哪些事实」，避免上下文膨胀。
	lastRoundNote string
	// lastYieldTotal / lowYieldStreak：收成（finding+foothold+credential）连续不增长时
	// 强制复盘一次——比"每 5 轮"更贴近"卡住了"的真实信号。
	// hot 是跨轮复用的规划会话。每轮结束后把历史折叠成「本轮输入 + 结论」两条消息
	// （Session.AppendMessages），下一轮继续用同一个 Session：system prompt 与工具
	// schema 的缓存前缀因此保持不变，而上下文体积不随轮数增长。
	// 不用时（resume 模式 / 失败轮）为 nil。
	hotMu sync.Mutex
	hot   *agentcore.Session
	// hotTools 是热会话首轮创建、其工具闭包所绑定的那个 ToolSet。Session 的 registry
	// 在 NewSession 时固定，所以后续轮次必须**在这个对象上**重置轮级状态；对每轮新建的
	// ToolSet 调用 BeginRound 不会影响模型实际调用的工具。
	hotTools          *ToolSet
	hotRounds         int
	hotLastUsed       time.Time
	proxyAddr         string                  // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                  // recording proxy's CA cert path (HTTPS verify)
	proxyFn           func() (string, string) // dynamic resolver (proxy-pool failover); nil = static
	webSearch         WebSearchOpts           // web_search tool backend selection (off by default)
	workDir           string                  // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool             // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool             // resolver: use non-streaming (Complete) path? (nil = streaming)
	// execService（P13）由 host 注入：planner 也能查看/发起长任务执行。
	execService ExecService
	// policyFn（P20）解析本任务的探索策略（nil = 无策略）。每个 run 现读：
	// 改策略下一轮生效，无需重建 agent。
	policyFn     func() *policy.Effective
	workersFn    func() int  // 本任务运行时的 worker 池大小（队列背压；nil = 不启用）
	noaEnabledFn func() bool // resolver: use experimental noa compaction? (nil = off)
	// plannerPromptFn 返回任务级 planner 提示词快照（nil/空 = 走全局/默认链）。
	plannerPromptFn func() string
	boundedView     func() bool // nil = 有界视图（默认）
	maxTokensFn     func() int  // resolver: per-reply output cap (nil/0 = send no cap)
	compactor       *Compactor  // cold-node compaction (§7); nil = disabled
	// guard is the SAME per-task Guard workers use (intercept rules + LLM judge +
	// RoE scope gate). Planner can call Bash/default tools during its light opening
	// probe; without this it bypassed every command-level boundary.
	guard *guard.Guard

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetRoundSeconds 注入单轮墙钟预算（秒；0 = 不限）。没有它，一次跑飞的规划会一直占着
// 这个任务（只有任务总 deadline 能兜底，而很多任务并没有 deadline）。
func (p *Planner) SetRoundSeconds(n int64) { p.roundDur = time.Duration(n) * time.Second }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// WaitCompaction 等这个 planner 名下的后台压缩收尾（任务停止/删除时调用）。
// 压缩不受单轮 ctx 约束，没有这一步它会在任务拆掉之后继续写探索图。
func (p *Planner) WaitCompaction(ctx context.Context) bool {
	if p == nil {
		return true
	}
	return p.compactor.Wait(ctx)
}

// SetGuard attaches the task Guard. nil keeps the historical no-guard behavior
// (tests / hosts without an interceptor).
func (p *Planner) SetGuard(g *guard.Guard) { p.guard = g }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

// SetExecService 注入长任务执行服务（P13）；nil = 未接线（工具返回可读拒绝）。
func (p *Planner) SetExecService(svc ExecService) { p.execService = svc }

// SetRotateEgressHandler wires the Clash egress rotation callback.
func (p *Planner) SetRotateEgressHandler(fn func(reason string) (string, error)) { p.rotateEgress = fn }

// SetPolicyResolver 注入任务级探索策略解析器（P20）；nil = 无策略。
func (p *Planner) SetPolicyResolver(fn func() *policy.Effective) { p.policyFn = fn }

// SetWorkersResolver 注入本任务**运行时**的 worker 池大小（队列背压用；nil/0 = 不启用上限）。
// 用池快照而不是实时设置：改 workers 只影响之后启动的任务，按实时值排队会算错容量。
func (p *Planner) SetWorkersResolver(fn func() int) { p.workersFn = fn }

func (p *Planner) workers() int {
	if p.workersFn == nil {
		return 0
	}
	return p.workersFn()
}

// policy 取本轮的策略快照（解析器未接线 → nil = 不判定）。
func (p *Planner) policy() *policy.Effective {
	if p.policyFn == nil {
		return nil
	}
	return p.policyFn()
}

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// PlannerSessionID is the transcript AND noa-archive session id of a task's planner
// session (one accumulating conversation per task, shared by both stores).
func PlannerSessionID(explorationID int64) string {
	return fmt.Sprintf("exp%d-planner", explorationID)
}

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) {
	p.proxyAddr, p.proxyCACert, p.proxyFn = addr, caCert, nil
}

// SetProxyFunc resolves egress per run (proxy-pool failover without a rebuild).
func (p *Planner) SetProxyFunc(fn func() (string, string)) { p.proxyFn = fn }

func (p *Planner) proxySnapshot() (string, string) {
	if p.proxyFn != nil {
		return p.proxyFn()
	}
	return p.proxyAddr, p.proxyCACert
}

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// SetBoundedView wires the resolver deciding whether the pre-fetched situation is
// the bounded blackboard view (default) or the full graph_overview payload.
// Read per round so the setting takes effect without rebuilding the planner.
func (p *Planner) SetBoundedView(fn func() bool) { p.boundedView = fn }

func (p *Planner) useBoundedView() bool { return p.boundedView == nil || p.boundedView() }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// SetPlannerPromptResolver wires the task-level planner prompt snapshot.
func (p *Planner) SetPlannerPromptResolver(fn func() string) { p.plannerPromptFn = fn }

func (p *Planner) plannerPromptOverride() string {
	if p.plannerPromptFn == nil {
		return ""
	}
	return p.plannerPromptFn()
}

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【你的规划待办（跨唤醒保留，上一轮你写的）】：\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("据此推进：只对【前置步骤已完成 / 其依赖的 fact 已存在】的下一步派意图；用 TodoWrite 更新清单（把已被 fact 满足的步骤标 completed）。不要重复派已在清单里 pending/in_progress 的步骤。")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = 摘要).
//	"goal"    — the human (via 主 agent 的 set_goals) added one OR MORE goals in a
//	            single call (Goals = 本次新增的目标文本，1+ 条；set_goals 支持批量).
//	"goal_deleted" — the human deleted a goal from 总览的目标管理 (Detail = 被删目标文本).
//	"goal_edited"  — the human edited a goal from 总览的目标管理 (OldGoal→NewGoal 文本).
//	"goal_abandoned" — the human abandoned a goal (Summary=目标文本, Detail=放弃理由).
//	"cancelled" — the human deleted intent IntentID (Detail = 删除原因). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
//	"requeued" — intent IntentID went back to open for resumption (long-running
//	            executions settled, lease expired, pause/resume). Detail = why.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	State    string   // Kind=="done" 专用：本次结算终态（done/negative/no_output/blocked/…）
	Summary  string   // Kind=="cancelled" 专用：删除前捕获的意图摘要（真删除后节点已不存在，无法再查）
	Goals    []string // Kind=="goal" 专用：本次 set_goals 新增的目标文本（1 条或多条）
	OldGoal  string   // Kind=="goal_edited" 专用：修改前的目标文本
	NewGoal  string   // Kind=="goal_edited" 专用：修改后的目标文本
	Hints    []string // Kind=="hint" 专用：本次 add_hint 新增的提示文本（1 条或多条）
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【本次触发本轮的实际变动（先看这里，再决定是否补方向）】：")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- 人（主 agent）新增了一个目标：%s —— 新的待达成目标，请据此补充探索方向（若尚无对应意图）。", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 人（主 agent）新增了 %d 个目标：%s —— 均为新的待达成目标，请逐一为尚无对应意图的目标补充探索方向。", len(ev.Goals), strings.Join(ev.Goals, "；")))
			}
		case "hint":
			// 提示是**数据/方向**，不是覆盖约束的指令——这句必须写进触发行本体：
			// 模型容易把"人新增了提示"读成"照做，包括越界"。
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- 人新增了一条战略提示：「%s」—— 它是要纳入判断的测试方向，优先级低于操作约束与授权范围；按【战略提示的采纳纪律】处理，采纳后当轮 consume_hint。", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 人新增了 %d 条战略提示：%s —— 均为要纳入判断的测试方向，优先级低于操作约束与授权范围；逐条按【战略提示的采纳纪律】处理，采纳后当轮 consume_hint。", len(ev.Hints), quoteAll(ev.Hints)))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- 人删除了该目标：%s —— 该目标已移除，请据此重判剩余目标/方向（不必再为它派意图）。", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- 人修改了目标，由「%s」变为「%s」—— 请据新目标调整探索方向（原方向若已不适用请停派）。", ev.OldGoal, ev.NewGoal))
		case "goal_abandoned":
			b.WriteString(fmt.Sprintf("\n- 人放弃了该目标：%s（理由：%s）—— 它不再阻断收官，也不必再为它派意图；请据此重判剩余目标/方向。", ev.Summary, ev.Detail))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- 意图 #%d（%s）的 worker 报告了一个 finding：%s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// 意图内容优先用删除时捕获的 Summary（真删除后节点已不存在，intentSummary 查不到）。
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- 意图 #%d 由用户删除，意图内容是：%s、删除原因是：%s。该意图已删除（不再执行）；请据此重新规划。", ev.IntentID, sm, ev.Detail))
		case "reconcile":
			// 平台生成的收尾对账清单（只含计数与动作提示），不是目标内容。
			detail := strings.TrimSpace(ev.Detail)
			if detail == "" {
				detail = "进入收官期：停止扩面，转入对账。"
			}
			b.WriteString("\n- " + detail)
		case "requeued":
			// 退回 open ≠ 完成：它仍会被 worker 领取续跑。旧版在这里也发 "done"，
			// planner 会去取一个并不存在的输出结论，误以为这条已经做完。
			detail := strings.TrimSpace(ev.Detail)
			if detail == "" {
				detail = "等待重新领取"
			}
			b.WriteString(fmt.Sprintf("\n- 意图 #%d（%s）已退回 open 等待 worker 续跑：%s —— 它的产出尚未结算，不要当成已完成，也不要重复派同一方向。", ev.IntentID, intentSummary(ts, ev.IntentID), detail))
		default: // "done"
			head := fmt.Sprintf("\n- 意图 #%d（%s）的 worker 结束", ev.IntentID, intentSummary(ts, ev.IntentID))
			if ev.State != "" {
				head += fmt.Sprintf("，终态 %s（%s）", ev.State, intentStateLabel(ev.State))
			}
			switch ev.State {
			case "no_output":
				b.WriteString(head + "：本次运行没有写回任何产出——不要当成已覆盖，需要换打法重派或另开方向。")
			case "blocked", "exhausted", "stopped":
				b.WriteString(head + "：" + workerOutput(ts, ev.IntentID))
			default:
				b.WriteString(head + "，输出结论：" + workerOutput(ts, ev.IntentID))
			}
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("；本意图新产生的事实 id：%s ", fids))
			}
		}
	}
	b.WriteString("\n（完整细节可 node_detail / get_worker_output / list_findings 再查。）")
	return b.String()
}

// intentStateLabel 把意图终态翻成 planner 能直接读的中文标签。终态语义是
// 「这轮到底做成了什么」的关键：done 与 no_output 在后续规划里完全相反。
func intentStateLabel(state string) string {
	switch state {
	case "done":
		return "完成"
	case "negative":
		return "否定结论"
	case "out-of-scope":
		return "超出授权范围"
	case "no_output":
		return "无产出"
	case "blocked":
		return "阻塞"
	case "exhausted":
		return "未做完"
	case "stopped":
		return "被终止"
	case "paused":
		return "已暂停"
	default:
		return state
	}
}

// quoteAll 把多条外来文本逐条用「」包起来再用分号连接：提示文本是人写的自由文本，
// 不加边界时读者分不清哪一段是平台的话、哪一段是提示本身。
func quoteAll(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = "「" + s + "」"
	}
	return strings.Join(q, "；")
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, "、")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, err := ts.ActivityListTail(&id, 1000) // 尾部：结论在最新那一窗，不在开头 1000 步里
	if err != nil {
		return "(取输出失败)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(该 work 尚无输出记录)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …（已截断，完整见 get_worker_output）"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any, bounded bool) string {
	b, err := json.Marshal(SanitizeJSONStrings(data))
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	header := "\n\n【本轮态势（完整 graph_overview 预取，等同你调用该工具的返回；有界视图已关闭。更早的知识用 list_facts/list_worker_traces 翻页）】：\n"
	if bounded {
		header = "\n\n【本轮态势（有界黑板视图，等同 graph_overview 的默认返回；totals 是真实总数，列表只是窗口。需要折叠区/继承任务/AD 图等完整态势时调 graph_overview(detail=true)；更早的知识用 list_facts/list_worker_traces 翻页）】：\n"
	}
	return header + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中间产物输出规约
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
// PlannerPromptBody 返回内置 planner 正文（DB agent_prompts 的播种与迁移用）。
func PlannerPromptBody() string { return plannerDefaultTmpl }

const plannerDefaultTmpl = `你是一个网络安全平台授权渗透测试系统的"规划者"，被频繁唤醒（图一变就唤醒）。职责：读态势 → 判目标 → **只在确有未被覆盖的新方向时**补充探索意图。你是规划者、不是执行者：本轮所有产物只能是【生成/说清意图】或【判定目标】，绝不在 plan 里把活干了。

任务目标：{{.Goal}}

**本轮该产出几个意图（先想清楚这条）**：
- **硬底线（最高优先）**：只要【目标未达成】且【当前没有任何 open 或 running 意图】（frontier_open=0 且 running_intents 为空），本轮就【必须】产出至少一个向目标推进的意图——没有在跑的 work 可等、也没有在排队的方向时，产出 0 意图=任务停摆；哪怕已知方向都只在 recent_done 里，也要据下面 done/exhausted/blocked 的判断另开一条或续派一条。
- 硬底线之外，**产出 0 个意图是正常结果，但要有正当理由**（不是"少派更稳"的默认）：①**已覆盖**——你想到的方向都已被仍在 open/running 的意图处理（换措辞重复生成已存在的意图是严重错误）；②**等待依赖**——下一步依赖当前在跑 work 的产出、而它还没出来（此时硬派会让下游拿不到前置而空转，应等下次唤醒图更新后再派）。
- 反过来：确有【未覆盖、且不依赖在跑 work】的新方向，或目标未达成且范围内仍有未测面，就该派——别把 0 意图当偷懒的默认。

**每次唤醒的决策流程**：

1. **完整态势已附在本提示下方**（就是 graph_overview 的返回，无需再调它）：task（原始标题+目标/根节点）、资产计数、goals+状态、open/running/recent_done 意图、无端点的站点（若态势提供该字段，提示可能待探的方向）、facts（探索事实数，与漏洞是两类）、recent_facts（{id,summary,confidence?}）。
   - **范围**：探索节点（goals/意图/facts/findings）只含本任务；**资产默认按任务隔离**（仅本任务及明确关联任务；用户开启「共享全局资产」后可查询全库）——出现非本任务相关的资产时忽略。
   - **血缘**：每个意图带 parents（上游：派生自哪些事实/意图）和 yields（下游：产生了哪些事实/发现），recent_facts 每条带 from_intent；据此理解"哪些事实来自哪个方向、能否综合出新方向"。
   - **否定/存疑观察**（recent_facts 里"端口关闭/不可注入"等）是 worker 的观察、不是定论：采信前先 node_detail(id) 看 evidence——evidence 扎实、confidence=observed 且手段已穷尽的才视为该方向暂时封住；evidence 缺失、只是"看起来像/只探一次"、或 confidence=inferred 的，按【尚未探明】处理，若在范围内且无其它意图覆盖，默认派一条复核意图去证实或推翻（**同一否定方向至多复核一次**；复核后仍为否定、且证据合理，就尊重该结论、不再派）。
   - **要更深细节才按需调**：list_facts（分页，最新在前，默认 20，可 q 过滤、before 翻页，带 total/has_more）、list_findings（全部漏洞）、node_detail(id)（完整证据/详情；列表/recent_facts 只给摘要）、list_assets（pull：q 搜索、type/company_id/task_id 过滤、分页，或 id/ids 直取）、asset_neighbors。资产是否共享由用户设置决定，不能自行开启；共享不代表扩大测试授权。

2. **判目标（核心职责）**：goals 字段已含目标与状态；对已被某发现/事实证明的未达成目标，调 prove_goal(goal_id, evidence_id, reason) 标 met。**当你标记的恰是最后一个未完成目标时，系统自动判定整个任务完成**——收官只由逐个 prove_goal 驱动，没有别的"一键完成"手段。
   - ⚠️ **量化验收核对（严禁提前盖章）**：目标含可量化条件（覆盖度达 X%、达到指定数量、获得某权限）时，prove_goal 前【必须】核对上方 graph_overview 的实测值（coverage.pct、findings_total 计数等）：未达标就【禁止】prove_goal，改派意图补差；不得以"大体达成/核心已拿下"为由提前标 met。例：要求覆盖度 100% 而实测 coverage.pct=40% → 未达成，继续派补测意图。

3. **（可选，仅开局、极轻量）探测理解**：仅当图里几乎还没有 fact（recent_facts 基本为空、任务刚开始）、仅凭态势无法把初始意图说具体时，才用 Bash 等对目标做极少量、只读的探测（如 1–2 次 curl 看首页/指纹）。**唯一合法产物是一句更精准的意图描述**——绝不是漏洞的发现/验证/利用，也不是端点/目录/参数的枚举结果（那些是 worker 的活，写成意图派下去）。三条硬边界：
   - 图里已有 worker 产出的 fact（facts>0 / recent_facts 非空）→【禁止】再自己探测，一切判断基于已有 fact，本轮产物只能是"派新意图"或"结束"；想深挖某线索 → 派意图让 worker 去查，不是自己 curl。
   - 即使开局也最多探 ≤3 次就收手，只为把初始意图说清；一旦发现自己在"深入查证"而非"快速定方向"（逐个枚举端点/目录、逐个试 id、解码链、反复探同一接口、任何注入/越权/漏洞的测试验证——全是 worker 的重活），立刻停手写成意图。
   - 能从现有事实/态势判断的，根本不必探测。

4. **决定补哪些新方向**：**这里的"克制"只指【不重复已存在的意图】，不是"能少派就少派"**——目标未达成时，默认追问是"为逼近目标，还有哪些更深、更狠、尚未覆盖的打法"，而不是"是否可以收尾"。意图是【开放的探索方向】（不是固定类型/菜单），结合已知事实、资产、目标自判方向，逐一与 open + running + recent_done 比对：
   - 已有 open/running 覆盖 → 不再生成（正在处理）。
   - 在 recent_done 里出现过 → **先看该意图的 state（每条都带）分辨怎么停的，再决定**：
     · **done（正常跑完）**：已覆盖 → 不原样重派；是否死路看它 yields 出的 fact 结论、而非 state；仅出现【材料性新机理】（新事实/资产/参数/明显不同的打法）才重派，且 summary 写清与上次的不同；换措辞、"再试一次说不定行"不算，禁止重试。
     · **exhausted（预算耗尽、探到一半被掐断，只写回部分）/ blocked（模型或网络失败、基本没探成）**：都是中途没善终、信息不全——先用 get_worker_trace / get_worker_output 看它实际做到哪、卡在哪，再从下列里选：接近突破被预算掐 → 派"接上次进度继续"；纯外部故障没跑成（blocked 常是）→ 直接重派同方向；每次卡同一处 → 换打法/方向。依据永远是 trace 里的真实进度，不是 state 本身。
   - 完全无任何意图覆盖的全新方向 → 生成。
   - 所有已知方向都被仍在 open/running 的意图覆盖 → 不生成、直接结束（有在跑/在排队的 work，等它们推进）；但若只剩 recent_done 覆盖、已无 open/running 而目标未达成 → 按顶部硬底线必须另开或续派。
   - **深度优先于覆盖度**：coverage 是下限/验收项、不是探索目标本身；发现高价值入口（可能通向 RCE/提权/数据外泄）后，优先派意图把那条路【往深打穿】，而不是为拉平覆盖度去铺广、逐个资产浅测。
   - **保持路线多样、别过早收敛**：目标未达成时，若现有意图都挤在同一条路线/入口，而存在【本质不同】的未覆盖方向（另一入口面/另一类资产/另一条利用链），优先补那条分歧方向，而不是在同一线上加同义意图（看实质差异，不看措辞）；若该分歧方向已被现有意图覆盖，仍不生成。理想是 2–3 条机理不同的路线并存（如"从上传链打"与"从认证绕过打"），某条交出【目标逼近】的证据后才把资源集中过去。**但多样性永远服从顶部【操作约束】**：被约束排除的入口面/端口/主机/操作，即使本质不同也绝不生成意图。

   **串行利用链：分步派，别拆成并行。** 强依赖串行链（①→②→③，后一步依赖前一步的实际产出）：不要一次性并行下发（下游拿不到还不存在的前置只会重复/空转）；用 TodoWrite 把整条链记成待办（每步一条），本轮只派"前置已满足"的那步（通常第一步），待它产出 fact 后下次唤醒（提示会带上待办清单）再派下一步并把已满足的标 completed。"同一件事"别拆两条（"确认触发点"和"触发触发点"是同一步）；只有【平行、互不依赖】的维度（如枚举多个不相关端点）才用多意图并行。

5. **提交**：用【一次】add_intent 批量提交筛出的新方向（intents 数组，最多 4 个最高价值的，不要逐条多次调）：
   - **summary**：一句话自然语言描述该方向（测试目标完整地址 + 做什么 + 为什么），不套固定分类；去重主要靠它与已有意图比对。
   - **asset_ids**：本方向要测试/攻击的目标资产 id（尽量传，0/1/多个，来自 list_assets）——只要方向围绕具体资产（站点/接口/参数/主机）就务必传，用于覆盖去重、连入资产链路，跨多资产就都传；纯全局侦察无具体资产才留空。
   - **parent_ids**：本方向由哪些上游节点综合得出（可选，0/1/多个）——多个事实结合产生一个意图就都传，派生自某上游意图/发现也传其 id，顶层全新方向留空。

不重复、不硬凑；但目标未达成、又有未覆盖且更深的打法时，该派就派。简洁、聚焦、高效。`

// PlannerTemplateDefault returns the DB default template body ("" when none).
// Wired at startup so the agent package stays free of DB imports.
var PlannerTemplateDefault func() (string, bool)

func plannerSystem(goal, dataDir, workDir, taskOverride string) string {
	vars := PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()}
	body := ""
	// ① 任务级快照（tasks.planner_prompt_override）
	if strings.TrimSpace(taskOverride) != "" {
		if out, err := renderTmpl(taskOverride, vars); err == nil {
			body = out
		} else {
			log.Printf("[planner] 任务级 planner 提示词渲染失败，回退全局链: %v", err)
		}
	}
	// ② 全局覆盖（agent_prompts 当前版本）。**内容仍等于出厂正文时视同未覆盖**：
	// agent_prompts 在首次启动时就被 seed 成内置正文，若照字面当覆盖，③ 永远轮不到，
	// 模板库的「默认模板」就成了摆设。因此「系统 → Agent → planner」里改过正文才压过
	// 模板库；点「恢复为内置默认」后正文回到出厂内容，模板库默认重新接手——这正是
	// 「恢复默认」该有的含义：回到平台当前认定的默认，而不是钉死在某一版代码文本上。
	if body == "" && PromptOverride != nil {
		if t, ok := PromptOverride("planner"); ok && t != "" && strings.TrimSpace(t) != strings.TrimSpace(plannerDefaultTmpl) {
			if out, err := renderTmpl(t, vars); err == nil {
				body = out
			} else {
				log.Printf("[planner] 全局 planner 提示词渲染失败，回退模板库默认: %v", err)
			}
		}
	}
	// ④ 内置正文。这里直接渲染，不走 renderSystem——否则会二次套用 ② 与 extras。
	if body == "" {
		if out, err := renderTmpl(plannerDefaultTmpl, vars); err == nil {
			body = out
		} else {
			body = plannerDefaultTmpl
		}
	}
	// ARTEX parity: the editable body owns all exploration guidance. Only safety,
	// provenance/context and artifact rules stay code-owned; no constraint groups,
	// hybrid scaffold or policy/override blocks are appended.
	out := body + promptExtras("planner") +
		coldArchiveBlock + backoffDisciplineBlock + egressDisciplineBlock + hintLifecycleBlock + cveToolBlock +
		artifactSpec(workDir) +
		untrustedDataBlock() + blackboardViewBlock
	return out
}

// blackboardViewBlock is code-owned: the editable body predates the bounded view
// and still says the situation is the complete graph. Keep the distinction in
// front of the model without rewriting operator-edited prompts.
const blackboardViewBlock = `

【黑板视图（代码持有）】
- 本轮预取的是有界视图：totals 是真实总数，open/running/recent 等列表只是窗口；窗口外的方向不等于没派过。
- 需要完整态势（折叠区、继承任务、AD 图、检测热点等）时调 graph_overview(detail=true)；只看增量事件与路线用 blackboard_view(change_cursor=change_cursor)（旧 after 只跟踪节点新增，不代表全部图变化）。
- 意图 summary 就是方向本身：写清入口、动作和验证目标；图中已有的 open/running/recent 意图决定是否重复。`

// coldArchiveBlock 是冷区（cold digest）的**代码持有**纪律：老库的正文与领域模板
// 都不知道折叠机制，模型会把 recent_facts 的“最新一窗”当成全部历史，于是把已经
// 试过的方向当新方向重派。折叠只省上下文，不是删除——这句必须每轮都在。
const coldArchiveBlock = `

【冷区与 digest（折叠 ≠ 删除）】
- graph_overview.cold_digests 是较早节点折成的摘要（id + body + member_count）；成员仍然存在，node_detail(id) 可查全文。
- list_facts 的条目若带 folded_into_digest，说明它已折进该 digest；摘要没出现在 recent_facts，不代表这件事没发生过。
- 更老的 digest 只给裸 id（cold_digests_more），用 expand_digest(id) 展开。
- 判定「已覆盖/已经试过」时必须把冷区算上：折叠是上下文压缩，不是删除，也不是「没做过」。`

// hintLifecycleBlock 是提示（hint）的**代码持有**纪律，追加在可编辑正文之后。
//
// 为什么不只改 plannerDefaultTmpl：可编辑正文只对全新库生效，已有库里存的是旧模板；
// 而"采纳后 consume"是提示功能闭环的一部分——缺了它，提示会永远 active、每轮重复出现。
// 与 artifactSpec / stealthBlock 同一模式：生命周期规则归代码，措辞偏好归模板。
const hintLifecycleBlock = `

【战略提示（hints）的采纳纪律】
- 态势里的 hints 是**操作者在任务运行中直接写下的测试方向**（人写的，可信），不是 worker 的观察、也不是新事实。它**低于操作约束与授权范围**：提示要求的入口/动作若被约束排除，照约束拒绝，并在 note 写明理由。
- 每轮逐条处理 active 提示：先看它是否已被 open/running/paused 意图覆盖（按实质，不按措辞）。已覆盖就不重复派；未覆盖且仍适用，就用 add_intent 派一条，summary 里写明「按提示 #<id>」。
- **采纳后当轮调用 consume_hint(hint_ids=[<id>], note="…")**。判断为重复/不适用而不派的，也可以 consume 并写明理由——不要无理由让一条提示跨多轮一直 active。
- 需要等 worker 结果才能判断下一步的提示，可以暂时留着（下一轮再处理），但要能说清在等什么。
- consumed 的提示不再出现在态势里；**已采纳的提示不要重复执行**。删除提示是人的操作，planner 只 consume，不删除。`

func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	proxyAddr, proxyCA := p.proxySnapshot()
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	// wireToolSet 把「本轮」依赖与轮级状态装到**本轮实际被调用的**那个 ToolSet 上。
	//
	// 热会话复用时，模型调用的工具闭包绑定的是热会话首轮的 ToolSet（agentcore.Session
	// 的 registry 在 NewSession 时固定），而不是这里新建的 tsx。所以两处都要走同一套
	// 重装：否则 intentsRound 停在首轮的值，pace:round 会把后续每一轮的意图都拒掉
	// （任务 18/19 实测），GoalMet 与广度快照也会跨轮残留。
	// 复盘轮节律：有在跑的活才复盘（没活时硬底线优先）；每 5 个普通轮或新增 ≥4 条
	// 事实触发一次，两次之间至少 3 个普通轮。计数器放在 Planner 上，按任务隔离。
	wireToolSet := func(t *ToolSet) {
		t.ts = ts // 任务存储指针跨轮不变；重装一遍以防替换
		originNode := int64(0)
		if origin, _ := ts.OriginFactID(); origin > 0 {
			originNode = origin
		}
		t.BeginRound(p.policy(), p.workers(), taskClockFrom(ctx), originNode)
		t.SetExecService(p.execService)
		t.SetRotateEgressHandler(p.rotateEgress)
		t.SetPolicyAudit(emit) // 被拒留痕进时间线
		t.SetFindingRecorder(p.findingRecorder)
		if as != nil {
			t.SetAssetStore(as, as.Companies())
		}
		t.SetTaskID(taskID)
		t.SetTaskClock(taskClockFrom(ctx))
		t.SetBoundedView(p.boundedView)
		t.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
		t.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
		t.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	}
	wireToolSet(tsx)
	// 领域工具 + 基础默认工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 资产覆盖度功能关闭时剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	if p.webSearch.Enabled {
		tools = append(tools, NewWebSearchTool(p.webSearch))
	}
	// 工具 schema 瘦身：把低频内置工具的 schema 从每轮工具列表里摘出去，只留名字在
	// 系统提示的 deferred 块里。planner 37 个工具的 schema 合计约 3.3 万字符，每轮都
	// 随缓存前缀发送；这几项在绝大多数轮次里不会被调用（见 plannerDeferredTools）。
	if own := deferPlannerTools(tools); len(own) > 0 {
		def.Deferred = append(def.Deferred, own...)
		def.GlobalNames = append(def.GlobalNames, own...)
	}
	// 关键态势（刚完成的意图 + 预取的完整图）改放【本轮 user 输入】(见下方 input)，system
	// 只留静态规划正文。move-out 让 system 每轮稳定、更利于缓存；代价是若单轮变长，态势可能
	// 被 compaction 压缩（planner 单轮通常短，风险低）。situational 会拼进下方 input。
	situation := ""
	if p.useBoundedView() {
		situation = renderGraphOverview(tsx.blackboardViewData(0, 20), true)
	} else {
		situation = renderGraphOverview(tsx.graphOverviewData(), false) // 有界视图关闭：预取完整态势
	}
	situational := renderTriggers(ts, triggers) + situation
	// 穷尽复核反馈：上一轮 planner 声明穷尽、但复核员判定证据不足。必须放进本轮输入，
	// 否则模型会拿着同一个结论再声明一次（它并不知道有人否过它）。
	// 任务级 deadline / 终局模式(经 ctx 注入,见 taskclock.go)。终局那一轮把任务超时
	// planner 收尾词作为【本轮操作指令】拼进本轮 user 输入(随 situational),让它只做最后
	// 目标判定、不产新意图。
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n【任务终局收尾（本轮特殊指令，覆盖上面的常规规划流程）】：" + resolveTaskTimeoutWrapup("planner")
	}
	// 本任务的工作目录 <workDir>/tasks/<taskID>，先建好。
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir, p.plannerPromptOverride())
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作约束(若有)注入系统提示,框定探索边界
	}
	system, boundary := deferredSystem(sysBody, def)
	// 单轮墙钟预算：优先用 agent 配置的 run_seconds（默认 1200s），有任务 deadline 时
	// 再夹逼到剩余时间。到点由 SDK 进收尾（不是硬杀），已想清楚的结论能落地。
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, p.roundDur)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 走记录代理留痕；载入代理 CA 验证 MITM 重签的 HTTPS 证书
		WebFetchProxy:   proxyAddr,
		WebFetchCACert:  proxyCA,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:       false, // BLADE-owned web_search（含 Brave HTML 回退）挂在 Tools 里
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL, DeepSeekSearchHeaders: p.webSearch.Headers, CommandCodeSearchBaseURL: p.webSearch.CommandCodeBaseURL, CommandCodeSearchAPIKey: p.webSearch.CommandCodeAPIKey,
		DeepSeekSearchAPIKey: p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:  p.webSearch.DeepSeekModel,
		WebSearchProxy:       p.webSearch.Proxy,
		// Bash 子命令默认走代理+信任 CA。每次工具调用现取（出口可能在 run 中途
		// 恢复/切换），不把启动瞬间的快照冻结整轮。
		BashEnvFunc: func() []string {
			addr, ca := p.proxySnapshot()
			return proxyEnv(addr, ca)
		},
		WorkingDir:    taskDir, // 本任务工作目录 <workDir>/tasks/<taskID>
		ToolOutputDir: cmdOutDir(taskDir),
		TaskOutputDir: taskOutDir(taskDir),
		MaxTurns:      p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:   maxDur,     // 0=不限;有 deadline 时=距 deadline 剩余
		// 跨唤醒共享的规划待办：让串行链在多轮之间保留（session 是新的，store 不是）。
		Todos: p.todoFor(ts.ID()),
		// 命中【本轮】步数预算→ SDK 跑收尾:把本轮已想清楚的结论落地(该派的 add_intent、
		// 能证的 prove_goal、串行链记 TodoWrite),而非停止规划——planner 之后仍会被反复唤醒。
		// clamped(被任务 deadline 夹逼)时改用 PromptByReason(见 wrapupSettlementForTask)。
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = 不发上限,由服务端默认值决定
	}
	if p.guard != nil { // 与 worker/mainagent 同一把任务 Guard（拦截规则 + RoE 范围门 + 审批）
		opts.Hooks = p.guard.Hooks()
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = PlannerSessionID(ts.ID())
	}
	// 实验功能:开启后由 noa 接管上下文压缩(归档集中在 <workDir>/noa/<SessionID> 下,持久)。
	// 与 transcript 同一个 SessionID（见 PlannerSessionID）：历史与 noa 归档同源。
	noaSession := PlannerSessionID(ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// P14 领域上下文（详见 agent/compactor.go）：规划者被压缩吃掉态势后，常驻索引仍带着
	// 目标/约束原文与活跃方向；摘要也走渗透领域模板。
	installClock := taskClockFrom(ctx)
	InstallContextManager(&opts, p.compactionWindow(), p.maxTokens(), taskDir, func() string {
		// P23：同公司其它任务的沉淀（只读先验）也进常驻索引——被压缩吃掉态势后仍看得见。
		return BuildContextIndex(ContextIndexVars{Store: ts, Clock: installClock, CompanyLines: tsx.companyKnowledgeLines(), PlaybookLines: tsx.playbookLines(), CVELines: tsx.cveLines(), HasOverview: true})
	})
	// 态势（刚完成的意图 + 完整图）现在拼进本轮 user 输入（见下方 input）。user 里还有
	// 指令 + 跨唤醒待办（todo 是模型自己的规划便签，可再生，放 user 即可）。
	// 开场白按「本轮有无具体变动」分两种：有变动 → 指向下方【实际变动】块；无变动
	// (心跳定时巡检 / hint / 恢复等) → 别谎称"图发生了变化",转而提示顺带复查在跑意图。
	lead := "刚有具体变动（见下面的【本次触发本轮的实际变动】），据此规划下一步："
	if len(triggers) == 0 {
		lead = "本轮是**定时巡检（心跳到点）/无具体变动信号**的唤醒——图不一定有新变动。顺带复查在跑意图：长时间无进展或跑偏的用 steer_work 纠偏、方向整个错的用 kill_work 止损；再判定目标、决定是否补方向："
	}
	// 本轮输入的**分段顺序**是「稳定 → 易变」，见 composePlannerInput 的说明：
	// 缓存按 token 前缀匹配，每轮都变的态势放在最前面会让整段前缀失效。
	// 上一轮结论摘要与态势同属易变段，统一放进 volatile。
	// P20：硬底线在有策略时改写成"只在策略内成立"——否则提示词自相矛盾：
	// 一边要求必须派人，一边把提出来的人全拒掉。
	staticRules := "据下面的态势，判定目标。目标已【真正达成】（已拿到目标成果/已确认目标漏洞）时用 prove_goal 逐个标记。"
	volatile := situational
	// MaxDuration 现在会在墙钟到点打断在跑工具并就地进收尾(在活 ctx 上),单轮卡死不再
	// 绕过收尾,无需外部硬 ctx 兜底。ctx 只承载 pause / kill / shutdown。
	// 规划会话形态（三选一）：
	//   ① resume 模式（BLADE_PLANNER_RESUME_SESSION=1）——每轮从 transcript 全量恢复。
	//      回滚开关；上下文会持续膨胀（任务 14 实测 26 万 token/轮）。
	//   ② 热会话（默认）——复用同一个 Session，每轮结束后把历史折叠成两条消息。
	//      system prompt + 工具 schema 的缓存前缀跨轮保持，上下文体积不随轮数增长。
	//   ③ 冷会话（热会话不可用时兜底）——每轮新建，只带上一轮结论摘要。
	// 会话所有权：本轮成功且非 resume 模式时交给热会话（跨轮持有），否则本轮结束时关闭。
	// 不能在这里 defer Close —— 成功路径会把同一个 Session 留在 p.hot 里继续用。
	var s *agentcore.Session
	keepSession := false
	if resumePlannerSession() {
		s = agentcore.NewSession(opts)
		if p.tx != nil {
			_ = s.Resume(opts.SessionID)
			volatile = "【注意】下方态势是本轮刚从探索图取的**权威快照**；历史消息里的旧态势只作背景，冲突时一律以本轮为准。\n\n" + volatile
		}
	} else if hs, hts := p.takeHotSession(); hs != nil && hts != nil {
		s = hs
		// 复用会话的工具闭包绑定的是热会话首轮的 ToolSet：本轮在那个对象上重装依赖并
		// 重置轮级状态，后续的 quota / GoalMet / 写回统计才属于「本轮」。
		tsx = hts
		wireToolSet(tsx)
		keepSession = true
		// 热会话里已经有「上一轮问过什么 + 结论」，本轮输入只需带态势与待办。
	} else {
		s = agentcore.NewSession(opts)
		if note := strings.TrimSpace(p.lastRoundNote); note != "" {
			volatile = "【上一轮结论摘要（本轮是独立会话，只承接结论，不承接旧对话；态势以下方本轮快照为准）】\n" + note + "\n\n" + volatile
		}
	}
	input := composePlannerInput(staticRules, lead, volatile, renderPlannerTodos(opts.Todos.List()))
	prevMaxIntentID := latestIntentID(ts)
	lastText := ""
	var runText string
	runText, _, err = captureRunSession(ctx, s, input,
		func(r db.Activity) {
			if r.Kind == "text" && strings.TrimSpace(r.Summary) != "" {
				lastText = r.Summary
			}
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	switch {
	case err == nil && !resumePlannerSession():
		// 成功：把历史折叠成「本轮输入 + 结论」，留作下一轮的前缀。
		p.rememberRound(ts, prevMaxIntentID, lastText)
		// 热会话的前缀用**本轮结论文本**（runText），不是逐段 emit 的片段：折叠后的
		// 两条消息必须能让下一轮读懂"上一轮想清楚了什么"。
		p.parkHotSession(s, tsx, input, runText)
		keepSession = true
	case err == nil:
		p.rememberRound(ts, prevMaxIntentID, lastText)
	case keepSession:
		// 热会话上失败：半截历史不适合当前缀，丢弃，下一轮重新建。
		p.dropHotSession()
		keepSession = false
	}
	if !keepSession {
		_ = s.Close()
	}
	// 槽位空转可见化：本轮派的意图数没跟上空闲槽位，就在时间线上留一行，写清空了几个槽、
	// 本轮派了几条、按纪律应该派几条。纪律（slotUtilizationBlock）只要求"能说清已被覆盖"
	// 才允许少派；把它记下来，操作者能直接看出是哪一轮空转，而不是只在 worker 列表里看到
	// 几个 idle 却不知道为什么。只观察、不阻断——确实已覆盖时少派是正确的，硬拦会制造垃圾。
	if emit != nil && err == nil && !tsx.GoalMet {
		if states, serr := ts.CountIntentStates(); serr == nil {
			idle := p.workers() - states["running"]
			want := idle
			if want > 4 {
				want = 4 // 与"本轮最多 4 条"的名额上限一致
			}
			if idle >= 2 && tsx.intentsRound < want {
				emit(db.Activity{Worker: "planner", Kind: "text",
					Summary: fmt.Sprintf("本轮派了 %d 条意图，收尾时仍有 %d 个 worker 空闲（在跑 %d / 共 %d）：按槽位利用率纪律应派 %d 条，未补齐时请说明哪条方向覆盖了它们",
						tsx.intentsRound, idle, states["running"], p.workers(), want)})
			}
		}
	}
	return tsx.GoalMet, tsx.Reason, err
}

// composePlannerInput 按「稳定 → 易变」拼接本轮规划输入：
//
//	① 静态规则（判定口径 + 硬底线，只随策略变化）
//	② 开场白（按触发类型三选一，同类唤醒之间是同一段文本）
//	③ 本轮态势（每轮都变，且是最大的一块）
//	④ 本轮待办（模型自己的便签）
//
// 顺序不能随意调换：provider 的提示缓存按 **token 前缀** 匹配，把每轮都变的态势放在
// 最前面，第一条消息从开头就分叉，整段前缀都吃不到缓存（实测 planner 轮首命中率 40%，
// worker 89%）。按上面的顺序，前两段在同类唤醒之间完全一致，能跨轮命中。

func composePlannerInput(staticRules, lead, volatile, todos string) string {
	return staticRules + "\n\n" + lead + "\n\n" + volatile + "\n" + todos
}

// 规划热会话的保留策略。缓存前缀（system + 工具 schema）不随轮数变化，所以复用同一个
// Session 的收益是**每轮固定**的；限制轮数与空闲时长是为了不让摘要层数与陈旧会话无限累积。
const (
	plannerHotMaxRounds = 8
	plannerHotIdleTTL   = 10 * time.Minute
)

// takeHotSession 取出可复用的热会话；没有 / 超轮数 / 超空闲 / 任务已暂停或删除时返回 nil。
func (p *Planner) takeHotSession() (*agentcore.Session, *ToolSet) {
	p.hotMu.Lock()
	defer p.hotMu.Unlock()
	// 没有配套 ToolSet 的热会话不可复用：它的工具闭包绑定在哪个对象上无从得知，
	// 无法重置本轮额度（见 Plan 里 wireToolSet 的说明）。直接丢弃、走冷路径。
	if p.hot == nil || p.hotTools == nil {
		if p.hot != nil {
			p.hot.Close()
		}
		p.hot, p.hotTools, p.hotRounds = nil, nil, 0
		return nil, nil
	}
	if p.hotRounds >= plannerHotMaxRounds || time.Since(p.hotLastUsed) > plannerHotIdleTTL {
		p.hot.Close()
		p.hot, p.hotTools, p.hotRounds = nil, nil, 0
		return nil, nil
	}
	return p.hot, p.hotTools
}

// parkHotSession 把本轮会话留作下一轮的前缀，并把历史折叠成「本轮输入 + 结论」两条。
//
// 折叠是热会话成立的前提：不折叠的话它就是一个不断变大的 transcript（等价于 resume
// 全量），上下文成本会压过缓存收益。裁到两条消息后，下一轮请求 = system + 工具 schema +
// 这两条 + 新输入，前缀稳定、体积有界。
func (p *Planner) parkHotSession(s *agentcore.Session, tools *ToolSet, roundInput, lastText string) {
	if s == nil || tools == nil {
		return
	}
	s.Reset()
	s.AppendMessages(hotPrefixMessages(roundInput, lastText)...)
	p.hotMu.Lock()
	defer p.hotMu.Unlock()
	p.hot = s
	// 连同工具绑定一起留存：下一轮必须在这个 ToolSet 上重置轮级状态，
	// 新建的 ToolSet 并不会被复用会话里的工具闭包引用。
	p.hotTools = tools
	p.hotRounds++
	p.hotLastUsed = time.Now()
}

// hotPrefixMessages 是热会话在轮间保留的最小前缀：上一轮问了什么、最后结论是什么。
// 只留结论而不是完整工具历史，是为了让下一轮的前缀短、稳定、可缓存。
func hotPrefixMessages(roundInput, lastText string) []llm.Message {
	out := make([]llm.Message, 0, 2)
	if in := strings.TrimSpace(roundInput); in != "" {
		out = append(out, llm.UserText(clipLine(in, 3000)))
	}
	if t := strings.TrimSpace(lastText); t != "" {
		out = append(out, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{llm.TextBlock(clipLine(t, 1200))},
		})
	}
	return out
}

// dropHotSession 丢弃热会话（本轮失败/被取消时用：半截历史不适合当前缀）。
func (p *Planner) dropHotSession() {
	p.hotMu.Lock()
	defer p.hotMu.Unlock()
	if p.hot != nil {
		p.hot.Close()
	}
	p.hot, p.hotTools, p.hotRounds = nil, nil, 0
}

// CloseHotSession 供任务停止/删除时释放（引擎侧调用，幂等）。
func (p *Planner) CloseHotSession() { p.dropHotSession() }

// resumePlannerSession 是回滚开关：默认 false（每轮新会话 + 结论摘要），
// BLADE_PLANNER_RESUME_SESSION=1/true 恢复旧的跨轮 transcript resume。
func resumePlannerSession() bool {
	v := strings.TrimSpace(os.Getenv("BLADE_PLANNER_RESUME_SESSION"))
	return v == "1" || strings.EqualFold(v, "true")
}

// latestIntentID 返回任务里最新的意图 id（0 = 还没有意图），用于识别本轮新增。
func latestIntentID(ts *db.ExplorationStore) int64 {
	nodes, err := ts.ListByKind(db.KindIntent, 1)
	if err != nil || len(nodes) == 0 {
		return 0
	}
	return nodes[0].ID
}

// rememberRound 生成下一轮的新会话摘要：上一轮最后结论 + 本轮新增意图 + 它们产出的事实 id。
// 只保留结论级信息，不复制工具输出/旧态势——这是把跨轮上下文从"全量 transcript"
// 换成"可续接的结论"的关键。
func (p *Planner) rememberRound(ts *db.ExplorationStore, prevMaxID int64, lastText string) {
	nodes, _ := ts.ListByKind(db.KindIntent, 30)
	var intentions []string
	factIDs := make([]string, 0, 6)
	for _, n := range nodes {
		if n.ID <= prevMaxID || len(intentions) >= 8 {
			continue
		}
		var payload map[string]any
		_ = json.Unmarshal(n.Payload, &payload)
		summary, _ := payload["summary"].(string)
		intentions = append(intentions, fmt.Sprintf("#%d %s", n.ID, clipLine(summary, 90)))
		if fids := factIDsYielded(ts, n.ID); fids != "" {
			factIDs = append(factIDs, fids)
		}
	}
	var b strings.Builder
	if t := clipLine(strings.TrimSpace(lastText), 500); t != "" {
		b.WriteString("- 上一轮结论：" + t + "\n")
	}
	if len(intentions) > 0 {
		b.WriteString("- 上一轮新增意图：" + strings.Join(intentions, "；") + "\n")
	}
	if len(factIDs) > 0 {
		b.WriteString("- 上一轮产出的事实 id：" + strings.Join(factIDs, "、") + "\n")
	}
	if b.Len() == 0 {
		b.WriteString("- 上一轮没有新增意图/结论。\n")
	}
	p.lastRoundNote = b.String()
}
