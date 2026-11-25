package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/compaction"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/guard"
	"github.com/lin9527ya/blade/intercept"
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
	hotMu  sync.Mutex
	planMu sync.Mutex // serializes Plan; CloseHotSession waits for the active round
	hot    *agentcore.Session
	// hotTools 是热会话首轮创建、其工具闭包所绑定的那个 ToolSet。Session 的 registry
	// 在 NewSession 时固定，所以后续轮次必须**在这个对象上**重置轮级状态；对每轮新建的
	// ToolSet 调用 BeginRound 不会影响模型实际调用的工具。
	hotTools            *ToolSet
	hotCleanup          func() // resources owned by the retained registry
	hotRounds           int
	hotLastUsed         time.Time
	hotSurface          string
	hotExpID            int64
	hotLastNodeCursor   int64
	hotLastChangeCursor string
	proxyAddr           string                  // recording proxy for WebFetch (empty = direct)
	proxyCACert         string                  // recording proxy's CA cert path (HTTPS verify)
	proxyFn             func() (string, string) // dynamic resolver (proxy-pool failover); nil = static
	proxyFailureFn      func(proxyURL string, err error)
	webSearch           WebSearchOpts           // web_search tool backend selection (off by default)
	workDir             string                  // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints   func() bool             // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn      func() bool             // resolver: use non-streaming (Complete) path? (nil = streaming)
	// execService（P13）由 host 注入：planner 也能查看/发起长任务执行。
	execService   ExecService
	noaEnabledFn  func() bool          // resolver: use experimental noa compaction? (nil = off)
	boundedView   func() bool          // nil = 有界视图（默认）
	notifyWorkers func()               // 派发新意图后立即唤醒 worker 调度器
	maxTokensFn   func() int           // resolver: per-reply output cap (nil/0 = send no cap)
	cacheOpts     cacheOptionResolvers // prompt-cache TTL/retention & thinking-preservation options
	compactor     *Compactor           // cold-node compaction (§7); nil = disabled
	// compactionLLM, when set, supplies an independent provider for context
	// summarization (see InstallContextManager). nil = use this agent's provider.
	compactionLLM func() (CompactionLLM, bool)
	// llmVerifier optionally wraps the default verifyWithLLM with host accounting
	// (BeginLLMCall/EndLLMCall, llmrec attribution). nil = use verifyWithLLM directly.
	llmVerifier func(ctx context.Context, digest string) (string, error)
	// guard is the SAME per-task Guard workers use (intercept rules + LLM judge +
	// RoE scope gate). Planner can call Bash/default tools during its light opening
	// probe; without this it bypassed every command-level boundary.
	guard *guard.Guard

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu     sync.Mutex
	todos      map[int64]*actool.TodoStore
	todoLoaded map[int64]bool // 每进程只从 DB 恢复一次；模型主动清空后不再回填
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{
		prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns,
		todos: map[int64]*actool.TodoStore{}, todoLoaded: map[int64]bool{},
	}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// verifyWithLLM asks the task provider for a tool-less, isolated review of an
// evidence digest. It receives only the digest, not the worker transcript, so the
// worker's narration cannot sway it. It is NOT a separate model/provider; the host
// may wrap it with accounting via SetLLMVerifier.
func (p *Planner) verifyWithLLM(ctx context.Context, digest string) (string, error) {
	if p == nil || p.prov == nil {
		return "", fmt.Errorf("verifier model unavailable")
	}
	temp := 0.0
	msg, _, _, err := p.prov.Complete(ctx, llm.CompletionRequest{
		System:          []string{`你是授权渗透测试系统的只读证据复核员。只依据用户提供的证据摘要，判断事实是否足以支持其结论；不要补充未给出的事实，不要执行任何命令。输出 JSON：{"verdict":"supported|weak|insufficient|contradicted","reason":"一句话"}。`},
		DynamicBoundary: 1,
		Messages:        []llm.Message{llm.UserText(digest)},
		MaxTokens:       500,
		Temperature:     &temp,
		Thinking:        "disabled",
		PromptCacheKey:  "planner-evidence-verifier",
	})
	if err != nil {
		return "", err
	}
	return msg.Text(), nil
}

// SetRoundSeconds 注入单轮墙钟预算（秒；0 = 不限）。没有它，一次跑飞的规划会一直占着
// 这个任务（只有任务总 deadline 能兜底，而很多任务并没有 deadline）。
func (p *Planner) SetRoundSeconds(n int64) { p.roundDur = time.Duration(n) * time.Second }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetCompactionLLM supplies an optional independent provider for context
// summarization. nil/false = keep using this agent's own provider.
func (p *Planner) SetCompactionLLM(fn func() (CompactionLLM, bool)) { p.compactionLLM = fn }

// SetLLMVerifier wraps the default one-shot evidence review with host accounting.
// The wrapper must call VerifyWithLLM for the actual model call; it exists so the
// server can attach BeginLLMCall/EndLLMCall and llmrec attribution without the
// agent package depending on the engine.
func (p *Planner) SetLLMVerifier(fn func(ctx context.Context, digest string) (string, error)) {
	p.llmVerifier = fn
}

// VerifyWithLLM exposes the default one-shot evidence review to the host wrapper.
// It deliberately does not invoke the SetLLMVerifier hook, avoiding recursion.
func (p *Planner) VerifyWithLLM(ctx context.Context, digest string) (string, error) {
	return p.verifyWithLLM(ctx, digest)
}

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

// SetCacheOptions wires per-run resolvers for CacheTTL, PromptCacheRetention, and PreserveThinking.
func (p *Planner) SetCacheOptions(ttlFn, retentionFn func() string, preserveThinkingFn func() bool) {
	p.cacheOpts = cacheOptionResolvers{cacheTTLFn: ttlFn, retentionFn: retentionFn, preserveThinkingFn: preserveThinkingFn}
}

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

// SetProxyFailureHook registers a host callback invoked when native HTTP tools
// hit a transport error on the active egress proxy.
func (p *Planner) SetProxyFailureHook(fn func(proxyURL string, err error)) { p.proxyFailureFn = fn }

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

