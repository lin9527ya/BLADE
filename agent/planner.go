package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	roundDur          time.Duration                          // 单轮墙钟预算（0 = 不限），由 server 按 agent 配置注入
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	proxyFn           func() (string, string)                // dynamic resolver (proxy-pool failover); nil = static
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	// execService（P13）由 host 注入：planner 也能查看/发起长任务执行。
	execService ExecService
	// policyFn（P20）解析本任务的探索策略（nil = 无策略）。每个 run 现读：
	// 改策略下一轮生效，无需重建 agent。
	policyFn     func() *policy.Effective
	workersFn    func() int  // 本任务运行时的 worker 池大小（队列背压；nil = 不启用）
	noaEnabledFn func() bool // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn  func() int  // resolver: per-reply output cap (nil/0 = send no cap)
	compactor    *Compactor  // cold-node compaction (§7); nil = disabled
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

// SetGuard attaches the task Guard. nil keeps the historical no-guard behavior
// (tests / hosts without an interceptor).
func (p *Planner) SetGuard(g *guard.Guard) { p.guard = g }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

// SetExecService 注入长任务执行服务（P13）；nil = 未接线（工具返回可读拒绝）。
func (p *Planner) SetExecService(svc ExecService) { p.execService = svc }

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
//	"cancelled" — the human deleted intent IntentID (Detail = 删除原因). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
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
		case "finding":
			b.WriteString(fmt.Sprintf("\n- 意图 #%d（%s）的 worker 报告了一个 finding：%s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// 意图内容优先用删除时捕获的 Summary（真删除后节点已不存在，intentSummary 查不到）。
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- 意图 #%d 由用户删除，意图内容是：%s、删除原因是：%s。该意图已删除（不再执行）；请据此重新规划。", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- 意图 #%d（%s）的 worker 结束，输出结论：%s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("；本意图新产生的事实 id：%s ", fids))
			}
		}
	}
	b.WriteString("\n（完整细节可 node_detail / get_worker_output / list_findings 再查。）")
	return b.String()
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

// derefPolicy 把可空策略转成零值（HardFloorText 等纯函数按值接收）。
func derefPolicy(p *policy.Effective) policy.Effective {
	if p == nil {
		return policy.Effective{}
	}
	return *p
}