// SetNotifyWorkers wires the worker-dispatcher wakeup callback fired right when
// planner commits new intents via add_intent.
func (p *Planner) SetNotifyWorkers(fn func()) { p.notifyWorkers = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

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

// restorePlannerState loads the cross-process scratchpad once per Planner
// instance. Norma's TodoStore has no public setter, so the stored list is fed
// back through the same TodoWrite tool the model uses; that keeps one code path
// for validation and status values.
func (p *Planner) restorePlannerState(ctx context.Context, ts *db.ExplorationStore, taskID, expID int64) *actool.TodoStore {
	store := p.todoFor(expID)
	if taskID <= 0 {
		return store
	}
	p.todoMu.Lock()
	if p.todoLoaded[expID] {
		p.todoMu.Unlock()
		return store
	}
	p.todoLoaded[expID] = true
	p.todoMu.Unlock()

	state, found, err := ts.LoadPlannerState(taskID)
	if err != nil {
		// 读取失败不是「没有状态」：放开 loaded，让之后的轮次再试。
		p.todoMu.Lock()
		p.todoLoaded[expID] = false
		p.todoMu.Unlock()
		log.Printf("[planner] 读取持久化规划状态失败（本轮按空状态继续，下轮重试）: %v", err)
		return store
	}
	if !found {
		return store
	}
	if len(state.Todos) > 0 {
		todos := make([]db.PlannerTodoItem, 0, len(state.Todos))
		for _, it := range state.Todos {
			content := strings.TrimSpace(it.Content)
			if content == "" {
				continue
			}
			todos = append(todos, db.PlannerTodoItem{Content: content, Status: it.Status})
		}
		if len(todos) > 0 {
			payload, _ := json.Marshal(map[string]any{"todos": todos})
			if _, err := store.Tool().Call(ctx, payload, nil); err != nil {
				// 回填工具失败也不能标为已恢复：防止本轮结束时用空清单覆写库内记录。
				p.todoMu.Lock()
				p.todoLoaded[expID] = false
				p.todoMu.Unlock()
				log.Printf("[planner] 恢复规划待办失败（本轮不覆盖持久化状态，下轮重试）: %v", err)
			}
		}
	}
	if strings.TrimSpace(p.lastRoundNote) == "" && strings.TrimSpace(state.LastRoundNote) != "" {
		p.lastRoundNote = state.LastRoundNote
	}
	return store
}

// savePlannerState snapshots the scratchpad after a round. A crash mid-round can
// lose that round's TodoWrite, but every completed round is durable; the point is
// to survive process restart, not to make the tool call itself transactional.
func (p *Planner) savePlannerState(ts *db.ExplorationStore, taskID, expID int64, store *actool.TodoStore) {
	if taskID <= 0 || store == nil {
		return
	}
	p.todoMu.Lock()
	loaded := p.todoLoaded[expID]
	p.todoMu.Unlock()
	if !loaded {
		log.Printf("[planner] 跳过保存规划状态：持久化状态尚未成功恢复，避免空状态覆盖已有记录")
		return
	}
	items := store.List()
	todos := make([]db.PlannerTodoItem, 0, len(items))
	for _, it := range items {
		todos = append(todos, db.PlannerTodoItem{Content: it.Content, Status: string(it.Status)})
	}
	note := p.lastRoundNote
	if len(note) > 8000 {
		note = clipLine(note, 8000)
	}
	if err := ts.SavePlannerState(taskID, todos, note); err != nil {
		log.Printf("[planner] 保存规划状态失败: %v", err)
	}
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
//	"facts_batch" — N ordinary fact writes were coalesced; Summary = most recent.
//	"assets_batch" — N asset registrations/updates were coalesced; Summary = most recent.
//	"triggers_dropped" — N older triggers exceeded the queue cap and were not expanded.
//
// TriggerFactRef keeps branch identity through ordinary-fact coalescing.
type TriggerFactRef struct {
	IntentID int64
	FactID   int64
	Summary  string
	AssetIDs []int64
}

type TriggerEvent struct {
	Kind     string
	IntentID int64
	FactID   int64
	Detail   string
	AssetIDs []int64  // Kind=="fact"/"contradiction"/"negative_fact"/"supersede" 专用
	State    string   // Kind=="done" 专用：本次结算终态（done/negative/no_output/blocked/…）
	Summary  string   // Kind=="cancelled" 专用：删除前捕获的意图摘要（真删除后节点已不存在，无法再查）
	Goals    []string // Kind=="goal" 专用：本次 set_goals 新增的目标文本（1 条或多条）
	OldGoal  string   // Kind=="goal_edited" 专用：修改前的目标文本
	NewGoal  string   // Kind=="goal_edited" 专用：修改后的目标文本
	Hints    []string // Kind=="hint" 专用：本次 add_hint 新增的提示文本（1 条或多条）
	// Count 是 facts_batch/assets_batch/triggers_dropped 的聚合数量。
	Count int
	Facts []TriggerFactRef // facts_batch: bounded list of every fact in this batch
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
	const maxRenderedTriggers = 24
	// 触发队列按时间追加，最新事件（done/finding/blocked 等）通常更值得看；
	// 超限时优先保留目标/发现/能力事件，并在同优先级内保留最新；数量在文末说明。
	omitted := len(evs) - maxRenderedTriggers
	if len(evs) > maxRenderedTriggers {
		evs = selectTriggerWindow(evs, maxRenderedTriggers)
	}
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
		case "capability_fact", "lead_fact":
			label := map[string]string{"capability_fact": "高价值能力事实", "lead_fact": "旁路线索事实"}[ev.Kind]
			b.WriteString(fmt.Sprintf("\n- 意图 #%d 写回了%s #%d：%s。这是高优先级发散信号，优先判断是否立即补方向/并行验证。", ev.IntentID, label, ev.FactID, clipLine(ev.Summary, 240)))
		case "foothold", "credential":
			label := map[string]string{"foothold": "据点", "credential": "凭据"}[ev.Kind]
			b.WriteString(fmt.Sprintf("\n- 意图 #%d 记录了新的%s #%d：%s。立即检查能否据此并行推进横向/提权/利用，不要等下一轮普通事实批次。", ev.IntentID, label, ev.FactID, clipLine(ev.Summary, 240)))
		case "scope_request":
			b.WriteString(fmt.Sprintf("\n- 意图 #%d 提交了扩面请求 fact #%d：%s。先核对归属证据，再决定是否 add_task_scope / add_company_scope；不要直接测试范围外资产。", ev.IntentID, ev.FactID, clipLine(ev.Summary, 240)))
		case "fact", "contradiction", "negative_fact", "supersede":
			label := map[string]string{
				"fact": "事实", "contradiction": "矛盾事实", "negative_fact": "否定事实", "supersede": "替代事实",
			}[ev.Kind]
			head := fmt.Sprintf("\n- 意图 #%d 写回了%s", ev.IntentID, label)
			if ev.FactID > 0 {
				head += fmt.Sprintf(" #%d", ev.FactID)
			}
			head += "：" + clipLine(ev.Summary, 240)
			if len(ev.AssetIDs) > 0 {
				head += fmt.Sprintf("（相关资产：%v）", ev.AssetIDs)
			}
			b.WriteString(head + "。请结合证据判断是否需要补方向、复核或停止该分支。")
		case "asset":
			b.WriteString("\n- 新资产登记：" + clipLine(ev.Summary, 240) + "。请判断是否需要把该资产/邻域纳入探索方向。")
		case "facts_batch":
			n := ev.Count
			if n <= 0 {
				n = 1
			}
			b.WriteString(fmt.Sprintf("\n- worker 批量写回了 %d 条普通事实（最近一条：%s）。概览只是摘要，独立分叉按下方 id 核对，全文用 node_detail / list_facts。", n, clipLine(ev.Summary, 240)))
			for _, fact := range ev.Facts {
				b.WriteString(fmt.Sprintf("\n  · fact #%d（意图 #%d，资产 %v）：%s", fact.FactID, fact.IntentID, fact.AssetIDs, clipLine(fact.Summary, 160)))
			}
		case "assets_batch":
			n := ev.Count
			if n <= 0 {
				n = 1
			}
			b.WriteString(fmt.Sprintf("\n- 资产图发生 %d 次批量登记/更新（最近一次：%s）。具体资产用 list_assets / asset_neighbors 查看，不要只按这条摘要判断覆盖。", n, clipLine(ev.Summary, 240)))
		case "triggers_dropped":
			n := ev.Count
			if n <= 0 {
				n = 1
			}
			b.WriteString(fmt.Sprintf("\n- 另有 %d 条较早触发因队列上限未展开；请用 blackboard_view 的 changes 游标或 blackboard_query / list_facts 补查相关历史；概览窗口不保证包含所有丢弃事件。", n))
		case "proposal":
			b.WriteString(fmt.Sprintf("\n- 意图 #%d（%s）提交了意图提案 fact #%d：%s。提案只是候选方向，不会自动成为意图；先核对依据，再决定是否 add_intent。", ev.IntentID, intentSummary(ts, ev.IntentID), ev.FactID, clipLine(ev.Summary, 240)))
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
				detail = "进入收官期：优先收敛高确定性链路与补齐证据；仅直通目标的新高危发现可开短意图。"
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
	if omitted > 0 {
		b.WriteString(fmt.Sprintf("\n- 另有 %d 条较早触发未展开；概览仍是窗口，必要时用 blackboard_query / list_facts 补查。", omitted))
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
	header := "\n\n【本轮态势（完整 graph_overview 预取，等同你调用该工具的返回；有界视图已关闭。更早的知识用 list_facts/search_all_worker_traces 翻页）】：\n"
	if bounded {
		header = "\n\n【本轮态势（有界黑板视图，等同 graph_overview 的默认返回；totals 是真实总数，列表只是窗口。需要折叠区/继承任务/AD 图等完整态势时调 graph_overview(detail=true)；更早的知识用 list_facts/search_all_worker_traces 翻页）】：\n"
	}
	return header + string(b)
}

func renderGraphDeltaOverview(data map[string]any) string {
	b, err := json.Marshal(SanitizeJSONStrings(data))
	if err != nil {
		return ""
	}
	return "\n\n【本轮增量态势（热会话延续：之前全量黑板快照已在上方对话中；以下仅含游标以来的变更、最新统计/目标/提示与活跃意图清单，如需未列节点详情调 node_detail 或 graph_overview）】：\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中间产物输出规约
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
// PlannerPromptBody 返回内置 planner 正文（DB agent_prompts 的播种与迁移用）。
func PlannerPromptBody() string { return plannerDefaultTmpl }

// plannerScopeRequestBlock is code-owned: worker scope-expansion requests must be
// reviewed rather than silently ignored, and a denial must still leave an audit trail.
const plannerScopeRequestBlock = `

【扩面请求】
- [扩面请求] 不是授权：先 node_detail 核验归属证据，充分时才 add_task_scope / add_company_scope；不足时在本轮结论说明缺口，不擅自扩面，不重复制造拒绝事实。`

const plannerDefaultTmpl = `你是一个网络安全平台授权渗透测试系统的"规划者"，被频繁唤醒（图一变就唤醒）。职责：读态势 → 判目标 → **只在确有未被覆盖的新方向时**补充探索意图。你是规划者、不是执行者：默认产物是【生成/说清意图】或【判定目标】，不做扫描、目录/参数枚举、爆破、注入/越权/利用。**唯一例外**：为把关键分叉说具体而做的极少量只读检查；若该检查**直接复现**了漏洞（有可重复的命令/请求与回显），可以用 report_finding 记录，并在 evidence 开头注明「规划者只读复现」——仅凭指纹/版本/推断不算。其余观察通过 add_intent 的 observation 作为待验证观察交接给 worker 证实，不能写成事实或漏洞。

任务目标：{{.Goal}}

**本轮该产出几个意图（先想清楚这条）**：
- **硬底线（最高优先）**：只要【目标未达成】且【当前没有任何 running 意图，也没有可领的 open 意图】（claimable_intents=0 且 running_intents 为空），本轮就【必须】产出至少一个向目标推进的意图——没有在跑的 work 可等、也没有在排队的方向时，产出 0 意图=任务停摆；哪怕已知方向都只在 recent_done 里，也要据下面 done/exhausted/blocked 的判断另开一条或续派一条。**唯一例外——真实的外部等待**：若你已核对授权范围、现有事实与全部 recent_done，确实不存在任何可执行且未被覆盖的实验，只能等某个明确的外部条件（目标恢复/上线、凭据或授权下发、客户确认、外部依赖状态变化），本轮可以 0 意图；但必须说清【等什么条件、该条件变化后派什么、在它变化前不重复做什么】，并用 TodoWrite 记成待办。不要用「等外部条件」给低价值重复派单打掩护。**若 frontier_open>0 但 claimable_intents=0**，说明 open 意图被 strict 活跃前置/依赖环/暂停挡住：本轮是解阻轮，先看 graph_health 与相关 node_detail，拆链、补前置或改走独立方向，不能以"已有 open 意图"为由产出 0 意图。
- 硬底线之外，**产出 0 个意图是正常结果，但要有正当理由**（不是"少派更稳"的默认）：①**已覆盖**——你想到的方向都已被仍在 open/running 的意图处理（换措辞重复生成已存在的意图是严重错误）；②**等待依赖**——下一步依赖仍在 open/running/paused 的前置产出、而它还没出来（此时硬派会让下游拿不到前置而空转，应等下次唤醒图更新后再派）；前置已经进入任何终态时不算等待，按 fail-open 继续推进或改方向。
- 反过来：确有【未覆盖、且不依赖在跑 work】的新方向，或目标未达成且范围内仍有未测面，就该派——别把 0 意图当偷懒的默认。

**每次唤醒的决策流程**：

1. **完整态势已附在本提示下方**（就是 graph_overview 的返回，无需再调它）：task（原始标题+目标/根节点）、资产计数、goals+状态、open/running/recent_done 意图、无端点的站点（若态势提供该字段，提示可能待探的方向）、facts（探索事实数，与漏洞是两类）、recent_facts（{id,summary,confidence?}）。
   - **范围**：探索节点（goals/意图/facts/findings）只含本任务；资产按本任务 「asset_scope_policy」 过滤（target=目标所属全资产：同根域、同公司跨根域、本任务发现/锚定资产；exact=仅明确登记主机），见态势 「asset_scope」。范围外资产不要派单。
   - **血缘**：每个意图带 parents（上游：派生自哪些事实/意图）和 yields（下游：产生了哪些事实/发现），recent_facts 每条带 from_intent；据此理解"哪些事实来自哪个方向、能否综合出新方向"。
   - **否定/存疑观察**（recent_facts 里"端口关闭/不可注入"等）是 worker 的观察、不是定论：采信前先 node_detail(id) 看 evidence——evidence 扎实、confidence=observed 且手段已穷尽的才视为该方向暂时封住；evidence 缺失、只是"看起来像/只探一次"、或 confidence=inferred 的，按【尚未探明】处理。**是否重新实验由证据条件决定，不是固定复核次数**：身份/凭据、入口/端点/参数、手法/载荷、目标版本、网络路径/可达性，或与旧结论矛盾的新证据——任一发生变化，就可以且应该派新实验；条件没变、旧证据是 observed 且手法已穷尽时，不要只换措辞重派。否定只对当时测试过的条件成立，不能推广成整条路线永久封闭。重派时 parent_ids 挂到旧结论或新证据上，并用 hypothesis/stop_condition 写清「这次变了什么、要验证/停止什么」，避免循环；旧结论的 limitations（适用主机/端口/身份/版本）就是判断条件是否变化的依据。**同一次条件变化只值得一轮新实验；新实验仍无新信息就停止该分支并写清阻断点（blocked/open_proof_gap），不要继续换 payload 空转。**
   - **要更深细节才按需调**：list_facts（分页，最新在前，默认 20，可 q 过滤、before 翻页，带 total/has_more）、list_findings（全部漏洞）、node_detail(id)（完整证据/详情；列表/recent_facts 只给摘要）、list_assets（参数 dsl/type/id/ids/limit/offset；公司过滤用 DSL 字段 company_id；无 q/task_id 参数）、asset_neighbors。资产范围以 「asset_scope」 为准（policy/roots/companies）：target 策略下同根、同公司跨根域、本任务发现/锚定的资产都可直接派单；不得扩到未归因的第三方根域，也不得自行改策略（策略由操作者在任务设置切换）。

2. **判目标（核心职责）**：goals 字段已含目标与状态；对已被某发现/事实证明的未达成目标，调 prove_goal(goal_id, evidence_id, reason) 标 met。**当你标记的恰是最后一个未完成目标时，系统自动判定整个任务完成**——收官只由逐个 prove_goal 驱动，没有别的"一键完成"手段。
   - ⚠️ **量化验收核对（严禁提前盖章）**：目标含可量化条件（覆盖度达 X%、达到指定数量、获得某权限）时，prove_goal 前【必须】核对上方 graph_overview 的实测值（coverage.pct、findings_total 计数等）：未达标就【禁止】prove_goal，改派意图补差；不得以"大体达成/核心已拿下"为由提前标 met。例：要求覆盖度 100% 而实测 coverage.pct=40% → 未达成，继续派补测意图。

3. **（可选，轻量自检）探测理解**：当仅凭态势无法把关键分叉说具体时，可以做少量**只读、低成本**验证（通常 1–2 条命令即可；如响应头、连通性、公开页面/指纹）。以「信息增量」决定何时停：一旦能把关键分叉写成意图就停手，不要为凑固定次数继续检查。**产物通常是一句更精准的意图描述**；若检查**直接、可复现**地证实了一个漏洞，可用 report_finding 记录（evidence 必须是最小复现命令与关键回显，并注明「规划者只读复现」；指纹/版本推断不算），否则把观察写进意图的 observation，交给 worker 证实。不替代 worker 做扫描、目录/参数枚举、爆破、注入/越权/利用——那些是 worker 的重活，写成意图派下去。
   - 已有 worker fact 时优先基于事实判断；若确实需要确认一个关键分叉（例如某接口是否可达），仍可做极轻量只读检查，但不要重复 worker 已经做过的探测。
   - 发现自己在"深入查证"而不是"快速定方向"时立刻停手，写成意图。
   - 能从现有事实/态势判断的，不必探测。

4. **决定补哪些新方向**：**这里的"克制"只指【不重复已存在的意图】，不是"能少派就少派"**——目标未达成时，默认追问是"为逼近目标，还有哪些更深、更狠、尚未覆盖的打法"，而不是"是否可以收尾"。意图是【开放的探索方向】（不是固定类型/菜单），结合已知事实、资产、目标自判方向，逐一与 open + running + recent_done 比对：
   - 已有 open/running 覆盖 → 不再生成（正在处理）。
   - 在 recent_done 里出现过 → **先看该意图的 state（每条都带）分辨怎么停的，再决定**：
     · **done（执行正常结束）**：先看 yields 和证据判断具体问题是否已验证；没有结论不等于已覆盖。有新事实、独立分叉或验证缺口时可续派，说明这次要解决什么；不要只换措辞重复已经验证的同一个问题。
     · **no_output（正常结束但未写回任何新产出）/ negative（实证否定）**：no_output 不等于已覆盖——先用 get_worker_trace / get_worker_output 看是完全没跑成还是已有发现未落图，再据此续派或换打法；negative 按上方"否定/存疑观察"判断：条件已变或证据不足就派新实验（写清这次变化与停止条件），条件未变且证据扎实就尊重结论，不要机械复核。
     · **exhausted（预算耗尽、探到一半被掐断，只写回部分）/ blocked（模型或网络失败、基本没探成）**：都是中途没善终、信息不全——先用 get_worker_trace / get_worker_output 看它实际做到哪、卡在哪，再从下列里选：接近突破被预算掐 → 派"接上次进度继续"；纯外部故障没跑成（blocked 常是）→ 直接重派同方向；每次卡同一处 → 换打法/方向。依据永远是 trace 里的真实进度，不是 state 本身。
   - 完全无任何意图覆盖的全新方向 → 生成。
   - 所有已知方向都被仍在 open/running 的意图覆盖 → 不生成、直接结束（有在跑/在排队的 work，等它们推进）；但若只剩 recent_done 覆盖、已无 open/running 而目标未达成 → 按顶部硬底线必须另开或续派。
   - **深度优先于覆盖度**：coverage 是下限/验收项、不是探索目标本身；发现高价值入口（可能通向 RCE/提权/数据外泄）后，优先派意图把那条路【往深打穿】，而不是为拉平覆盖度去铺广、逐个资产浅测。
   - **保持路线多样、别过早收敛**：目标未达成时，若现有意图都挤在同一条路线/入口，而存在【本质不同】的未覆盖方向（另一入口面/另一类资产/另一条利用链），优先补那条分歧方向，而不是在同一线上加同义意图（看实质差异，不看措辞）；若该分歧方向已被现有意图覆盖，仍不生成。理想是至少 2–3 条机理不同的路线并存（如"从上传链打"与"从认证绕过打"），资产面广或有多组互不依赖的入口时可以并行更多独立路线，不必刻意压到 2–3 条；某条交出【目标逼近】的证据后才把资源集中过去。**但多样性永远服从顶部【操作约束】**：被约束排除的入口面/端口/主机/操作，即使本质不同也绝不生成意图。
   - **广度查漏与深度裂变**（思路启发，不改滚动节奏）：① **查漏三对照**：排查整类空白资产/入口区、recent_leads 中断掉尚未立项的线索尾巴、以及因缺登录态/前置条件被跳过的高价值面（标明前提待解锁，勿直接丢弃）；② **裂变八算子**：盯住单条 fact 从「同族外推（旧版/移动端/管理端/导出）、数据流追踪（落盘/缓存/异步/二次渲染）、同芯异壳（换入口/方法/Content-Type/协议通道）、同物异态（同一对象的读/改/删/分享/并发）、边界穿越（前端→网关→解析器→DB→内网解析差）、时间折叠（重放/延后/单包并发）、跨层降维（媒体库/反代/原生组件/客户端/云编排）、模式迁移（已验证手法复用到兄弟接口或兄弟资产）」推演新方向；③ **盲态建预言机（Oracle）**：遇 403/500/无回显黑盒时先构造微小扰动差分或时间/OOB 侧信道探测不变量破坏，不做相同 payload 盲重试。
   - **防误删红线（四维等价判定）**：严禁仅凭标题相似或落在同一接口就用 drop_intent 合并或删除细微差异。仅当「① 目标对象/接口 + ② 载体（Query/Body/Header/Cookie）/HTTP方法 + ③ 身份/前置状态 + ④ 验证机理/攻击假设」四维全同或被严格包含时才算重复；任一维度不同均属独立分叉，拿不准默认保留并用 priority 排先后。确需合并类似排队方向时，必须先将独特测试点写进保留方向的 summary/hypothesis 再 drop_intent。

   **串行利用链：分步派，别拆成并行。** 强依赖串行链（①→②→③，后一步依赖前一步的实际产出）：不要一次性并行下发（下游拿不到还不存在的前置只会重复/空转）；用 TodoWrite 把整条链记成待办（每步一条），本轮只派"前置已满足"的那步（通常第一步），待它产出 fact 后下次唤醒（提示会带上待办清单）再派下一步并把已满足的标 completed。"同一件事"别拆两条（"确认触发点"和"触发触发点"是同一步）；只有【平行、互不依赖】的维度（如枚举多个不相关端点）才用多意图并行。

5. **提交**：用【一次】add_intent 批量提交筛出的新方向（intents 数组；默认按价值选 2–6 个，覆盖面广或多组互不依赖的资产/入口时可到 8 个，不要为凑数硬塞，也不要逐条多次调）：
   - **summary**：一句话自然语言描述该方向（测试目标完整地址 + 做什么 + 为什么），不套固定分类；去重主要靠它与已有意图比对。**重试/复核已有方向时，必须写清这次变了什么**（身份/入口/手法/版本/新证据），并用 hypothesis/stop_condition 写明验证与停止条件；只换措辞的重复视为已覆盖。规划者自检看到、但未直接复现的观察，写进 observation，不要塞进 summary 当已观察事实。
   - **asset_ids**：本方向要测试/攻击的目标资产 id（尽量传，0/1/多个，来自 list_assets）——只要方向围绕具体资产（站点/接口/参数/主机）可在已知 id 时附上，用于覆盖去重、连入资产链路，跨多资产就都传；纯全局侦察无具体资产才留空。
   - **parent_ids**：本方向由哪些上游节点综合得出（可选，0/1/多个）——多个事实结合产生一个意图就都传，派生自某事实/发现也传其 id（意图前置用 depends_on，不放进 parent_ids），顶层全新方向留空。

不重复、不硬凑；但目标未达成、又有未覆盖且更深的打法时，该派就派。简洁、聚焦、高效。`

func splitPlannerPromptStatic(raw, goal, dataDir, workDir, codeStaticTail string) (staticPart, dynamicPart string, ok bool) {
	hasPerTaskVars := strings.Contains(raw, "{{.Scope}}") || strings.Contains(raw, "{{.AssetSummary}}")
	normalized := strings.Replace(raw, "任务目标：{{.Goal}}", "任务目标见末尾动态上下文段。", 1)
	if !hasPerTaskVars && !strings.Contains(normalized, "{{.Goal}}") {
		if out, err := renderTmpl(normalized, PlannerVars{DataDir: dataDir, Now: nowStr()}); err == nil {
			staticPart = out + promptExtras("planner") + codeStaticTail
			if g := strings.TrimSpace(goal); g != "" {
				dynamicPart = "当前任务目标：" + g
			}
			dynamicPart += artifactSpec(workDir)
			return staticPart, dynamicPart, true
		}
	}
	return "", "", false
}

// plannerSystemParts splits the planner prompt into the cross-task static blocks
// (placed BEFORE DynamicBoundary) and the per-task Goal + workDir blocks (placed
// AFTER DynamicBoundary) so tasks share the cached static tools + system prefix.
func plannerSystemParts(goal, dataDir, workDir string) (staticPart, dynamicPart string) {
	codeStaticTail := coldArchiveBlock + egressDisciplineBlock(false) + hintLifecycleBlock + cveLookupBlock +
		untrustedDataBlock() + blackboardViewBlock + scopeExpansionBlock + structuredIntentBlock
	fullVars := PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()}

	// ① 全局覆盖（agent_prompts 当前版本）。先剥离标准「任务目标：{{.Goal}}」使主干留在
	//    DynamicBoundary 之前的跨任务缓存前缀里；只有嵌入其它逐任务变量时才退回动态段。
	if PromptOverride != nil {
		if t, ok := PromptOverride("planner"); ok && t != "" && strings.TrimSpace(t) != strings.TrimSpace(plannerDefaultTmpl) {
			if sPart, dPart, splitOK := splitPlannerPromptStatic(t, goal, dataDir, workDir, codeStaticTail); splitOK {
				return sPart, dPart
			}
			if out, err := renderTmpl(t, fullVars); err == nil {
				return codeStaticTail, out + promptExtras("planner") + artifactSpec(workDir)
			}
			log.Printf("[planner] 全局 planner 提示词渲染失败，回退内置模板")
		}
	}
	// ② 内置默认正文：将任务目标移到 DynamicBoundary 之后，静态主干完全跨任务一致。
	if sPart, dPart, splitOK := splitPlannerPromptStatic(plannerDefaultTmpl, goal, dataDir, workDir, codeStaticTail); splitOK {
		return sPart, dPart
	}
	return plannerDefaultTmpl + promptExtras("planner") + codeStaticTail, artifactSpec(workDir)
}

func plannerSystem(goal, dataDir, workDir string) string {
	vars := PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()}
	body := ""
	// ① 全局覆盖（agent_prompts 当前版本）。内容仍等于出厂正文时走内置默认。
	if PromptOverride != nil {
		if t, ok := PromptOverride("planner"); ok && t != "" && strings.TrimSpace(t) != strings.TrimSpace(plannerDefaultTmpl) {
			if out, err := renderTmpl(t, vars); err == nil {
				body = out
			} else {
				log.Printf("[planner] 全局 planner 提示词渲染失败，回退内置模板: %v", err)
			}
		}
	}
	// ② 内置正文。这里直接渲染，不走 renderSystem——否则会二次套用覆盖与 extras。
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
		coldArchiveBlock + egressDisciplineBlock(false) + hintLifecycleBlock + cveLookupBlock +
		artifactSpec(workDir) +
		untrustedDataBlock() + blackboardViewBlock + scopeExpansionBlock + structuredIntentBlock
	return out
}

// blackboardViewBlock is code-owned: the editable body predates the bounded view
// and still says the situation is the complete graph. Keep the distinction in
// front of the model without rewriting operator-edited prompts.
const blackboardViewBlock = `

【读取黑板】
- 概览是窗口摘要，即使 detail=true 也不是全部原文。总数/读取状态与列表分开看；没显示不等于不存在。独立分叉看 recent_leads / branch_review_facts，leads_open > 20 时用 list_leads 分页；全文用 node_detail；资产上下文用 asset_dossier，历史用 blackboard_query / route_dossier。
- done 只表示执行结束，不证明方向已验证或整条路线已排除。`

// scopeExpansionBlock 是资产范围扩张的**代码持有**纪律。ARTEX 把 add_task_scope /
// add_company_scope 作为规划层的核心扩面入口；BLADE 保留了 RoE 强制门，因此必须把
// 「什么证据下可以扩、扩了就等于授权」写死在系统提示里，避免模型凭名称相似乱扩。
const scopeExpansionBlock = `

【范围】
- asset_scope_policy 是当前边界。扩面即授权：整域/整公司扩面须有可审计的归属证据（whois/证书/ICP/同一控制台等）；先 add_company_scope 定义公司归属，公司范围还需 add_task_scope(kind=company) 才纳入本任务。reason 写清依据。
- 新发现资产可 insert_assets 登记，但不得凭名称相似或猜测测试未归因的第三方。授权、审批和操作约束优先。`

// structuredIntentBlock 是意图契约/依赖/按需查询的代码持有说明。正文可能被旧库
// 或用户定制覆盖，但新字段和新工具必须始终能被 planner 发现。
const structuredIntentBlock = `

【方向与连续研究】
- add_intent 只需明确 summary；asset_ids / parent_ids 和 observation / hypothesis / route / mechanism / success_criteria / stop_condition / produces / acceptance 都可选。规划者自己看到但未复现的观察放 observation（worker 会看到「待验证，不是证据」），不要写成事实或漏洞。知道资产和事实时附上；不要为填表延迟一个明确的方向。父节点用事实/发现/据点/凭据/AD/云对象，意图前置用 depends_on。
- depends_on 默认 advisory，仅轻度排序；strict 才等待活跃前置，终态 fail-open，不是安全锁。解阻用 set_dependency_mode / drop_dependency / reopen_intent，写 reason，不改授权。
- worker 的 [意图提案] 先核对再 add_intent；一条事实有子意图不代表所有分叉已接手，按 lead id 逐个处理；可选 lead_ids 关联，原文与唯一父节点线索明确一致时自动关联，不为追踪强制填表。
- route_dossier 看路线，search_precedents 取历史先验（不是本次结论）；campaign 提供策略上下文，不授权。
- attention_oldest_leads / attention_oldest_facts / attention_oldest_unresolved 是全图召回窗口（各最多 5 条，不是全量）：主窗口没看到的老线索、未接手事实、blocked/exhausted/open_proof_gap 先看这里。changes 中带 summary/polarity/confidence/status 或 detail_required=true 的节点/漏洞变更，先用 node_detail 或 list_findings 核对再改判断。
- 需要跨轮保留的研究判断用 TodoWrite：记录待验证分叉、等待原因及什么新证据会改变结论，不必每轮复述全部历史；该清单会随任务持久化，进程重启后恢复。`

// coldArchiveBlock 是冷区（cold digest）的**代码持有**纪律：老库的正文与领域模板
// 都不知道折叠机制，模型会把 recent_facts 的“最新一窗”当成全部历史，于是把已经
// 试过的方向当新方向重派。折叠只省上下文，不是删除——这句必须每轮都在。
const coldArchiveBlock = `

【冷区】
- cold_digests 是折叠摘要，不是删除或已排除。成员全文用 node_detail / expand_digest；更旧记录用 list_facts / blackboard_query 翻页。`

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
	// A timeout settlement may start its final round while the regular planner
	// is still unwinding after cancellation. Serialize both and let CloseHotSession
	// wait for the active round instead of racing p.hot / transcript state.
	p.planMu.Lock()
	defer p.planMu.Unlock()
	// 跨进程恢复：TodoWrite 清单与上一轮结论只在 Planner 对象里时，重启即丢；
	// 每轮开始恢复一次、结束落库一次。恢复只影响规划便签，不写入事实图。
	plannerTodos := p.restorePlannerState(ctx, ts, taskID, ts.ID())
	defer p.savePlannerState(ts, taskID, ts.ID(), plannerTodos)
	_, proxyCA := p.proxySnapshot()
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	createdTools := tsx
	defer func() {
		tsx.CloseHTTP() // tsx may have been replaced by the retained hot ToolSet
		if createdTools != tsx {
			createdTools.CloseHTTP()
		}
	}()
	// wireToolSet 把「本轮」依赖与轮级状态装到**本轮实际被调用的**那个 ToolSet 上。
	//
	// 热会话复用时，模型调用的工具闭包绑定的是热会话首轮的 ToolSet（agentcore.Session
	// 的 registry 在 NewSession 时固定），而不是这里新建的 tsx。所以两处都要走同一套
	// （任务 18/19 实测），GoalMet 与广度快照也会跨轮残留。
	// 复盘轮节律：有在跑的活才复盘（没活时硬底线优先）；每 5 个普通轮或新增 ≥4 条
	// 事实触发一次，两次之间至少 3 个普通轮。计数器放在 Planner 上，按任务隔离。
	wireToolSet := func(t *ToolSet) {
		t.ts = ts // 任务存储指针跨轮不变；重装一遍以防替换
		t.SetEgressProxyFunc(p.proxySnapshot)
		t.SetProxyFailureHook(p.proxyFailureFn)
		originNode := int64(0)
		if origin, _ := ts.OriginFactID(); origin > 0 {
			originNode = origin
		}
		t.BeginRound(taskClockFrom(ctx), originNode)
		t.SetExecService(p.execService)
		t.SetRotateEgressHandler(p.rotateEgress)
		t.SetFindingRecorder(p.findingRecorder)
		verifier := p.verifyWithLLM
		if p.llmVerifier != nil {
			verifier = p.llmVerifier
		}
		t.SetLLMVerifier(verifier)
		if as != nil {
			t.SetAssetStore(as, as.Companies())
		}
		t.SetTaskID(taskID)
		t.SetTaskRoot(filepath.Join(p.workDir, "tasks", strconv.FormatInt(taskID, 10)))
		t.SetTaskClock(taskClockFrom(ctx))
		t.SetBoundedView(p.boundedView)
		t.SetNotifyWorkers(p.notifyWorkers)
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
	releaseRoundResources := true
	defer func() {
		if releaseRoundResources {
			cleanup()
		}
	}()
	if p.webSearch.Enabled {
		tools = append(tools, NewWebSearchTool(p.webSearch))
	}
	// 工具 schema 瘦身：把低频内置工具的 schema 从每轮工具列表里摘出去，只留名字在
	// 系统提示的 deferred 块里。planner 37 个工具的 schema 合计约 3.3 万字符，每轮都
	// 随缓存前缀发送；这几项在绝大多数轮次里不会被调用（见 plannerDeferredTools）。
	mergeDeferred(&def, deferPlannerTools(tools))
	// 本任务的工作目录 <workDir>/tasks/<taskID>，先建好。
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	tsx.SetRunDir(taskDir)
	tsx.SetTaskRoot(taskDir)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	staticSys, dynamicSys := plannerSystemParts(goal, p.workDir, taskDir)
	staticSys += plannerScopeRequestBlock
	var constraintTail string
	if p.wantConstraints() {
		constraintTail = constraintBlock(ts) // 操作约束置于静态 AlwaysSkills/Capability/Deferred 段之后，避免打断跨任务静态前缀缓存
	}
	system, boundary := deferredSystemWithDynamic(staticSys, def, dynamicSys, constraintTail)
	// 单轮墙钟预算：优先用 agent 配置的 run_seconds（默认 1200s），有任务 deadline 时
	// 再夹逼到剩余时间。到点由 SDK 进收尾（不是硬杀），已想清楚的结论能落地。
	tc := taskClockFrom(ctx)
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, p.roundDur)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	plannerSID := PlannerSessionID(ts.ID())
	opts := agentcore.Options{
		Provider:                p.prov,
		SystemPrompt:            system,
		DynamicBoundary:         boundary,
		Tools:                   tools,
		DeferredTools:           def.Deferred,
		UnlockSet:               def.Unlock,
		PermissionMode:          permission.ModeBypass,
		EnableWebFetch:                true, // 走记录代理留痕；载入代理 CA 验证 MITM 重签的 HTTPS 证书
		WebFetchProxyFunc:             p.proxySnapshot,
		WebFetchCACert:                proxyCA,
		WebFetchDisableDirectFallback: true,
		OmitCurrentDateReminder: true,
		PromptCacheKey:          plannerSID,
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
		Todos: plannerTodos,
		// 命中【本轮】步数预算→ SDK 跑收尾:把本轮已想清楚的结论落地(该派的 add_intent、
		// 能证的 prove_goal、串行链记 TodoWrite),而非停止规划——planner 之后仍会被反复唤醒。
		// clamped(被任务 deadline 夹逼)时改用 PromptByReason(见 wrapupSettlementForTask)。
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = 不发上限,由服务端默认值决定
	}
	p.cacheOpts.apply(&opts)
	if p.guard != nil { // 与 worker/mainagent 同一把任务 Guard（拦截规则 + RoE 范围门 + 审批）
		opts.Hooks = p.guard.Hooks()
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = plannerSID
	}
	// 实验功能:开启后由 noa 接管上下文压缩(归档集中在 <workDir>/noa/<SessionID> 下,持久)。
	// 与 transcript 同一个 SessionID（见 PlannerSessionID）：历史与 noa 归档同源。
	noaSession := plannerSID
	win := p.compactionWindow()
	noaSt := enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, win, noaWarn(noaSession))
	// P14 领域上下文（详见 agent/compactor.go）：规划者被压缩吃掉态势后，常驻索引仍带着
	// 目标/约束原文与活跃方向；摘要也走渗透领域模板。未压缩前 HasOverview 仅保留目标/约束/时钟，
	// 避免把 Turn 1 已预取的大段态势在尾部重发。
	installClock := taskClockFrom(ctx)
	var compactionOverride []CompactionLLM
	if p.compactionLLM != nil {
		if c, ok := p.compactionLLM(); ok {
			compactionOverride = append(compactionOverride, c)
		}
	}
	InstallContextManagerDynamic(&opts, win, p.maxTokens(), taskDir, func(compacted bool) string {
		// P23：同公司其它任务的沉淀（只读先验）也进常驻索引——被压缩吃掉态势后仍看得见。
		return BuildContextIndex(ContextIndexVars{Store: ts, Clock: installClock, CompanyLines: tsx.companyKnowledgeLines(), PlaybookLines: tsx.playbookLines(), CVELines: tsx.cveLines(), HasOverview: true, Uncompacted: !compacted, StabilizeClock: true})
	}, compactionOverride...)
	attachNoaState(opts.Compactor, noaSt)
	// MaxDuration 现在会在墙钟到点打断在跑工具并就地进收尾(在活 ctx 上),单轮卡死不再
	// 绕过收尾,无需外部硬 ctx 兜底。ctx 只承载 pause / kill / shutdown。
	// 规划会话形态（三选一）：
	//   ① resume 模式（BLADE_PLANNER_RESUME_SESSION=1）——每轮从 transcript 全量恢复。
	//      回滚开关；上下文会持续膨胀（任务 14 实测 26 万 token/轮）。
	//   ② 热会话（默认）——在有界窗口内追加增量游标变更，保留 Turn 1 完整黑板前缀吃满 KV 缓存；
	//      达到上限后再折叠成「输入 + 结论」重新建基线，上下文体积有界。
	//   ③ 冷会话（热会话不可用时兜底）——每轮新建，只带上一轮结论摘要。
	p.invalidateHotSurface(plannerSurfaceFingerprint(strings.Join(opts.SystemPrompt, "\x00")+fmt.Sprintf("|%d|%s|%t|%d|%d|%s|%t", opts.MaxTurns, opts.MaxDuration, opts.NonStreaming, opts.MaxTokens, win, opts.WebFetchCACert, noaSt.enabled), opts.Tools))
	var s *agentcore.Session
	keepSession := false
	useDelta := false
	var prevNodeCursor int64
	var prevChangeCursor string
	notePrefix := ""
	if resumePlannerSession() {
		p.dropHotSession()
		s = agentcore.NewSession(opts)
		if p.tx != nil {
			_ = s.Resume(opts.SessionID)
			notePrefix = "【注意】下方态势是本轮刚从探索图取的**权威快照**；历史消息里的旧态势只作背景，冲突时一律以本轮为准。\n\n"
		}
	} else if hs, hts, hexp, hnode, hchange := p.takeHotSession(); hs != nil && hts != nil {
		s = hs
		// 复用会话的工具闭包绑定的是热会话首轮的 ToolSet：本轮在那个对象上重装依赖并
		// 重置轮级状态，后续的 quota / GoalMet / 写回统计才属于「本轮」。
		tsx = hts
		wireToolSet(tsx)
		keepSession = true
		if p.useBoundedView() && hexp == ts.ID() && len(s.Messages()) > 0 && llm.LastBoundaryIndex(s.Messages()) < 0 && (hnode > 0 || hchange != "") && p.shouldUseDelta() {
			useDelta = true
			prevNodeCursor = hnode
			prevChangeCursor = hchange
		}
	} else {
		s = agentcore.NewSession(opts)
		if note := strings.TrimSpace(p.lastRoundNote); note != "" {
			notePrefix = "【上一轮结论摘要（本轮是独立会话，只承接结论，不承接旧对话；态势以下方本轮快照为准）】\n" + note + "\n\n"
		}
	}
	var bb map[string]any
	situation := ""
	if useDelta {
		bb = tsx.blackboardDeltaData(prevNodeCursor, 30, prevChangeCursor)
		if resync, _ := bb["resync_required"].(bool); resync {
			situation = renderGraphOverview(bb, true)
		} else {
			situation = renderGraphDeltaOverview(bb)
		}
	} else if p.useBoundedView() {
		bb = tsx.blackboardViewData(0, 20)
		situation = renderGraphOverview(bb, true)
	} else {
		bb = tsx.graphOverviewData() // 有界视图关闭：预取完整态势
		situation = renderGraphOverview(bb, false)
	}
	nextNodeCursor, nextChangeCursor := extractBlackboardCursors(bb, prevNodeCursor, prevChangeCursor)
	situational := renderTriggers(ts, triggers) + situation
	if tc.Final {
		situational += "\n\n【任务终局收尾（本轮特殊指令，覆盖上面的常规规划流程）】：" + resolveTaskTimeoutWrapup("planner")
	}
	lead := "刚有具体变动（见下面的【本次触发本轮的实际变动】），据此规划下一步："
	if len(triggers) == 0 {
		lead = "本轮是**定时巡检（心跳到点）/无具体变动信号**的唤醒——图不一定有新变动。顺带复查在跑意图：长时间无进展或跑偏的用 steer_work 纠偏、方向整个错的用 kill_work 止损；再判定目标、决定是否补方向："
	}
	staticRules := "据下面的态势，判定目标。目标已【真正达成】（已拿到目标成果/已确认目标漏洞）时用 prove_goal 逐个标记。"
	volatile := notePrefix + situational
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
		// 成功：保留增量热会话前缀（未超窗时不 Reset），超出消息/token 上限后折叠重置。
		p.rememberRound(ts, prevMaxIntentID, lastText)
		var adoptedCleanup func()
		if !keepSession {
			adoptedCleanup = cleanup
		}
		p.parkHotSession(s, tsx, input, runText, ts.ID(), nextNodeCursor, nextChangeCursor, adoptedCleanup)
		if adoptedCleanup != nil {
			releaseRoundResources = false
		}
		keepSession = true
	case err == nil:
		p.rememberRound(ts, prevMaxIntentID, lastText)
	case keepSession:
		// 热会话上失败：半截历史不适合当前缀，丢弃，下一轮重新建。
		p.dropHotSession()
		s = nil // dropHotSession already closed the retained session
		keepSession = false
	}
	if !keepSession && s != nil {
		_ = s.Close()
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
	plannerHotMaxRounds   = 16
	plannerHotMaxMessages = 48
	plannerHotIdleTTL     = 10 * time.Minute
	// plannerFullReconcileEvery 每 N 个热会话轮强制一次完整有界视图，而不是只看
	// delta；否则落在 delta 字段索引之外的老项（attention 窗口）可能长期不再出现。
	plannerFullReconcileEvery = 5
)