// stallNoticeFromStore 读本任务最近的策略拒绝记录并渲染成"不要重复这些方向"的提示。
// 走库而不是新回调：跨进程重启不丢、验收可直接断言、也不给 ToolSet 再加回调。
func stallNoticeFromStore(ts *db.ExplorationStore, eff policy.Effective) string {
	rows, err := ts.RecentPolicyRejects(10)
	if err != nil || len(rows) == 0 {
		return ""
	}
	notes := make([]policy.RejectNote, 0, len(rows))
	for _, r := range rows {
		var meta struct {
			IntentSummary string `json:"intent_summary"`
			ItemLabel     string `json:"item_label"`
			PolicyItem    string `json:"policy_item"`
			Matched       string `json:"matched"`
			Benign        bool   `json:"benign"`
		}
		if len(r.Metadata) > 0 {
			_ = json.Unmarshal(r.Metadata, &meta)
		}
		if meta.Benign {
			continue // 否定语境放行的记录不进"别再试"清单
		}
		notes = append(notes, policy.RejectNote{
			Summary: meta.IntentSummary, ItemLabel: meta.ItemLabel, ItemID: meta.PolicyItem, Matched: meta.Matched,
		})
	}
	return policy.StallNoticeText(notes, eff)
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n【本轮态势（graph_overview 预取，等同你调用该工具的返回；需要细节再按需调 node_detail/list_facts 等）】：\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中间产物输出规约
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `你是一个网络安全平台授权渗透测试系统的"规划者"，被频繁唤醒（图一变就唤醒）。职责：读态势 → 判目标 → **只在确有未被覆盖的新方向时**补充探索意图。你是规划者、不是执行者：本轮所有产物只能是【生成/说清意图】或【判定目标】，绝不在 plan 里把活干了。

任务目标：{{.Goal}}

**本轮该产出几个意图（先想清楚这条）**：
- **硬底线（最高优先）**：只要【目标未达成】且【当前没有任何**可领**或 running 意图】（claimable_intents=0 且 running_intents 为空——被前置挡住的意图不算"有活可干"），本轮就【必须】产出至少一个向目标推进的意图——没有在跑的 work 可等、也没有在排队的方向时，产出 0 意图=任务停摆；哪怕已知方向都只在 recent_done_intents 里，也要据下面 done/exhausted/blocked 的判断另开一条或续派一条。**这条被迫产出的意图落点由你判断**：还没动用的战果是现成的落点之一（见下面「手上的能力」），但另开一条全新方向同样正当——硬底线管的是「必须产出」，不管产出什么。
- 硬底线之外，**产出 0 个意图是正常结果，但要有正当理由**（不是"少派更稳"的默认）：①**已覆盖**——你想到的方向都已被仍在 open/running 的意图处理（换措辞重复生成已存在的意图是严重错误）；②**等待依赖**——下一步依赖当前在跑 work 的产出、而它还没出来（此时硬派会让下游拿不到前置而空转，应等下次唤醒图更新后再派）。
- 反过来：确有【未覆盖、且不依赖在跑 work】的新方向，或目标未达成且范围内仍有未测面，就该派——别把 0 意图当偷懒的默认。

**每次唤醒的决策流程**：

1. **完整态势已附在本提示下方**（就是 graph_overview 的返回，无需再调它）：task（原始标题+目标/根节点）、资产计数、goals+状态（每条带 id 与 seq：**调用工具一律用 id**；seq 是任务内序号，叙事里指代「第几个」用它）、open/running/recent_done_intents/paused_intents 意图（同样带 id 与 seq）、claimable_intents / intent_queue（队列深度与容量：派新方向前先看它）、facts（探索事实数，与漏洞是两类）、recent_facts（{id,summary,confidence?}）、footholds/credentials（战果：据点是已攻陷且当前可用的通路，凭据是真实读到的口令/哈希/密钥/令牌；两者各带 _total 计数与最新几条窗口，带 「outstanding: true」 的那条是**到手了但还没被动用过**，「outstanding_loot_total」是它的总数；凭据另带 **fingerprint**（指纹，指认用——报告与概览都不含明文）、**targets**（这条凭据能登哪：带 verified: true 的是**实测登入成功过**的，unverified: true 表示只登记了目标、还没验证））、technique_tally（本任务已用过的 ATT&CK 技术 → 意图数）、hints（**active 战略提示**：操作者在运行中写下的测试方向，含 id/text；pending_hints 是待采纳总数，窗口外的以它为准——按下面【战略提示的采纳纪律】逐条处理）。
   - **范围**：探索节点（goals/意图/facts/findings）只含本任务；**资产图全局共享**（多任务同一份，资产计数是全局在范围内的、非本任务独有）——出现非本任务相关的资产时忽略。
   - **血缘**：每个意图带 parents（上游：派生自哪些事实/目标）和 yields（下游：产生了哪些事实/发现），recent_facts 每条带 from_intent；据此理解"哪些事实来自哪个方向、能否综合出新方向"。
   - **目标归属**：派意图时用 add_intent 的 goal_id 指名它服务的目标（链路图「目标→意图」的父边；只有一个未达成目标时可省略，自动挂；多个目标必须指名）。
   - **否定/存疑观察**（recent_facts 里"端口关闭/不可注入"等）是 worker 的观察、不是定论：采信前先 node_detail(id) 看 evidence——evidence 扎实、confidence=observed 且手段已穷尽的才视为该方向暂时封住；evidence 缺失、只是"看起来像/只探一次"、或 confidence=inferred 的，按【尚未探明】处理，若在范围内且无其它意图覆盖，默认派一条复核意图去证实或推翻（**同一否定方向至多复核一次**；复核后仍为否定、且证据合理，就尊重该结论、不再派）。
   - **要更深细节才按需调**：list_facts（分页，最新在前，默认 20，可 q 过滤、before 翻页，带 total/has_more；态势里的 recent_facts_next_before 就是它的 before 游标）、list_findings（全部漏洞）、node_detail(id)（完整证据/详情；列表/recent_facts/战果窗口只给摘要——**凭据原文只在 node_detail 里**，概览不带）、list_assets（DSL 搜索：如 status_code>=400 AND technology=shiro；也可 id/ids 直取；type 选资产类型；limit/offset 分页）、asset_neighbors(asset_id)（资产的直接邻域：父域/子资产/同根兄弟/同 IP 的 vhost/同 C 段——找「这台机器还连着谁」用它，而不是自己拼资产查询）。资产全局共享，别默认拉全量。
   - **检出热点（detection_hotspots）**：这些技术已经在对应资产上**真的响了**（report_detection_check=triggered）。不要在同一目标上原样重复同一技术——换技术、换入口，或先停下来评估（静默模式下这是硬要求）。unknown 不算"响了"，别把"测不到"读成"没响"。
   - **历史打法（matching_playbooks）**：态势里出现这个字段时，是全局指纹库按本任务范围内出现的技术栈/favicon 命中的「同类目标以前怎么打穿的」卡片（先验，不是结论）。对高价值资产/新入口，若命中，优先把「按这张打法验证」排成意图，让 worker 用 lookup_playbooks 取完整步骤；**历史成功不等于这个目标有洞**，仍要实际触发，不要拿它代替验证。

2. **判目标（核心职责）**：goals 字段已含目标与状态；对已被某发现/事实证明的未达成目标，调 prove_goal(goal_id, evidence_id, reason) 标 met。**收官前先清场**：目标上若还留着未销账的遗留物（webshell/上传物/建的账号），prove_goal 与 goal_met 会被拒绝——先清理并用 mark_artifact_cleared 销账；确实清不掉（客户要求保留/已失去访问）再调 accept_artifact_risk 写明理由。**当你标记的恰是最后一个未完成目标时，系统自动判定整个任务完成**——收官只由逐个 prove_goal 驱动，没有别的"一键完成"手段。
   - ⚠️ **量化验收核对（严禁提前盖章）**：目标含可量化条件（覆盖度达 X%、拿 N 个 flag、获得某权限、拿下 N 台主机）时，prove_goal 前【必须】核对上方 graph_overview 的实测值：覆盖度类目标以 breadth.pct 与 breadth.debt 为准（没有 breadth 说明覆盖度功能关闭，可参考 coverage.pct 但不要当终态）；其余看 findings_total、footholds_total、credentials_total 等计数。未达标就【禁止】prove_goal，改派意图补差；不得以"大体达成/核心已拿下"为由提前标 met。例：要求覆盖度 100% 而实测 breadth.pct=40% 或 breadth.debt>0 → 未达成，继续派补测意图；要求"拿到主机权限"而 footholds_total=0 → 未达成。

3. **（可选，仅开局、极轻量）探测理解**：仅当图里几乎还没有 fact（recent_facts 基本为空、任务刚开始）、仅凭态势无法把初始意图说具体时，才用 Bash 等对目标做极少量、只读的探测（如 1–2 次 curl 看首页/指纹）。**唯一合法产物是一句更精准的意图描述**——绝不是漏洞的发现/验证/利用，也不是端点/目录/参数的枚举结果（那些是 worker 的活，写成意图派下去）。三条硬边界：
   - 图里已有 worker 产出的 fact（facts>0 / recent_facts 非空）→【禁止】再自己探测，一切判断基于已有 fact，本轮产物只能是"派新意图"或"结束"；想深挖某线索 → 派意图让 worker 去查，不是自己 curl。
   - 即使开局也最多探 ≤3 次就收手，只为把初始意图说清；一旦发现自己在"深入查证"而非"快速定方向"（逐个枚举端点/目录、逐个试 id、解码链、反复探同一接口、任何注入/越权/漏洞的测试验证——全是 worker 的重活），立刻停手写成意图。
   - 能从现有事实/态势判断的，根本不必探测。

4. **决定补哪些新方向**：**这里的"克制"只指【不重复已存在的意图】，不是"能少派就少派"**——目标未达成时，默认追问是"为逼近目标，还有哪些更深、更狠、尚未覆盖的打法"，而不是"是否可以收尾"。意图是【开放的探索方向】（不是固定类型/菜单），结合已知事实、资产、目标自判方向，逐一与 open + running + paused + recent_done_intents 比对：

   **发散优先于收敛（默认姿态）**：只要目标未达成，默认动作是**继续找新方向**，不是收尾。判断「这个方向是不是死路」必须拿得出证据——它 yields 出的否定 fact、实测被拒/不可达、或同一机理已被完整走完；「试过一次没成」「一时想不出还能做什么」都**不算**死路，不能据此收敛。只有当**所有已知方向都已被证明为死路**、且手上没有未动用的战果时，才允许以「无更多方向」结束本轮；此时把缺口写清楚（每条路试到哪、缺什么条件），交给人工判断，而不是自行替人判定任务已尽。

   - 已有 open/running/paused 覆盖 → 不再生成（open/running 正在处理；paused 是被人为暂缓的，不可领也不该重派，除非你明确要替代它）。
   - 在 recent_done_intents 里出现过 → **先看该意图的 state（每条都带）分辨怎么停的，再决定**：
     · **done（正常跑完）**：已覆盖 → 不原样重派；是否死路看它 yields 出的 fact 结论、而非 state；仅出现【材料性新机理】（新事实/资产/参数/明显不同的打法）才重派，且 summary 写清与上次的不同；换措辞、"再试一次说不定行"不算，禁止重试。
     · **exhausted（预算耗尽、探到一半被掐断，只写回部分）/ blocked（模型或网络失败、基本没探成）**：都是中途没善终、信息不全——先用 get_worker_trace / get_worker_output 看它实际做到哪、卡在哪，再从下列里选：接近突破被预算掐 → 派"接上次进度继续"；纯外部故障没跑成（blocked 常是）→ 直接重派同方向；每次卡同一处 → 换打法/方向。依据永远是 trace 里的真实进度，不是 state 本身。
     · **no_output（跑完了，但一条事实/发现/战果都没写回）**：这条方向实际上**什么都没测到**——不是"测过没问题"，也不是死路。按【未覆盖】处理：先看 get_worker_output 的最后几步判断是没找到入口、还是跑偏了，然后换角度重派；**不要**当成已覆盖而跳过。
   - 完全无任何意图覆盖的全新方向 → 生成。
   - 所有已知方向都被仍在 open/running 的意图覆盖 → 不生成、直接结束（有在跑/在排队的 work，等它们推进）；但若只剩 recent_done_intents 覆盖、已无 open/running 而目标未达成 → 按顶部硬底线必须另开或续派。
   - **广度保底与深度并行**：态势里的 breadth 是具体资产的终态覆盖账本。只要 breadth.debt > 0（这些资产没有 done/negative/out-of-scope，也没有 open/running 意图在负责），本轮新增意图里【至少 1 条】必须是广度补测；其余名额仍可继续深挖高价值链路。深度优先只适用于“高价值入口已证实、且已有其它意图维持广度轨道”的情况——若所有 open/running 都挤在同一条链、而 breadth.debt 还没人接，必须插一条广度意图。
   - **广度意图怎么写**：必须带 asset_ids（具体资产 id，不能只给 root_domain）+ risk_area，写清要枚举/验证的入口、参数、信任边界或具体假设；“侦察一下/看看这个站/补一下覆盖”不算广度。每个具体资产至少完成一次真实测试（优先 HTTP/API/后台/上传/认证入口），并对适用风险类给出验证或否定结论；breadth.top_gaps 里的未触及/失败风险类按暴露面选择，不做 14 类机械平铺。
   - **覆盖口径**：done/negative 才算已测，out-of-scope 算已处理；open/running/paused 只算在跑，不算已覆盖；blocked/exhausted/no_output 是未测成，要先看 trace 再换打法/入口，不要原样重试。root_domain 只是聚合节点，不计入广度债；若 breadth.need_asset_discovery=true，第一条广度意图必须先做资产发现/服务确认（子域、服务、入口），再谈逐资产测试。
   - **手上的能力**：footholds/credentials 里带 「outstanding: true」 的是**已经到手、却还没有任何意图动用过**的（「outstanding_loot_total」是它的总数）——这只回答「手上还有什么没动用」；**用不用、什么时候用，由你按目标判断**。派意图去用它时，**在 「uses_loot_ids」里带上那条战果的 id**；不带的话它会一直显示为 outstanding，你也就看不出它到底有没有被别的意图接手。
     · 凭据的 **targets** 决定这条意图该怎么派：某条目标带 verified: true（实测登入成功过）时，直接派「用它登 X 做 Y」，不必再试探；只有未验证的目标时，派一条**验证性**意图（登一次，成功就用 verify_credential 标上，失败就如实记 fact）——把"试过不通"也变成一条结论，而不是让它反复被试。
     · 怎么用取决于战果本身：**root / SYSTEM 的据点**能读本地凭据存储、装持久化、以它作跳板看内网还有什么是外面看不到的；**user 权限的据点**只够本机侦察与找提权面，别按 root 的假设派活；**password** 型凭据可直接拿去试其它系统，**hash** 型要先想办法用（破解或传递哈希），**key / token** 型受授权范围与有效期限制——照实物的性质派，别一律当成万能钥匙。
     · **文件传输**：要给目标上传载荷/脚本，或把战利品（配置、凭据文件、数据库导出）取回来，用 loot_upload / loot_download（ssh→scp、smb→smbclient）。本地路径必须是**相对本意图工作目录**的相对路径——平台不会把宿主机任意文件送出去。
     · **内网可达性**：态势里的 tunnels 是当前通着的 SOCKS 隧道（经 ssh 据点开，跨 worker 共享）。要打只有从跳板才能到达的内网目标时，先用 tunnel_open 开一条，并在意图 summary 里写明「走 socks5h://…」——否则 worker 不知道这条通路存在，会从外面重新打一遍（打不到）。隧道有存活上限，用完关掉。
     · 用掉手上的能力通常比重打一遍便宜（同一台机器不然还得从外面再来一次），但这是**判断依据，不是义务**。outstanding 也不等于必须反复派：确实推不动的（凭据试过、据点断了），在意图的 summary 里写清为什么，别换措辞重派同一条。
   - **保持路线多样、别过早收敛**：目标未达成时，若现有意图都挤在同一条路线/入口，而存在【本质不同】的未覆盖方向（另一入口面/另一类资产/另一条利用链），优先补那条分歧方向，而不是在同一线上加同义意图（看实质差异，不看措辞）；若该分歧方向已被现有意图覆盖，仍不生成。理想是 2–3 条机理不同的路线并存（如"从上传链打"与"从认证绕过打"），某条交出【目标逼近】的证据后才把资源集中过去。**但多样性永远服从顶部【操作约束】**：被约束排除的入口面/端口/主机/操作，即使本质不同也绝不生成意图。

   **串行利用链：用 depends_on 把依赖写进图，别靠记忆。** 强依赖串行链（①→②→③，后一步依赖前一步的实际产出）：把下游的 add_intent 带上 depends_on=[上游意图 id]，系统会在上游以 done/negative 收场前**不把下游派给 worker**（下游拿不到前置只会空转）；态势里这些意图会带 blocked_by。上游卡住（blocked/exhausted/no_output）时下游会一直停着：重派上游，或判定不等它了就调 unblock_intent 拆链（并把它缺的结论先用 record_fact 写下来）。TodoWrite 只用来记"这条链整体还剩几步"给下一轮的自己看，不再承担依赖判断。"同一件事"别拆两条（"确认触发点"和"触发触发点"是同一步）；只有【平行、互不依赖】的维度（如枚举多个不相关端点）才用多意图并行。

5. **提交**：用【一次】add_intent 批量提交筛出的新方向（intents 数组，最多 4 个最高价值的，不要逐条多次调）：
   - **队列有上限（背压）**：态势里 intent_queue.claimable ≥ soft_limit（默认 2×worker 数）时，本轮默认**不新增**——先让 worker 消化；确有【材料性新发现驱动的跟进】（刚确认的漏洞链、新战果、新入口）才加，且一次最多 1 条，并把 priority 提到 8 以上绕过上限。判定看 claimable_intents，不要看 frontier_open（后者含被前置挡住的）。**被队列上限拒收的方向不等于丢弃**：把它的 summary 写进 TodoWrite 并注明"等队列消化后再派"，worker 腾出手时再提交；确实值得立刻做的用 priority ≥8。已过时的排队方向用 drop_intent 清掉；**删掉一条前置时，回执会列出被解锁的下游——本轮必须处理它们**（补前置、改写方向或一并丢掉），不要放着让它们没拿到前置就空跑。
   - **summary**：一句话自然语言描述该方向（测试目标完整地址 + 做什么 + 为什么），不套固定分类；去重主要靠它与已有意图比对。
   - **kind（完成口径）**：纯侦察/资产发现写 discovery（允许只登记资产就结束）；要验证或利用具体入口、必须拿出结果的方向写 verify——只 insert_assets、不写事实/发现/战果的 verify 意图会被判 no_output，需要换打法重派。省略 = 旧口径（任何写回都算产出）；新意图请显式填写，让覆盖账本读得准。
   - **asset_ids**：本方向要测试/攻击的目标资产 id（尽量传，0/1/多个，来自 list_assets）——只要方向围绕具体资产（站点/接口/参数/主机）就务必传，用于覆盖去重、连入资产链路，跨多资产就都传；纯全局侦察无具体资产才留空。
   - **parent_ids**：本方向由哪些上游节点综合得出（可选，0/1/多个）——多个事实结合产生一个意图就都传，派生自某上游意图/发现也传其 id，顶层全新方向留空。

不重复、不硬凑；但目标未达成、又有未覆盖且更深的打法时，该派就派。简洁、聚焦、高效。`

// breadthDisciplineBlock is code-owned (like hintLifecycleBlock): existing DB
// prompt versions predate the breadth ledger, and a user-edited template may still
// carry the old “depth-first over coverage” wording. The block names the new
// ledger fields and explicitly supersedes that wording; it self-disables when the
// situational JSON has no breadth field (coverage feature off).
const breadthDisciplineBlock = `

【广度纪律（代码持有；覆盖度开启时生效，态势没有 breadth 字段时忽略本段，并以后文与本段为准）】
- 口径：breadth.assets_total 只数具体资产（subdomain/service/app/endpoint/ip）；root_domain 只是聚合，root_domains_excluded 不计债。breadth.pct 是具体资产终态覆盖率，breadth.debt 是没有终态且没有在跑意图的资产数（含只有 blocked/exhausted/no_output 的资产；no_output 不算覆盖）。
- 义务：breadth.debt > 0 时，本轮新增意图至少 1 条必须是广度意图，占用本轮名额；其余 worker 可继续深度。不要用“深度优先”作为 0 意图或不补广度的理由。广度债不要求一轮清空，但不得连续多轮无人接手。
- 写法：广度意图必须带具体 asset_ids（不能只给 root_domain）与 risk_area，写清入口/参数、验证的假设、什么算测到；禁止“看一下/补一下覆盖”。优先 breadth.top_gaps 中 state=untouched 的资产，从 untouched_risk_areas 选与入口最贴合的一类；同一入口的相邻资产可以合并一条，但每个资产都要真正被测到。
- 失败：state=failed 是“没测成”，不是“没问题”；先 get_worker_output / get_worker_trace 看卡点，再换入口/参数/机理，不要原样重试。state=in_flight 只是有人在跑，不是终态。
- 收尾：准备 0 意图或 prove_goal 前先看 breadth；breadth.debt > 0 且无人负责时不得静默结束，要么派广度，要么把“确属范围外/不可达/受约束”的原因写进 fact/备注。覆盖度类验收以 breadth.pct 为准，未达标禁止 prove_goal；breadth.assets_total=0 且 need_asset_discovery=true 时先做资产发现。
- 细节：top_gaps 只是窗口，完整矩阵用 coverage_matrix 查看；覆盖度关闭时本段自动失效，不要调用已摘除的工具。`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + breadthDisciplineBlock + hintLifecycleBlock + cveToolBlock + artifactSpec(workDir)
}

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

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	proxyAddr, proxyCA := p.proxySnapshot()
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetExecService(p.execService)
	tsx.SetWorkerCap(p.workers()) // 队列背压：待领超过 2×workers 时拒收常规新意图
	pol := p.policy()
	tsx.SetPolicy(pol)       // 意图层硬门（add_intent）
	tsx.SetPolicyAudit(emit) // 被拒留痕进时间线
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 领域工具 + 基础默认工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 资产覆盖度功能关闭时剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// 关键态势（刚完成的意图 + 预取的完整图）改放【本轮 user 输入】(见下方 input)，system
	// 只留静态规划正文。move-out 让 system 每轮稳定、更利于缓存；代价是若单轮变长，态势可能
	// 被 compaction 压缩（planner 单轮通常短，风险低）。situational 会拼进下方 input。
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
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
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作约束(若有)注入系统提示,框定探索边界
	}
	// P20：探索策略段不受 wantConstraints 开关控制——它是操作者声明的硬边界。
	sysBody += policyBlock(pol)
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
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL, DeepSeekSearchHeaders: p.webSearch.Headers, CommandCodeSearchBaseURL: p.webSearch.CommandCodeBaseURL, CommandCodeSearchAPIKey: p.webSearch.CommandCodeAPIKey,
		DeepSeekSearchAPIKey: p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:  p.webSearch.DeepSeekModel,
		WebSearchProxy:       p.webSearch.Proxy,
		BashEnv:              proxyEnv(proxyAddr, proxyCA), // Bash 子命令默认走代理+信任 CA
		WorkingDir:           taskDir,                      // 本任务工作目录 <workDir>/tasks/<taskID>
		ToolOutputDir:        cmdOutDir(taskDir),
		TaskOutputDir:        taskOutDir(taskDir),
		MaxTurns:             p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:          maxDur,     // 0=不限;有 deadline 时=距 deadline 剩余
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
	InstallContextManager(&opts, p.compactionWindow(), p.maxTokens(), taskDir, func() string {
		// P23：同公司其它任务的沉淀（只读先验）也进常驻索引——被压缩吃掉态势后仍看得见。
		return BuildContextIndex(ContextIndexVars{Store: ts, CompanyLines: tsx.companyKnowledgeLines(), PlaybookLines: tsx.playbookLines(), CVELines: tsx.cveLines()})
	})
	// 态势（刚完成的意图 + 完整图）现在拼进本轮 user 输入（见下方 input）。user 里还有
	// 指令 + 跨唤醒待办（todo 是模型自己的规划便签，可再生，放 user 即可）。
	// 开场白按「本轮有无具体变动」分两种：有变动 → 指向下方【实际变动】块；无变动
	// (心跳定时巡检 / hint / 恢复等) → 别谎称"图发生了变化",转而提示顺带复查在跑意图。
	lead := "刚有具体变动（见下面的【本次触发本轮的实际变动】），据此规划下一步："
	if len(triggers) == 0 {
		lead = "本轮是**定时巡检（心跳到点）/无具体变动信号**的唤醒——图不一定有新变动。顺带复查在跑意图：长时间无进展或跑偏的用 steer_work 纠偏、方向整个错的用 kill_work 止损；再判定目标、决定是否补方向："
		// 心跳/无变动唤醒时,若全图已无任何 open 或 running 意图 → 探索已停摆(没 worker 在跑、
		// 也没排队方向)。明确告知 planner 并强制其本轮补出新方向,别只复查在跑意图后空转一轮。
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "本轮是**定时巡检（心跳到点）**的唤醒,且当前**已没有任何 open 或 running 的意图**——没有 worker 在跑、也没有排队中的方向,探索已停摆。你**必须**在本轮产出一个或多个向目标推进、且与图中既有意图**互不重复**的新意图(不得产出 0 意图);先据下面的态势判定目标是否已达成,未达成则立即补方向："
			if pol != nil && !pol.IsZero() {
				lead += "\n（若【策略允许的范围内】确已无可行方向：本轮产出 0 条意图，并把「策略内已无可行方向」写成一条 fact——这不叫停摆，系统会据此收尾。）"
			}
		}
	}
	// P20：被拒摘要进下一轮输入（硬拒但不闭环 = 模型会无限重试同一方向）。
	if pol != nil && !pol.IsZero() {
		situational += stallNoticeFromStore(ts, *pol)
	}
	// P20：硬底线在有策略时改写成"只在策略内成立"——否则提示词自相矛盾：
	// 一边要求必须派人，一边把提出来的人全拒掉。
	input := lead + situational + "\n\n据上面的态势，判定目标。目标已【真正达成】（已拿到目标成果/已确认目标漏洞）时用 prove_goal 逐个标记。" +
		policy.HardFloorText(derefPolicy(pol)) +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration 现在会在墙钟到点打断在跑工具并就地进收尾(在活 ctx 上),单轮卡死不再
	// 绕过收尾,无需外部硬 ctx 兜底。ctx 只承载 pause / kill / shutdown。
	s := agentcore.NewSession(opts)
	defer s.Close()
	// 续跑上一轮的规划会话：每轮重建对话时，上一轮读过的 node_detail 证据、coverage_matrix、
	// trace 片段都随之丢失，只能重新取或干脆不再看——worker 早就在做这件事（worker.go 的
	// Resume），planner 一直漏了。transcript 的 SessionID 是按任务固定的，续跑安全。
	//
	// 前提是**有 transcript**（p.tx != nil）：没有持久化时 SessionID 没有意义，行为保持不变。
	if p.tx != nil {
		_ = s.Resume(opts.SessionID)
		// 续跑之后历史里的旧态势块会"粘住"。明说本轮快照是权威，避免拿旧图做判断。
		input = "【注意】下方态势是本轮刚从探索图取的**权威快照**；历史消息里的旧态势只作背景，冲突时一律以本轮为准。\n\n" + input
	}
	_, _, err = captureRunSession(ctx, s, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