// shouldUseDelta lets the hot session use the cheap delta most rounds, but forces
// a periodic full bounded snapshot for reconciliation. Callers hold hotMu.
func (p *Planner) shouldUseDelta() bool {
	p.hotMu.Lock()
	defer p.hotMu.Unlock()
	if p.hotRounds <= 0 {
		return true
	}
	return p.hotRounds%plannerFullReconcileEvery != 0
}

func extractBlackboardCursors(bb map[string]any, fallbackNode int64, fallbackChange string) (int64, string) {
	if bb == nil {
		return fallbackNode, fallbackChange
	}
	nodeCursor := fallbackNode
	if meta, ok := bb["blackboard"].(map[string]any); ok {
		switch v := meta["next_cursor"].(type) {
		case int64:
			if v > nodeCursor {
				nodeCursor = v
			}
		case int:
			if int64(v) > nodeCursor {
				nodeCursor = int64(v)
			}
		case float64:
			if int64(v) > nodeCursor {
				nodeCursor = int64(v)
			}
		}
	}
	changeCursor := fallbackChange
	if c, ok := bb["change_cursor"].(string); ok && c != "" {
		changeCursor = c
	}
	return nodeCursor, changeCursor
}

// takeHotSession 取出可复用的热会话；没有 / 超轮数 / 超空闲 / 任务已暂停或删除时返回 nil。
func (p *Planner) takeHotSession() (*agentcore.Session, *ToolSet, int64, int64, string) {
	p.hotMu.Lock()
	defer p.hotMu.Unlock()
	// 没有配套 ToolSet 的热会话不可复用：它的工具闭包绑定在哪个对象上无从得知，
	// 无法重置本轮额度（见 Plan 里 wireToolSet 的说明）。直接丢弃、走冷路径。
	if p.hot == nil || p.hotTools == nil {
		p.closeHotLocked()
		return nil, nil, 0, 0, ""
	}
	if p.hotRounds >= plannerHotMaxRounds || time.Since(p.hotLastUsed) > plannerHotIdleTTL {
		p.closeHotLocked()
		return nil, nil, 0, 0, ""
	}
	return p.hot, p.hotTools, p.hotExpID, p.hotLastNodeCursor, p.hotLastChangeCursor
}

// parkHotSession 保留或折叠热会话作为下一轮的前缀：在安全窗口内不调用 Reset()，
// 保留 Turn 1 完整黑板前缀及后续增量对话吃满 KV 缓存；超出消息/token 阈值时折叠并重置游标，
// 让下轮重建干净基准快照，确保上下文体积始终有界。
func (p *Planner) parkHotSession(s *agentcore.Session, tools *ToolSet, roundInput, lastText string, expID, nextNodeCursor int64, nextChangeCursor string, cleanups ...func()) {
	if s == nil || tools == nil {
		return
	}
	msgs := s.Messages()
	compacted := llm.LastBoundaryIndex(msgs) >= 0
	maxTok := p.compactionWindow() / 2
	if maxTok <= 0 {
		maxTok = 60000
	}
	withinWindow := !compacted && len(msgs) <= plannerHotMaxMessages && compaction.EstimateTokens(msgs) <= maxTok
	if !withinWindow {
		s.Reset()
		s.AppendMessages(hotPrefixMessages(roundInput, lastText)...)
		nextNodeCursor = 0
		nextChangeCursor = ""
	}
	p.hotMu.Lock()
	defer p.hotMu.Unlock()
	p.hot = s
	// 连同工具绑定一起留存：下一轮必须在这个 ToolSet 上重置轮级状态，
	// 新建的 ToolSet 并不会被复用会话里的工具闭包引用。
	p.hotTools = tools
	if len(cleanups) > 0 && cleanups[0] != nil {
		p.hotCleanup = cleanups[0]
	}
	p.hotRounds++
	p.hotLastUsed = time.Now()
	p.hotExpID = expID
	p.hotLastNodeCursor = nextNodeCursor
	p.hotLastChangeCursor = nextChangeCursor
}

// hotPrefixMessages 是热会话在轮间保留的最小前缀：上一轮问了什么、最后结论是什么。
// 只留结论而不是完整工具历史，是为了让下一轮的前缀短、稳定、可缓存。
func hotPrefixMessages(roundInput, lastText string) []llm.Message {
	out := make([]llm.Message, 0, 2)
	if in := compactHotRoundInput(roundInput); in != "" {
		out = append(out, llm.UserText(in))
	}
	if t := strings.TrimSpace(lastText); t != "" {
		out = append(out, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{llm.TextBlock(clipLine(t, 3000))},
		})
	}
	return out
}

// compactHotRoundInput keeps the stable prompt/trigger preamble, not the giant
// prefetched graph JSON. The old 3000-char prefix was almost entirely static rules
// and overview JSON, so the next round effectively lost the previous trigger and
// strategic reasoning. Assistant text is kept separately (up to 3000 chars).
func compactHotRoundInput(roundInput string) string {
	in := strings.TrimSpace(roundInput)
	if in == "" {
		return ""
	}
	head := in
	cut := strings.Index(in, "【本轮态势")
	if d := strings.Index(in, "【本轮增量态势"); d > 0 && (cut < 0 || d < cut) {
		cut = d
	}
	if cut > 0 {
		head = in[:cut]
	}
	const triggerMark = "【本次触发本轮的实际变动"
	if i := strings.Index(in, triggerMark); i >= 0 {
		end := len(in)
		rest := in[i+len(triggerMark):]
		if j := strings.Index(rest, "\n\n【"); j >= 0 {
			end = i + len(triggerMark) + j
		}
		head += "\n" + in[i:end]
	}
	return clipLine(strings.TrimSpace(head), 3000)
}

// dropHotSession 丢弃热会话（本轮失败/被取消时用：半截历史不适合当前缀）。
func (p *Planner) dropHotSession() {
	p.hotMu.Lock()
	defer p.hotMu.Unlock()
	p.closeHotLocked()
}

// CloseHotSession 供任务停止/删除时释放（引擎侧调用，幂等）。
func (p *Planner) CloseHotSession() {
	p.planMu.Lock()
	defer p.planMu.Unlock()
	p.dropHotSession()
}

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
	if t := clipLine(strings.TrimSpace(lastText), 1500); t != "" {
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

// Include model-visible tool descriptions and schemas: a registry change must not
// keep using closures/schemas from a prior hot session. Ordinary graph changes do
// not invalidate it; they arrive through the turn input.
func plannerSurfaceFingerprint(system string, tools []actool.CoreTool) string {
	h := sha256.New()
	h.Write([]byte(system))
	parts := make([]string, 0, len(tools))
	for _, tool := range tools {
		b, _ := json.Marshal(struct {
			Name, Description string
			Schema            any
		}{tool.Name(), tool.Description(), tool.InputSchema()})
		parts = append(parts, string(b))
	}
	sort.Strings(parts)
	for _, part := range parts {
		h.Write([]byte(part))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (p *Planner) invalidateHotSurface(surface string) {
	p.hotMu.Lock()
	defer p.hotMu.Unlock()
	if p.hotSurface != "" && p.hotSurface != surface {
		p.closeHotLocked()
	}
	p.hotSurface = surface
}

// Preserve event order after selecting priority; ordinary batches must not bury
// human goals, new capabilities or contradictions during dense tool streams.
func TriggerPriority(kind string) int {
	switch kind {
	case "goal", "goal_edited", "goal_deleted", "goal_abandoned", "hint", "finding", "contradiction", "scope_request":
		return 3
	case "capability_fact", "lead_fact", "proposal", "foothold", "credential", "supersede":
		return 2
	case "facts_batch", "assets_batch", "fact", "asset":
		return 0
	default:
		return 1
	}
}

func selectTriggerWindow(events []TriggerEvent, limit int) []TriggerEvent {
	if len(events) <= limit {
		return events
	}
	selected := make([]bool, len(events))
	remaining := limit
	for priority := 3; priority >= 0 && remaining > 0; priority-- {
		for i := len(events) - 1; i >= 0 && remaining > 0; i-- {
			if TriggerPriority(events[i].Kind) == priority {
				selected[i] = true
				remaining--
			}
		}
	}
	out := make([]TriggerEvent, 0, limit)
	for i, event := range events {
		if selected[i] {
			out = append(out, event)
		}
	}
	return out
}

// Caller holds hotMu; Plan/CloseHotSession additionally serialize through planMu.
func (p *Planner) closeHotLocked() {
	if p.hot != nil {
		_ = p.hot.Close()
	}
	if p.hotCleanup != nil {
		p.hotCleanup()
	}
	if p.hotTools != nil {
		p.hotTools.CloseHTTP()
	}
	p.hotCleanup = nil
	p.hot, p.hotTools, p.hotRounds = nil, nil, 0
	p.hotExpID, p.hotLastNodeCursor, p.hotLastChangeCursor = 0, 0, ""
}
