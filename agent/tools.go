package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	acperm "github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/policy"
)

// compactIntents distills intents to {id, summary, state, asset_ids, parents,
// yields} so the planner sees both the direction and its LINEAGE — parents (the
// upstream nodes it derived from: facts/intents/findings) and yields (the facts/
// findings it produced) — without pulling full payloads. parentsOf/yieldsOf are
// built from the exploration edges in graph_overview.
// payloadAttempts 读意图 payload 里的领取次数（写入方是 claim 的原子 UPDATE）。
// 兼容 JSON 数字与历史脏值：解析不出就当 0，绝不让展示层报错。
func payloadAttempts(p map[string]any) int {
	switch v := p["attempts"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

// kindMap 把 id 列表映射成 {id: kind}，只保留已知 kind。
func kindMap(ids []int64, kinds map[int64]string) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		if k := kinds[id]; k != "" {
			out[strconv.FormatInt(id, 10)] = k
		}
	}
	return out
}

func compactIntentContract(c map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"mechanism", "route", "class", "estimated_tokens", "variant", "recheck_of"} {
		if v, ok := c[k]; ok {
			out[k] = v
		}
	}
	if v, ok := c["produces"]; ok {
		out["produces"] = v
	}
	if s, ok := c["acceptance"].(string); ok && strings.TrimSpace(s) != "" {
		out["acceptance"] = clipLine(s, 240)
	}
	if v, ok := c["change_evidence"]; ok {
		out["change_evidence"] = v
	}
	return out
}

func compactIntents(ns []*db.Node, parentsOf, yieldsOf map[int64][]int64, kindMaps ...map[int64]string) []map[string]any {
	var kinds map[int64]string
	if len(kindMaps) > 0 {
		kinds = kindMaps[0]
	}
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		m := map[string]any{"id": n.ID, "summary": p["summary"], "state": n.State}
		if ra, _ := p["risk_area"].(string); strings.TrimSpace(ra) != "" {
			m["risk_area"] = ra
		}
		if n.Inherited {
			m["source_task_id"] = n.SourceTaskID
			m["inherited"] = true
		}
		// asset_ids is the structured "which assets this direction covers" signal for
		// dedup; fall back to legacy payload keys (target_ids plural, then target_id
		// single) so intents stored before the rename still surface their anchors.
		if tg, ok := p["asset_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_id"]; ok && tg != nil && tg != "" {
			m["asset_ids"] = []any{tg}
		}
		if ps := parentsOf[n.ID]; len(ps) > 0 {
			m["parents"] = ps // 上游：本意图派生自哪些节点（多个事实可共同产生一个意图）
			if kinds != nil {
				if pk := kindMap(ps, kinds); len(pk) > 0 {
					m["parent_kinds"] = pk // id→kind：分清 goal / fact / finding
				}
			}
		}
		if ys := yieldsOf[n.ID]; len(ys) > 0 {
			m["yields"] = ys // 下游：本意图产生了哪些事实/发现
			if kinds != nil {
				if yk := kindMap(ys, kinds); len(yk) > 0 {
					m["yield_kinds"] = yk // id→kind：分清 fact / finding / loot
				}
			}
		}
		// 领取次数：升级阶梯的判据（第 2 次必须换角度，第 3 次不再原样重派）。
		if att := payloadAttempts(p); att > 0 {
			m["attempts"] = att
			if lo, _ := p["last_outcome"].(string); lo != "" {
				m["last_outcome"] = lo
			}
		}
		// 意图的假设与验收标准：复盘轮按它比较候选路线，worker 按 success_criteria
		// 判 done/no_output。只带非空字段，避免把旧意图的输出撑大。
		if h, _ := p["hypothesis"].(string); strings.TrimSpace(h) != "" {
			m["hypothesis"] = clipLine(h, 200)
		}
		if sc, _ := p["success_criteria"].(string); strings.TrimSpace(sc) != "" {
			m["success_criteria"] = clipLine(sc, 200)
		}
		if stop, _ := p["stop_condition"].(string); strings.TrimSpace(stop) != "" {
			m["stop_condition"] = clipLine(stop, 160)
		}
		// 契约只带决策需要的字段：acceptance 截断到 240 字，完整内容走 node_detail。
		if c, ok := p["contract"].(map[string]any); ok && len(c) > 0 {
			m["contract"] = compactIntentContract(c)
		}
		out = append(out, m)
	}
	return out
}

// ToolSet exposes the PG-backed dual graph (asset + exploration) to an LLM agent.
// One ToolSet is created per planner/worker run; per-run signals live here.
type ToolSet struct {
	findingRecorder FindingRecorder
	as              *db.AssetStore   // asset store (optional; nil = asset tools not available)
	cs              *db.CompanyStore // company store (optional)
	ts              *db.ExplorationStore
	worker          string
	taskID          int64 // PG tasks.id; 0 when unknown (tests / orchestrator cross-task reads)
	// clock 是本轮的任务截止信息（可空）。只用来给 add_intent 加收官期软信号——
	// 策略判定仍走 pol；时间到了不该拒收意图（收官期还可能要清理/补齐），只提醒。
	clock TaskClock
	// coverageDisabled mirrors tasks.coverage_enabled=false. Stored inverted so the
	// zero value (all existing ToolSet constructions) means ENABLED — matching the
	// DB default (true). When true: graphOverviewData drops the coverage block, the
	// auto-scope hook (insertAssets) is skipped, and add_task_scope/list_untested_assets
	// are filtered out of the agent's tool list. The scope field stays regardless.
	coverageDisabled bool
	// breadthCached is the per-ToolSet snapshot of the concrete-asset breadth
	// ledger. Planner ToolSets live one round, so this is a round snapshot; sharing
	// it between graphOverviewData and add_intent avoids a second matrix query.
	breadthLoaded bool
	breadthCached *db.BreadthSummary
	// coverageCached 是本轮 task_scope 覆盖矩阵的快照：探索台账与 coverage_matrix 工具
	// 共用一次查询（矩阵本身是一次 CTE）。ToolSet 每轮一个，失效即重建。
	coverageLoaded bool
	coverageCached []db.CoverageMatrixAsset
	ledgerLoaded   bool
	ledgerCached   *db.LedgerReport
	chainsLoaded   bool
	chainsCached   *db.ChainReport
	// completionGate 是收官审计的语气（block/notice/off），由 server 注入、每轮现读。
	// nil = off（agent 单测/无 server 上下文时保持旧行为）。
	completionGate func() string
	// completionBlocked 是"审计拒收了一次"的通知（server 用来累计指标）。nil = 不记。
	completionBlocked func()
	// forceReview 让下一轮强制复盘（收官审计拒收后：先补空白再重试）。nil = 不支持。
	forceReview func()
	// breadthNewDebt tracks debt assets already targeted by intents created in this
	// ToolSet, so a second add_intent call in the same round does not re-warn for
	// the same asset.
	breadthNewDebt map[int64]bool
	// execService（P13）由 host 注入：长任务执行的托管后端。nil = 未接线，
	// 四个执行工具会返回可读拒绝而不是静默消失。
	execService ExecService
	// pol（P20）是本任务的探索策略：意图层硬门（add_intent）按它判定。
	// nil / 零策略 = 不做任何策略判定（行为与从前完全一致）。
	pol *policy.Effective
	// workerCap 是本任务的 worker 池大小（0 = 未知/不启用）。用途只有一个：
	// 队列背压——待领意图超过 intentQueueFactor×workerCap（默认 3×）时拒收低优先级新意图。
	workerCap int
	// intentsRound 计**本轮**成功创建的意图数（pace:round 额度）。
	//
	// 它曾经等于「本 ToolSet 生命周期内创建的意图数」，因为 planner 每轮新建一个
	// ToolSet。热会话复用打破了这个前提：agentcore.Session 的工具注册表在 NewSession
	// 时固定，复用时第 2 轮起调用的仍是第 1 轮那个 ToolSet 的闭包，计数永远停在上一轮
	// 的值——于是每一轮都被 pace:round 硬拒，最终被判「策略封死」（任务 18 实测）。
	// 现在由 BeginRound 在每轮开头显式清零，与 ToolSet 是否复用无关。
	intentsRound int
	// emitPolicy 是"意图被策略拒绝"的留痕通道（引擎的 emitActivity）。
	// nil 时退回直接写库 + 日志，保证不静默丢。
	emitPolicy func(db.Activity)
	// ownerNode is the exploration node that writes attach to: assets this run
	// touches get anchored to it as lineage/provenance (NOT visibility — the asset
	// graph is global and shared). Worker = its claimed intent; planner = begin root.
	ownerNode int64
	// ownerIsIntent marks ownerNode as the intent this run currently owns (worker).
	// Only then do write-back tools enforce an explicit intent_id: a reused worker
	// slot keeps other intents' history, and a stale id must not re-attach today's
	// facts/findings to a finished intent. Planner's owner is the origin fact, so it
	// must stay exempt.
	ownerIsIntent bool
	GoalMet       bool
	Reason        string
	writes        WriteCounts
	// killWork, if set, terminates a running work by intent id (engine callback,
	// wired by the planner). nil = the kill_work tool reports unavailable.
	killWork func(intentID int64) error
	// steerWork, if set, queues a mid-run course-correction for the work running an
	// intent id (engine callback, wired by the planner): the worker injects it before
	// its next tool call and re-plans, without being killed. nil = tool unavailable.
	steerWork func(intentID int64, msg string) error
	// rotateEgressFn 是「切换 Clash 出口」的宿主回调；nil = 未配置（工具返回可读拒绝）。
	rotateEgressFn func(reason string) (string, error)
	// exhaustedFn 是 planner 的「策略穷尽」信号：mark_strategy_exhausted 写完结论
	// 事实后把原因交给 Planner；引擎在本轮**结束之后**再暂停任务。不在工具调用中途
	// 取消自己的 ctx——那会让本轮的结论与回执一起丢掉。
	exhaustedFn func(reason string)
	// exhaustCooldownFn 读出「穷尽复核否决后的剩余冷却轮数」。>0 时
	// mark_strategy_exhausted 直接返回，逼 planner 先补复核点名的缺口；用闭包而不是
	// 快照，才能挡住同一轮内的再次声明。nil = 不冷却。
	exhaustCooldownFn func() int
	// reviewRound 为真时，本轮是复盘轮：add_intent 最多 3 条（复盘优先于铺量）。
	reviewRound bool
	// openingStrict 为真时，粗粒度（只有风险类、没有具体入口）的意图直接拒收。
	// 只在任务开局轮由 planner 打开：那一轮还没有任何事实，重写一条具体方向是唯一
	// 有意义的动作；任务中后段则只警告不拦（已有事实可依，拦下来只会浪费一轮）。
	openingStrict bool
	// enrich, if set, receives async auto-completion triggers (DNS resolve for a
	// domain, HTTP probe for a site). nil = no engine enrichment.
	enrich EnrichTrigger
	// notify, if set, wakes the task's planner after a graph change that should be
	// re-planned promptly (currently: a new hint). nil = no wake (the hint is still
	// stored and read on the next round triggered by other events). debounced.
	notify func()
	// notifyFinding, if set, wakes the task's planner when this run reports a finding,
	// carrying (intentID, summary) so the round can spell out which intent found what.
	// Wired for workers; nil elsewhere → falls back to notify (bare wake).
	notifyFinding func(intentID int64, summary string)
	// resumeTask, if set, revives the task after a graph change that should make a
	// stopped task run again (currently: set_goals adds a goal). It flips a terminal/
	// paused task back to running and (re)starts the engine loops — a plain notify()
	// can't, because the planner's terminal gate swallows wakes. Wired ONLY for the
	// main agent (human steering); nil for the goals decomposer and workers.
	resumeTask func()
	// notifyGoal, if set, wakes the planner AND records ONE "人新增了 N 个目标：…" trigger
	// for a whole set_goals call (batch-aware — one call, one trigger, not one per goal)
	// so the next round spells out the added goals (instead of the planner having to
	// spot new open goals in the overview). Wired ONLY for the main agent; nil for the
	// goals decomposer (round-0 has no running planner to inform) and workers → those
	// fall back to the bare notify.
	notifyGoal func(texts []string)
	// notifyHint, if set, wakes the planner AND records ONE "人新增了 N 条战略提示：…"
	// trigger for a whole add_hint call (batch-aware — one call, one trigger) so the next
	// round is told the round was fired by a new hint and spells the hint out, instead of
	// the planner having to spot it folded into the graph overview. Wired for the main
	// agent + cross-task orchestration; nil elsewhere → falls back to the bare notify.
	notifyHint func(texts []string)
}

// SetNotifyGoal wires the goal-add trigger callback (see ToolSet.notifyGoal). Set only
// by the main-agent chat, so runtime-added goals are announced to the planner by name.
func (t *ToolSet) SetNotifyGoal(fn func([]string)) { t.notifyGoal = fn }

// SetNotifyHint wires the hint-add trigger callback (see ToolSet.notifyHint). Set by
// the main-agent chat and cross-task orchestration, so a runtime-added hint fires a
// planner round announced by name instead of a bare wake.
func (t *ToolSet) SetNotifyHint(fn func([]string)) { t.notifyHint = fn }

// SetResumeTask wires the task-revive callback (see ToolSet.resumeTask). Set only by
// the main-agent chat, so runtime-added goals can pull a finished task back to running.
func (t *ToolSet) SetResumeTask(fn func()) { t.resumeTask = fn }

// SetNotify wires the planner-wake callback (see ToolSet.notify). Set by callers
// that hold the task handle (main-agent chat, cross-task orchestration).
func (t *ToolSet) SetNotify(fn func()) { t.notify = fn }

// SetNotifyFinding wires the finding-wake callback (see ToolSet.notifyFinding).
func (t *ToolSet) SetNotifyFinding(fn func(int64, string)) { t.notifyFinding = fn }

// SetRotateEgressHandler wires the host's Clash egress rotation callback.
func (t *ToolSet) SetRotateEgressHandler(fn func(reason string) (string, error)) {
	t.rotateEgressFn = fn
}

// SetStrategyExhaustedHandler wires the planner's "no viable direction" signal
// (see ToolSet.exhaustedFn). Set only on the planner's ToolSet.
func (t *ToolSet) SetStrategyExhaustedHandler(fn func(string)) { t.exhaustedFn = fn }

// exhaustCooldown 返回穷尽复核否决后的剩余冷却轮数（0 = 可声明）。
func (t *ToolSet) exhaustCooldown() int {
	if t.exhaustCooldownFn == nil {
		return 0
	}
	return t.exhaustCooldownFn()
}

// EnrichTrigger is the enrichment engine seen from the tool layer (see package
// enrich). Kept as an interface here to avoid coupling agent → enrich.
type EnrichTrigger interface {
	ResolveDomain(id int64, host string)
	ProbeSite(id int64, url string)
}

// WriteCounts breaks down what a worker persisted this run, by node kind, so the
// engine can log an accurate "wrote back" summary instead of lumping assets and
// findings under "facts" (record_fact → Facts, insert_assets → Assets,
// report_finding → Findings, report_foothold/report_credential → 战果; each
// element of a batch counts once).
type WriteCounts struct {
	Facts       int
	Assets      int
	Findings    int
	Footholds   int
	Credentials int
	Detections  int
	Artifacts   int
	Footprints  int
	// ValidationAssets 是本轮"验证产出"锚定到的资产 id 集合（事实/发现/战果/
	// 检测/遗留物）。verify 意图绑定了多个资产时，引擎据此记录它实际验证了哪些，
	// 覆盖矩阵不再把没验证过的资产算成已测。
	ValidationAssets map[int64]bool
}

// noteValidationAssets 记录验证产出锚定的资产。
func (w *WriteCounts) noteValidationAssets(ids []int64) {
	if len(ids) == 0 {
		return
	}
	if w.ValidationAssets == nil {
		w.ValidationAssets = make(map[int64]bool, len(ids))
	}
	for _, id := range ids {
		if id > 0 {
			w.ValidationAssets[id] = true
		}
	}
}

// ValidationAssetIDs 返回排序后的验证锚定资产 id（稳定输出，便于落库与测试）。
func (w WriteCounts) ValidationAssetIDs() []int64 {
	if len(w.ValidationAssets) == 0 {
		return nil
	}
	out := make([]int64, 0, len(w.ValidationAssets))
	for id := range w.ValidationAssets {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ValidationTotal is the "verification result" subset: facts, findings, loot,
// detections and artifacts — everything except merely registered assets. A verify
// intent that only ran insert_assets proved nothing (see settleIntentState).
func (w WriteCounts) ValidationTotal() int {
	// 痕迹的登记/清理不是“验证了目标”，不计入验证产出。
	return w.Facts + w.Findings + w.Footholds + w.Credentials + w.Detections + w.Artifacts
}

// Total is every node persisted this run, regardless of kind — the
// "explored but persisted nothing" signal (Total == 0).
func (w WriteCounts) Total() int {
	return w.Facts + w.Assets + w.Findings + w.Footholds + w.Credentials + w.Detections + w.Artifacts + w.Footprints
}

// String renders the per-kind breakdown for logs, e.g. "事实1 资产25 漏洞0 据点1 凭据2".
func (w WriteCounts) String() string {
	return fmt.Sprintf("事实%d 资产%d 漏洞%d 据点%d 凭据%d 检测%d 遗留物%d 痕迹%d",
		w.Facts, w.Assets, w.Findings, w.Footholds, w.Credentials, w.Detections, w.Artifacts, w.Footprints)
}

// Writes reports what this run wrote back, split by node kind (so the engine can
// tell "explored but persisted nothing" apart from a completed intent, and log an
// honest breakdown instead of calling assets/findings "facts").
func (t *ToolSet) Writes() WriteCounts { return t.writes }

func NewToolSet(ts *db.ExplorationStore, worker string) *ToolSet {
	return &ToolSet{ts: ts, worker: worker}
}

// SetTaskID sets the PG task id on this ToolSet so that report_finding can
// dual-write to the standalone findings table (which survives task deletion).
func (t *ToolSet) SetTaskID(id int64) { t.taskID = id }

// SetTaskClock 注入本轮的任务截止信息（nil/零值 = 没有期限，行为不变）。
func (t *ToolSet) SetTaskClock(tc TaskClock) { t.clock = tc }

// SetCompletionGate 注入收官审计语气：block（默认）/ notice / off。nil 或空 = off。
func (t *ToolSet) SetCompletionGate(fn func() string) { t.completionGate = fn }

// SetCompletionBlocked 注入"审计拒收"的回调（指标用；nil = 不记）。
func (t *ToolSet) SetCompletionBlocked(fn func()) { t.completionBlocked = fn }

// SetForceReview 注入"下一轮强制复盘"的回调（收官审计拒收后）。nil = 不支持。
func (t *ToolSet) SetForceReview(fn func()) { t.forceReview = fn }

// completionGateMode 读当前语气；未注入时 off（不改变 agent 单测/无 server 场景的行为）。
func (t *ToolSet) completionGateMode() string {
	if t.completionGate == nil {
		return "off"
	}
	switch m := strings.ToLower(strings.TrimSpace(t.completionGate())); m {
	case "block", "notice", "off":
		return m
	default:
		return "block"
	}
}

// completionGaps 是收官审计的 agent 侧入口（db 层实现，server 的 goalless 路径也用它）。
func (t *ToolSet) completionGaps() []db.CompletionGap {
	if t.ts == nil || t.taskID <= 0 {
		return nil
	}
	gaps, err := t.ts.CompletionGaps(t.as, db.CompletionOptions{
		TaskID: t.taskID, CoverageEnabled: !t.coverageDisabled,
	})
	if err != nil {
		return nil // 读不出来不阻断收尾：门坏了不能把人锁在里面
	}
	return gaps
}

// formatCompletionGaps 把缺口渲染成可直接执行的一段话。
func formatCompletionGaps(gaps []db.CompletionGap) string {
	var b strings.Builder
	b.WriteString("目标已达成，但探索还不充分（收官审计）：")
	for _, g := range gaps {
		b.WriteString("\n- " + g.Detail)
	}
	b.WriteString("\n\n处理完再来 prove_goal；确实接受风险的，用 accept_completion_risk 写明理由（会进报告的数据缺项）。")
	return b.String()
}

// endgameWarning 是收官期的**软信号**（不拒收）。硬拒有风险：收官期仍可能需要清理/
// 补齐意图，按 kind 区分又不足以说明"这条是不是新探索面"。与 breadth_warning 同形态。
func (t *ToolSet) endgameWarning() string {
	if !t.clock.Endgame(time.Now()) {
		return ""
	}
	return "任务已进入收官期：不再新开探索面。除非这条意图是把在跑方向推到结论、清理遗留物或补齐报告缺项，否则本轮应当收笔而不是继续扩面。"
}

// SetCoverageEnabled records whether this task has the asset-coverage feature on
// (default enabled). Passing false makes graphOverviewData omit the coverage block
// and DropCoverageTools filter the two coverage-only tools out of the agent's tool
// list. It does NOT stop scope accumulation: insertAssets' auto-scope hook runs
// either way, because task_scope is the task's range boundary (the filter basis for
// asset queries), not merely a coverage denominator.
func (t *ToolSet) SetCoverageEnabled(enabled bool) { t.coverageDisabled = !enabled }

// CoverageDisabled reports whether the coverage feature is off for this task.
func (t *ToolSet) CoverageDisabled() bool { return t.coverageDisabled }

// coverageOnlyTools are the LLM tools that only make sense when asset coverage is
// on. When the feature is off they are filtered out of the agent's tool list so
// they neither pollute the prompt nor let the model build a disabled denominator.
// add_task_scope is deliberately NOT here: task_scope is the task's range boundary
// (the filter basis for asset queries), not merely a coverage denominator, so the
// agents that own范围定义 keep it either way — in lockstep with insertAssets'
// auto-scope hook, which also runs regardless of the switch.
var coverageOnlyTools = map[string]bool{"list_untested_assets": true, "coverage_matrix": true}

// DropCoverageTools returns tools with the coverage-only ones removed when this
// task has the feature disabled; otherwise it returns tools unchanged.
func (t *ToolSet) DropCoverageTools(tools []actool.CoreTool) []actool.CoreTool {
	if !t.coverageDisabled {
		return tools
	}
	out := tools[:0:0]
	for _, tool := range tools {
		if coverageOnlyTools[tool.Name()] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// Cross-task reuse: exported accessors returning the per-task tool logic bound to
// THIS ToolSet's store. Host-side orchestration tools build a ToolSet for an
// arbitrary task, then Call these — so cross-task reads/hint reuse the exact
// same logic as the in-task tools. (readTool ignores ToolContext, so Call(…,nil)
// is safe; add_hint is a writeTool but also doesn't deref the context here.)
func (t *ToolSet) GraphOverviewTool() actool.CoreTool      { return t.graphOverview() }
func (t *ToolSet) ListFindingsTool() actool.CoreTool       { return t.listFindings() }
func (t *ToolSet) GetWorkerTraceTool() actool.CoreTool     { return t.getWorkerTrace() }
func (t *ToolSet) ListWorkerTracesTool() actool.CoreTool   { return t.listWorkerTraces() }
func (t *ToolSet) SearchWorkerTracesTool() actool.CoreTool { return t.searchAllWorkerTraces() }
func (t *ToolSet) NodeDetailTool() actool.CoreTool         { return t.nodeDetail() }
func (t *ToolSet) AddHintTool() actool.CoreTool            { return t.addHint() }

// SetEnrich wires the async enrichment engine (DNS/HTTP auto-completion).
func (t *ToolSet) SetEnrich(e EnrichTrigger) { t.enrich = e }

// SetOwnerNode sets the exploration node that writes anchor to (worker: its
// intent node; planner/main: the begin root). Assets created/referenced while
// ownerNode is set are anchored to it as lineage (not visibility).
func (t *ToolSet) SetOwnerNode(id int64) { t.ownerNode = id }

// SetOwnerIntent marks the owner as the worker's current intent, enabling the
// stale-intent write-back guard (worker slot transcripts span multiple intents).
func (t *ToolSet) SetOwnerIntent(id int64) {
	t.ownerNode = id
	t.ownerIsIntent = id > 0
}

// writeIntent resolves an explicit intent_id for a write-back tool. In a worker
// run, an empty id falls back to the current intent, and a different id is
// rejected: the slot's history still shows old intents, and silently attaching
// new results to them would corrupt the graph's yields edges.
func (t *ToolSet) writeIntent(raw json.RawMessage) (int64, error) {
	return t.writeIntentID(pid(raw))
}

func (t *ToolSet) writeIntentID(id int64) (int64, error) {
	if !t.ownerIsIntent || t.ownerNode <= 0 {
		return id, nil
	}
	if id <= 0 {
		return t.ownerNode, nil
	}
	if id != t.ownerNode {
		return 0, fmt.Errorf("intent_id=%d 不是当前运行的意图 #%d：槽位历史里的旧意图只作参考，写回只能挂当前意图", id, t.ownerNode)
	}
	return id, nil
}

// SetExecService 注入长任务执行服务（P13）；nil 表示本运行环境不支持后台执行。
func (t *ToolSet) SetExecService(svc ExecService) { t.execService = svc }

// SetPolicy 注入任务级探索策略（P20）；nil / 零策略 = 不判定。
func (t *ToolSet) SetPolicy(p *policy.Effective) { t.pol = p }

// SetWorkerCap 注入本任务的 worker 池大小，用于队列背压（0 = 不启用）。
// 由 planner 按**任务运行时的池快照**注入——实时读设置会让改过 workers 的任务按错误容量排队。
func (t *ToolSet) SetWorkerCap(n int) { t.workerCap = n }

// SetPolicyAudit 注入"意图被策略拒绝"的留痕通道（引擎活动流）。
func (t *ToolSet) SetPolicyAudit(fn func(db.Activity)) { t.emitPolicy = fn }

// BeginRound 把 ToolSet 上所有「轮级」状态重置为本轮初值。
//
// 为什么需要它：planner 正常路径每轮新建 ToolSet，这些字段天然是新的；但**热会话复用**
// 时 Session 的工具注册表固定在第 1 轮那个 ToolSet 上（SDK 的 NewSession 之后无法替换
// registry），于是第 2 轮起仍调用同一个闭包。不重置的话：
//   - intentsRound 停在上一轮的值 → 每轮都被 pace:round 拒（任务 18 被判策略封死）；
//   - GoalMet 残留 → 上一轮判定达成后，本轮所有 add_intent 被拒；
//   - breadthCached / breadthNewDebt 是上一轮的快照 → 广度提示与实际不符；
//   - writes 会跨轮累加，收尾统计失真。
//
// 每轮由 Planner.Plan 在调用模型之前调一次；热会话复用时调的是**会话工具闭包绑定的
// 那个** ToolSet（首轮创建的那个），而不是当轮新建的实例——见 planner.go 的 wireToolSet。
func (t *ToolSet) BeginRound(pol *policy.Effective, workerCap int, clock TaskClock, ownerNode int64) {
	t.intentsRound = 0
	t.GoalMet = false
	t.Reason = ""
	t.writes = WriteCounts{}
	t.pol = pol
	t.workerCap = workerCap
	t.clock = clock
	t.ownerNode = ownerNode
	// 广度快照按轮重取：资产/意图在上一轮结束后可能已经变了。
	t.breadthLoaded = false
	t.breadthCached = nil
	t.breadthNewDebt = map[int64]bool{}
}

// beginIntentQuota 在一次 add_intent 调用开始时取额度快照：库存量各查一次库，
// 本轮用量从 ToolSet 计数起算并在批量循环里累加（一批 4 条不该打 8 次库）。
func (t *ToolSet) beginIntentQuota() *intentQuota {
	q := &intentQuota{round: t.intentsRound}
	if t.ts != nil && (t.workerCap > 0 || (t.pol != nil && !t.pol.IsZero())) {
		if n, err := t.ts.CountOpenIntents(); err == nil {
			q.open = n
		}
		if n, err := t.ts.CountIntents(); err == nil {
			q.total = n
		}
	}
	// 队列背压只看可领数（被前置挡住的步骤不消耗 worker）。
	if t.ts != nil && t.workerCap > 0 {
		if n, err := t.ts.CountClaimableIntents(); err == nil {
			q.claimable = n
		}
	}
	return q
}

// intentQuota 是一次 add_intent 调用期间共享的额度视图。
type intentQuota struct {
	open  int
	total int
	round int
	// claimable 是"现在就能被 worker 领走"的条数（不含 depends_on 未满足的）。队列背压按它算：
	// 被前置挡住的步骤不消耗 worker，拿它们占额度会在 worker 全空闲时反而拒新活。
	claimable int
	// pool 是防打转判定的对比池（见 intent_dedup.go）：同一批里的重复也要当场拦住，
	// 所以整批共享一份，每建一条就追加进去。
	pool       []intentFP
	poolLoaded bool
}

// reportPolicyReject 把一次拒绝写进时间线：模型想做什么、被哪条策略拦、命中什么。
// 图里不落这条意图（刻意——不污染探索与去重），但人必须看得见。
func (t *ToolSet) reportPolicyReject(it intentItem, v policy.Verdict) {
	meta, _ := json.Marshal(map[string]any{
		"intent_summary": it.Summary,
		"policy_item":    v.ItemID,
		"item_label":     v.ItemLabel,
		"where":          v.Where,
		"matched":        v.Matched,
		"benign":         v.Benign,
		"risk_area":      db.NormalizeRiskArea(it.RiskArea),
	})
	summary := fmt.Sprintf("探索策略拒绝：%s", v.ItemLabel)
	if v.Benign {
		summary = fmt.Sprintf("探索策略放行（否定语境，仅留痕）：%s", v.ItemLabel)
	}
	act := db.Activity{Worker: "system", Kind: "policy_reject", Summary: summary,
		Detail:   fmt.Sprintf("意图：「%s」\n命中：%s（%s）%s\n", it.Summary, v.ItemLabel, v.ItemID, matchNote(v)),
		Metadata: meta}
	if t.emitPolicy != nil {
		t.emitPolicy(act)
		return
	}
	if t.ts != nil {
		if _, err := t.ts.AppendActivity(act); err != nil {
			log.Printf("[policy] 拒绝留痕失败: %v", err)
		}
	}
}

func matchNote(v policy.Verdict) string {
	if v.Matched == "" {
		return ""
	}
	return "命中词「" + v.Matched + "」"
}

// anchorOwner records a lineage edge from this run's owner node to an asset
// (no-op if unset). Provenance only — the asset graph is global and shared, so
// this no longer affects which assets a task can read.
func (t *ToolSet) anchorOwner(assetID int64) {
	if t.ts != nil && t.ownerNode > 0 && assetID > 0 {
		_ = t.ts.Anchor(t.ownerNode, assetID)
	}
}

// pid parses an id that may arrive as a JSON number or string ("" / 0 → 0).
func pid(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// pidList parses a list of ids (number|string), dropping zeros/invalids.
func pidList(raw []json.RawMessage) []int64 {
	var out []int64
	for _, r := range raw {
		if v := pid(r); v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func idp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func readTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:    func(json.RawMessage) bool { return true },
		Concurrent:  func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func writeTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// readExpTool / writeExpTool build a domain tool whose handler dereferences the
// task-bound ExplorationStore. Two ToolSets carry a nil store: the catalog's
// seed-only shell (never called) and the server-level one behind buildDomainReg,
// which the tools table can bind to ANY agent — including ones that never run
// inside a task (auto/pentest/reporter/自定义 agent/旁路提问). Refusing there
// keeps a mis-bound tool a bad tool call; without the guard it was a nil deref,
// and tool handlers run on the harness's own goroutine, so the panic is out of
// reach of every recover() in the server and kills the whole process.
func (t *ToolSet) readExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return readTool(name, desc, schema, t.needExploration(name, run))
}

func (t *ToolSet) writeExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return writeTool(name, desc, schema, t.needExploration(name, run))
}

// needExploration wraps a handler so it only runs with an exploration store.
// Tools that degrade more usefully than "unavailable" (report_finding points at
// add_task_hint, set_goals/set_constraints at the task itself) keep their own
// bespoke guard instead.
func (t *ToolSet) needExploration(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) func(context.Context, json.RawMessage) (actool.Result, error) {
	return func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if t.ts == nil {
			return actool.Errorf(name + " 需要任务上下文（探索图）：当前 agent 不在某个任务内运行，取不到任务的探索图，该工具不可用。请在任务内使用它，或改用带 task_id 的跨任务读取工具（get_task_node_detail / list_task_findings / get_task_graph 等）。"), nil
		}
		return run(ctx, in)
	}
}

func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// --- read tools (planner + worker) ---

func (t *ToolSet) graphOverview() actool.CoreTool {
	return t.readExpTool("graph_overview",
		"探索态势。默认返回有界黑板视图（真实总数与窗口分离、活动意图/最近知识/路线/增量事件）；detail=true 返回完整态势（字段多、token 高，需要折叠区/继承任务/AD 图等时用）。",
		obj(map[string]any{
			"detail": map[string]any{"type": "boolean", "description": "true=完整态势；默认 false=有界黑板视图"},
			"after":  intp("事件游标：只返回 id>after 的节点（用上次 blackboard.next_cursor）"),
			"limit":  intp("事件窗口 1..100，默认 20"),
		}),
		func(_ context.Context, raw json.RawMessage) (actool.Result, error) {
			var a struct {
				Detail bool  `json:"detail"`
				After  int64 `json:"after"`
				Limit  int   `json:"limit"`
			}
			_ = json.Unmarshal(raw, &a)
			if a.Detail {
				return jsonResult(t.graphOverviewData())
			}
			return jsonResult(t.blackboardViewData(a.After, a.Limit))
		})
}

func (t *ToolSet) blackboardView() actool.CoreTool {
	return t.readExpTool("blackboard_view",
		"有界黑板视图：真实总数、活动意图（含 contract）、最近知识、路线聚合与增量事件。窗口不是全量；更早的详情用 graph_overview(detail=true) / list_facts / list_worker_traces 翻页。",
		obj(map[string]any{
			"after": intp("事件游标：只返回 id>after 的节点（用上次 blackboard.next_cursor）"),
			"limit": intp("事件窗口 1..100，默认 20"),
		}),
		func(_ context.Context, raw json.RawMessage) (actool.Result, error) {
			var a struct {
				After int64 `json:"after"`
				Limit int   `json:"limit"`
			}
			_ = json.Unmarshal(raw, &a)
			return jsonResult(t.blackboardViewData(a.After, a.Limit))
		})
}

// recentDoneCap 是态势里 recent_done_intents 的窗口。
//
// 12 太小：上百意图的任务里已结束意图有 50~80 条，12 条窗口之外的既不在折叠摘要里、
// 也不在常规视野里，planner 会把试过的方向当新方向重派（实测任务 5：86 条已结束，
// 62% 不在任何可见窗口）。30 条覆盖绝大多数任务；更旧的用 list_worker_traces 翻页。
const recentDoneCap = 30

// 折叠区意图索引的体积上限：摘要截 60 字、最多 100 条。超出只给计数——上百条意图的
// 任务里，全量索引会挤占真正有用的态势内容，更旧的走 list_worker_traces。
//
// 60 字的依据（实测任务 6 的 45 条折叠意图）：
//
//	摘要 40 字 → 3.7k 字符；60 字 → 4.6k；80 字 → 5.5k；100 字 → 6.4k
//
// 60 字已能写清「对哪个入口、做什么测试」；再长主要是结论细节，那些看 expand_digest
// 更合适。按 100 条上限估算，最坏约 1 万字符（≈3–4k token）/轮。
const (
	foldedIntentSummaryMax = 60
	foldedIntentsCap       = 100
)

// recentFactsCap 是态势里 recent_facts 的窗口。它只是「最新一窗」，不是全部——
// 超过窗口时概览会给出 recent_facts_next_before，让 planner 用 list_facts 接着翻。
const recentFactsCap = 20

// breadthSummary returns this round's concrete-asset breadth ledger. It is
// cached per ToolSet so graphOverviewData and add_intent share one matrix query.
func (t *ToolSet) breadthSummary() *db.BreadthSummary {
	if t.coverageDisabled || t.ts == nil || t.taskID <= 0 {
		return nil
	}
	if t.breadthLoaded {
		return t.breadthCached
	}
	t.breadthLoaded = true
	if b, err := t.ts.BreadthSummary(t.taskID); err == nil {
		t.breadthCached = b
	}
	return t.breadthCached
}

// scopeSurface 是本任务「范围面」的快照：范围内的注册域、以及同根域下**还没被任何
// 意图碰过**的资产。它是「目标正面打不进去时，自动转去试范围内的其它资产」这条策略的
// 依据——planner 必须每轮都看得到还剩哪些同主体资产没试过，而不是凭记忆判断。
type scopeSurface struct {
	RootDomains []string `json:"root_domains"`
	// Untouched 是同根域内**没有任何意图引用**的具体资产（不含 root_domain 与 service）。
	Untouched []scopeAssetRef `json:"untouched_siblings"`
	// UntouchedTotal 是真实总数（Untouched 只是展示窗口）。
	UntouchedTotal int `json:"untouched_siblings_total"`
	// SubdomainDiscoveryPending 为真表示：范围内的注册域在资产图里只有目标主机本身
	// （同根子域一条都没登记）。此时「横向」不是"去试别的资产"，而是**先去发现**——
	// 用 intel_search（domain="<根域>"）与 DNS/证书透明度把同根子域找出来，再用
	// insert_assets 登记（登记即自动入范围）。任务 18 就是这种形状：范围里只有
	// www.vaticanum.com，资产图里也一个兄弟都没有，于是"横向"无从谈起。
	SubdomainDiscoveryPending bool `json:"subdomain_discovery_pending,omitempty"`
	// DiscoveryHints 是给 planner 的具体查询表达式（原样可用的 FOFA 语法）。
	DiscoveryHints []string `json:"discovery_hints,omitempty"`
	// Shallow 是「测过、但深度不足」的资产：每个资产的终结风险类少于阈值时列出，
	// 并给出还没终结的风险类。breadth 的 debt/pct 只回答"碰没碰过"，这里回答
	// "碰到什么程度"——没有这一层，广度达标与测得很浅可以同时成立。
	Shallow      []shallowAssetRef `json:"shallow_assets,omitempty"`
	ShallowTotal int               `json:"shallow_assets_total,omitempty"`
	// DepthThreshold 是上面那层用的阈值（每个资产至少要终结多少个风险类）。
	DepthThreshold int `json:"depth_threshold,omitempty"`
}

type shallowAssetRef struct {
	ID      int64    `json:"id"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Done    []string `json:"done_risk_areas,omitempty"`
	Missing []string `json:"missing_risk_areas,omitempty"`
}

// siblingDiscoveryThreshold：同根域内已登记的具体资产（含目标本身）少于这个数时，
// 认为"还没做同根子域发现"。取 2 是因为 1 个就等于"只有目标主机自己"。
const siblingDiscoveryThreshold = 2

type scopeAssetRef struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

const scopeUntouchedWindow = 12

// scopeSurface 取范围面快照。AssetStore/任务上下文缺失时返回 nil（态势里不出现该字段）。
func (t *ToolSet) scopeSurface() *scopeSurface {
	if t.as == nil || t.taskID <= 0 || t.ts == nil {
		return nil
	}
	roots, err := t.as.ScopeRootDomains(t.taskID)
	if err != nil || len(roots) == 0 {
		return nil
	}
	out := &scopeSurface{RootDomains: roots, Untouched: []scopeAssetRef{}}
	assets, total, err := t.as.UntouchedSiblings(t.taskID, roots, scopeUntouchedWindow)
	if err != nil {
		return out
	}
	for _, a := range assets {
		out.Untouched = append(out.Untouched, scopeAssetRef{ID: a.ID, Type: a.Type, Label: a.Label})
	}
	out.UntouchedTotal = total
	// 深度面：测过但风险类终结数不足的资产。与 untouched 是两个维度——
	// untouched 回答"有没有碰过"，这里回答"碰到什么程度"。
	if gaps, total, err := t.ts.DepthGaps(t.taskID, db.DefaultDepthAreas, 8); err == nil && total > 0 {
		out.ShallowTotal = total
		out.DepthThreshold = db.DefaultDepthAreas
		for _, g := range gaps {
			out.Shallow = append(out.Shallow, shallowAssetRef{
				ID: g.AssetID, Type: g.Type, Label: g.Label,
				Done: g.DoneAreas, Missing: g.Missing,
			})
		}
	}
	// 同根子域一条都没登记 → 横向无从谈起，先做发现。判定用**资产图里的同根具体资产数**
	// （不是范围行数：范围里可能只有一条 subdomain，而资产图里已经有 10 个兄弟）。
	if known, err := t.as.CountSiblingsInGraph(roots); err == nil && known < siblingDiscoveryThreshold {
		out.SubdomainDiscoveryPending = true
		for _, r := range roots {
			out.DiscoveryHints = append(out.DiscoveryHints,
				fmt.Sprintf(`intel_search: domain="%s"（再补 cert="%s"、ip=<目标解析到的 IP>）`, r, r))
		}
	}
	return out
}

// graphOverviewData computes the distilled situational snapshot shared by the
// graph_overview tool and the planner's wake-up prompt (which pre-injects it so
// the model needn't spend a turn calling the tool — every plan round starts with
// an empty context and always needs this first).
func (t *ToolSet) graphOverviewData() map[string]any {
	return t.graphOverviewDataInternal(true)
}

// graphOverviewDataForWorker omits the breadth ledger: a worker acts on one
// intent and must not try to widen its job. Skipping it also avoids a needless
// coverage-matrix query for every worker run.
func (t *ToolSet) graphOverviewDataForWorker() map[string]any {
	out := t.graphOverviewDataInternal(false)
	// 线索/未消费事实/冷区事实索引/能力/已否定方向是**规划层**的决策材料：
	// worker 只该做手里这一条意图，看到这些会诱发自行扩面。显式过滤，避免
	// 以后往 overview 加字段时又漏进 worker 上下文。
	for _, k := range []string{
		"recent_leads", "leads_total", "leads_open", "leads_converted", "leads_dismissed",
		"unconsumed_facts", "capabilities", "ruled_out", "folded_facts", "folded_facts_total",
		"ledger", "chains",
	} {
		delete(out, k)
	}
	return out
}

// blackboardViewData is the bounded read-side projection used by the planner's
// pre-fetched situation block and the blackboard_view tool. It keeps the real
// totals separate from display windows, adds revision/event cursor/route
// aggregation, and drops bulky derived sections that are one detail call away.
func (t *ToolSet) blackboardViewData(after int64, limit int) map[string]any {
	if after < 0 {
		after = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	out := t.graphOverviewData()
	// Capture totals before pruning fields.
	totals := map[string]any{}
	if states, err := t.ts.CountIntentStates(); err == nil {
		totals["intents"] = states
	}
	if v, ok := out["facts"]; ok {
		totals["facts"] = v
	}
	if v, ok := out["findings_total"]; ok {
		totals["findings"] = v
	}
	if v, ok := out["footholds_total"]; ok {
		totals["footholds"] = v
	}
	if v, ok := out["credentials_total"]; ok {
		totals["credentials"] = v
	}
	if v, ok := out["edges_total"]; ok {
		totals["edges"] = v
	}
	if v, ok := out["frontier_open"]; ok {
		totals["open_intents"] = v
	}
	if v, ok := out["done_intents_total"]; ok {
		totals["done_intents"] = v
	}
	if v, ok := out["claimable_intents"]; ok {
		totals["claimable"] = v
	}

	// Bulky/derived sections stay available through graph_overview(detail=true).
	for _, k := range []string{
		"folded_facts", "folded_intents", "folded_intents_note",
		"related_tasks", "cold_digests_more",
	} {
		delete(out, k)
	}
	truncateMaps := func(key string, n int) {
		if arr, ok := out[key].([]map[string]any); ok && len(arr) > n {
			out[key] = arr[:n]
		}
	}
	truncateMaps("open_intents", 12)
	truncateMaps("running_intents", 8)
	truncateMaps("paused_intents", 8)
	truncateMaps("recent_done_intents", 10)
	truncateMaps("recent_facts", 10)
	truncateMaps("finding_list", 5)
	truncateMaps("footholds", 3)
	truncateMaps("credentials", 3)
	truncateMaps("cold_digests", 3)
	truncateMaps("hints", 20)
	truncateMaps("goals", 20)
	for _, key := range []string{"matching_playbooks", "matching_cves", "company_knowledge"} {
		if arr, ok := out[key].([]string); ok && len(arr) > 3 {
			out[key] = arr[:3]
		}
	}

	// Incremental events. With a cursor we page forward (oldest first) and ask
	// for one extra row to report has_more. Without a cursor the first view shows
	// the LATEST page; has_earlier says older history exists but is behind the
	// forward-only cursor, so it must not be reported as has_more.
	events := make([]map[string]any, 0, limit)
	hasMore, hasEarlier := false, false
	probe := limit + 1
	nodes, err := t.ts.NodesChangedSince(after, probe)
	if err == nil {
		if len(nodes) > limit {
			if after == 0 {
				nodes = nodes[1:] // drop the oldest, keep the latest page
				hasEarlier = true
			} else {
				nodes = nodes[:limit]
				hasMore = true
			}
		}
		for _, n := range nodes {
			m := compactNode(n)
			m["kind"] = n.Kind
			if s, ok := m["summary"].(string); ok {
				m["summary"] = clipLine(s, 80)
			}
			events = append(events, m)
		}
	}
	nextCursor := after
	if len(events) > 0 {
		if id, ok := events[len(events)-1]["id"].(int64); ok {
			nextCursor = id
		}
	}
	revision, _ := t.ts.MaxNodeID()
	out["totals"] = totals
	out["events"] = events
	out["routes"] = blackboardRoutes(out)
	out["blackboard"] = map[string]any{
		"revision":    revision,
		"cursor":      after,
		"has_more":    hasMore,
		"has_earlier": hasEarlier,
		"next_cursor": nextCursor,
		"window_note": "有界黑板视图：totals 是真实总数，意图/知识/事件与 routes 都是当前窗口；折叠列表已省略（计数看 folded_*_total）。更早的详情用 graph_overview(detail=true) 或 list_facts/list_worker_traces 翻页。",
	}
	return out
}

// blackboardRoutes groups the visible intents by contract.route (falling back to
// risk_area) so the planner can see which chains are open/running/done without
// reading every intent. The summaries are intentionally bounded.
func blackboardRoutes(overview map[string]any) []map[string]any {
	type routeGroup struct {
		route     string
		intents   int
		open      int
		running   int
		done      int
		summaries []map[string]any
	}
	groups := map[string]*routeGroup{}
	order := []string{}
	add := func(items []map[string]any) {
		for _, it := range items {
			route := ""
			if c, ok := it["contract"].(map[string]any); ok {
				route, _ = c["route"].(string)
			}
			if strings.TrimSpace(route) == "" {
				route, _ = it["risk_area"].(string)
			}
			if strings.TrimSpace(route) == "" {
				route = "unrouted"
			}
			route = strings.TrimSpace(route)
			g := groups[route]
			if g == nil {
				g = &routeGroup{route: route}
				groups[route] = g
				order = append(order, route)
			}
			g.intents++
			switch it["state"] {
			case "open":
				g.open++
			case "running":
				g.running++
			case "paused":
				g.open++
			default:
				g.done++
			}
			if len(g.summaries) < 3 {
				summary, _ := it["summary"].(string)
				g.summaries = append(g.summaries, map[string]any{
					"id": it["id"], "state": it["state"], "summary": clipLine(summary, 80),
				})
			}
		}
	}
	for _, key := range []string{"open_intents", "running_intents", "paused_intents", "recent_done_intents"} {
		items, _ := overview[key].([]map[string]any)
		add(items)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := groups[order[i]], groups[order[j]]
		if a.intents != b.intents {
			return a.intents > b.intents
		}
		return a.route < b.route
	})
	out := make([]map[string]any, 0, len(order))
	for _, route := range order {
		if len(out) >= 15 {
			break
		}
		g := groups[route]
		out = append(out, map[string]any{
			"route": g.route, "intents": g.intents,
			"open": g.open, "running": g.running, "done": g.done,
			"recent": g.summaries,
		})
	}
	return out
}

// graphHealth 是图体检的轻量摘要：每 30 秒最多跑一次 AuditGraph（8 类检查），
// 把问题计数与最严重的几条带进态势。没有它，依赖环/饥饿/死依赖只在启动日志里。
type graphHealthEntry struct {
	at   time.Time
	data map[string]any
}

var graphHealthCache sync.Map // exploration id → graphHealthEntry

func (t *ToolSet) graphHealth() map[string]any {
	if t.ts == nil {
		return nil
	}
	key := t.ts.ID()
	if v, ok := graphHealthCache.Load(key); ok {
		if e, ok := v.(graphHealthEntry); ok && time.Since(e.at) < 30*time.Second {
			return e.data
		}
	}
	rep, err := t.ts.AuditGraph()
	if err != nil || rep == nil || len(rep.Counts) == 0 {
		return nil
	}
	data := map[string]any{"issues": len(rep.Issues), "counts": rep.Counts}
	if len(rep.Issues) > 0 {
		n := len(rep.Issues)
		if n > 3 {
			n = 3
		}
		list := make([]map[string]any, 0, n)
		for _, is := range rep.Issues[:n] {
			list = append(list, map[string]any{"code": is.Code, "severity": is.Severity, "detail": clipLine(is.Detail, 160)})
		}
		data["top"] = list
	}
	graphHealthCache.Store(key, graphHealthEntry{at: time.Now(), data: data})
	return data
}

func (t *ToolSet) graphOverviewDataInternal(includeBreadth bool) map[string]any {
	out := map[string]any{}
	// goals summary folded in so the planner needn't call list_goals each round.
	goals, _ := t.ts.ListByKind(db.KindGoal, 100)
	// 节点一律只用**全局 id** 标识。此前态势里还带一个任务内序号 seq，模型在叙事里
	// 用它指代"第几个"、在工具参数里却必须用 id——两套编号同时出现，模型混用时就会
	// 拿着 seq 去调工具，找错节点（VPS 实测）。序号对机器寻址没有任何价值，直接不给。
	gsum := make([]map[string]any, 0, len(goals))
	for _, g := range goals {
		var p map[string]any
		_ = json.Unmarshal(g.Payload, &p)
		gsum = append(gsum, map[string]any{"id": g.ID, "state": g.State, "text": p["text"]})
	}
	out["goals"] = gsum
	// hints: 人类/主 agent 通过 add_hint 挂上图的战略提示；folded in so the
	// planner reads them every round when generating intents (否则只写不读)。
	// 只取 active：consumed 是已采纳的历史，再放进态势会让 planner 据旧提示重复派意图。
	// 先按状态过滤再截窗口，避免 consumed 越积越多把更老的 active 挤出 50 条窗口。
	hints, _ := t.ts.ListByKindState(db.KindHint, db.StateHintActive, db.MaxActiveHints)
	hsum := make([]map[string]any, 0, len(hints))
	for _, h := range hints {
		var p map[string]any
		_ = json.Unmarshal(h.Payload, &p)
		hint := map[string]any{"id": h.ID, "state": h.State, "text": p["text"]}
		// 覆盖缺口一键补测会带上锚定资产：planner 需要知道这条提示针对哪台资产，
		// 否则只能从文字里猜（list_assets 可按 id 取详情）。
		if p["asset_ids"] != nil {
			hint["asset_ids"] = p["asset_ids"]
		}
		if findingTrafficBindingEnabled() && p["traffic_refs"] != nil {
			hint["traffic_refs"] = p["traffic_refs"]
		}
		hsum = append(hsum, hint)
	}
	out["hints"] = hsum
	// 精确的待采纳数（不受窗口截断影响）：planner 据此判断"是否还有提示没处理"。
	if n, err := t.ts.CountByKindState(db.KindHint, db.StateHintActive); err == nil {
		out["pending_hints"] = n
	}
	// P23 公司级知识：同公司其它任务沉淀的已确认结论（只读背景）。**刻意不与本任务图混同**：
	// "别的任务测过 X"不等于"本任务测过 X"，所以单独一段 + 一句说明，不参与本任务去重。
	if ks := t.companyKnowledgeRows(); len(ks) > 0 {
		lines := make([]string, 0, len(ks))
		for _, k := range ks {
			lines = append(lines, renderKnowledgeLine(k))
		}
		out["company_knowledge"] = lines
		out["company_knowledge_note"] = "以上来自同公司【其它任务】的沉淀（只读）：当先验用，需要时自己复核；" +
			"它们不代表本任务已测过——本任务的进度仍以本任务图为准。"
	}
	// 指纹打法库（全局）：本任务范围内出现的技术栈/favicon 命中过哪些"同类目标打穿"的打法。
	// 与上面公司知识的分工：那个按客户、这个是跨客户的**指纹匹配**；同样只作先验——
	// 历史命中不等于目标有洞，动手前仍要确认指纹一致并实际触发。
	if t.as != nil && t.taskID > 0 {
		if matches, err := t.as.MatchPlaybooksForTask(t.taskID, 3); err == nil && len(matches) > 0 {
			lines := make([]string, 0, len(matches))
			for _, m := range matches {
				lines = append(lines, renderPlaybookLine(m))
			}
			out["matching_playbooks"] = lines
			out["matching_playbooks_note"] = "以上是历史【已打穿】目标沉淀的同类打法（全局指纹库，只读）。" +
				"先确认目标指纹与卡片一致，再用 lookup_playbooks 取完整步骤快速验证；" +
				"历史命中不代表目标一定有洞，仍要实际触发。用完用 record_playbook_feedback 回报 worked/failed。"
		}
	}
	// CVE 库：按本任务范围内资产的指纹/产品/版本匹配。与打法库一样是**线索**，
	// 不是漏洞证据；有脚本时可用 cve_exploit 实际触发（仍走 guard）。
	if t.as != nil && t.taskID > 0 {
		if matches, err := t.as.MatchCVEsForTask(t.taskID, 3); err == nil && len(matches) > 0 {
			lines := make([]string, 0, len(matches))
			for _, m := range matches {
				lines = append(lines, renderCVEMatchLine(m))
			}
			out["matching_cves"] = lines
			out["matching_cves_note"] = "以上是按本任务范围内资产指纹/产品/版本匹配到的 CVE 库条目（全局只读线索）。" +
				"**CVE/版本匹配不是证据**：具体到某台资产时用 lookup_cves(asset_id=...) 取详情与 probe_guide；" +
				"有脚本且目标明确时才用 cve_exploit 实际触发。cve_exploit 只返回输出/execution_id，不会自动产生 finding/foothold。"
		}
	}
	// 广度账本：具体资产（root_domain 除外）的终态覆盖与债务。完整矩阵走
	// coverage_matrix 按需取；这里只给规模 + 最多 8 个缺口，避免撑爆上下文。
	// 覆盖度关闭时 coverage_matrix 已被摘除、也不注入 breadth——否则模型会去找不存在
	// 的工具/字段。
	if includeBreadth && !t.coverageDisabled && t.constraintOn("breadth") {
		if b := t.breadthSummary(); b != nil {
			out["breadth"] = b
		}
	}
	// 探索台账：把「资产 × 风险类」摊成状态计数 + 待处理缺口（≤12）。它是发散的
	// 第一来源，也是收官审计的依据；完整网格走 exploration_ledger 按需取。
	if t.constraintOn("ledger") {
		if rep := t.ledgerReport(); rep != nil && rep.CellsTotal > 0 {
			out["ledger"] = map[string]any{
				"counts": rep.Counts, "waived": rep.Waived,
				"gaps": rep.Gaps, "gaps_total": rep.GapsTotal,
				"note": "untested/failed 是发散来源；tested 只代表有一条结论，不代表测透",
			}
		}
	}
	// 通路台账：手上有哪些据点/凭据、推进到哪一步。深度不靠模型自觉，靠这张表。
	if t.constraintOn("ledger") {
		if rep := t.chainReport(); rep != nil && len(rep.Rows) > 0 {
			rows := make([]map[string]any, 0, 8)
			for _, r := range rep.Rows {
				if len(rows) >= 8 {
					break
				}
				rows = append(rows, map[string]any{
					"loot_id": r.LootID, "kind": r.Kind, "label": r.Label,
					"stage": r.Stage, "status": r.Status, "uses": r.Uses,
					"downstream": len(r.Downstream),
				})
			}
			out["chains"] = map[string]any{
				"counts": rep.Counts, "rows": rows,
				"note": "unused/open 是深度缺口；用过一次不算推进，看 stage 与 downstream",
			}
		}
	}
	// lineage from the exploration edges: an intent's parents (what it
	// derived_from — possibly several facts combined) and its yields (the
	// facts/findings it produced). factFrom maps a fact → the intent that
	// produced it. This is the relationship layer the flat lists lacked.
	edges, _ := t.ts.Edges(5000)
	parentsOf := map[int64][]int64{}
	yieldsOf := map[int64][]int64{}
	factFrom := map[int64]int64{}
	lootUsers := map[int64][]int64{} // 战果 → 用过它的意图（算不算「真动用」还取决于那些意图的收场状态，见 lootConsumingState）
	for _, e := range edges {
		switch e.Rel {
		case db.RelDerivedFrom, db.RelSpawns: // upstream: derived_from (fact/finding/intent→intent) or spawns (goal→intent, legacy begin→intent)
			parentsOf[e.To] = append(parentsOf[e.To], e.From)
		case db.RelYields: // intent --yields--> fact/finding
			yieldsOf[e.From] = append(yieldsOf[e.From], e.To)
			factFrom[e.To] = e.From
		case db.RelUses: // intent --uses--> 战果
			lootUsers[e.To] = append(lootUsers[e.To], e.From)
		}
	}
	// 血缘 kind：parents/yields 只有 id，planner 分不清 goal/fact/finding/loot。
	// 一次批量取 kind，输出 parent_kinds/yield_kinds（只增字段，不改旧语义）。
	lineageIDs := map[int64]bool{}
	for _, ids := range parentsOf {
		for _, id := range ids {
			lineageIDs[id] = true
		}
	}
	for _, ids := range yieldsOf {
		for _, id := range ids {
			lineageIDs[id] = true
		}
	}
	lineageKinds := map[int64]string{}
	if len(lineageIDs) > 0 {
		ids := make([]int64, 0, len(lineageIDs))
		for id := range lineageIDs {
			ids = append(ids, id)
		}
		if nodes, err := t.ts.NodesByIDs(ids); err == nil {
			for _, n := range nodes {
				lineageKinds[n.ID] = n.Kind
			}
		}
	}
	if total, eerr := t.ts.CountEdges(); eerr == nil {
		out["edges_total"] = total
		if total > len(edges) {
			out["edges_window_note"] = fmt.Sprintf("血缘边只取前 %d 条（共 %d 条）；更旧的边不在 parents/yields 里。", len(edges), total)
		}
	}
	// cold-digest §6: members folded into an active digest are shown via cold_digests
	// (below), not the flat recent_* lists. `covered` maps member id → its digest id.
	// §6 render-time revival check: a covered member that has become hot again (a new
	// intent derived from it) must reappear this round — so `hidden` folds a member out
	// only when it is covered AND still cold.
	covered, _ := t.ts.CoveredMembers()
	// Render-time hot set (ancestor of a live intent / fact under a live intent).
	// §6 revival check: a covered member that revived (now hot) must NOT stay folded
	// — hidden() only folds a member out when it is covered AND still cold. Computed
	// every round (cheap for real graph sizes); nil map degrades safely.
	var hotAtRender map[int64]bool
	if cg, _, err := loadColdGraph(t.ts); err == nil {
		hotAtRender = cg.hotSet()
	}
	hidden := func(id int64) bool { _, c := covered[id]; return c && !hotAtRender[id] }
	const openIntentsCap = 30
	fr, _ := t.ts.Frontier(openIntentsCap) // 可领的（前置已满足）；真实总数见 frontier_open
	all, _ := t.ts.ListByKind(db.KindIntent, 300)
	if total, ierr := t.ts.CountByKind(db.KindIntent); ierr == nil {
		out["intents_total"] = total
		if total > len(all) {
			out["intents_window_note"] = fmt.Sprintf("意图列表只取最新 %d 条（共 %d 条）；更旧的用 list_worker_traces 翻页。", len(all), total)
		}
	}
	// 展示用「全部 open」而不是 frontier：被前置挡住的意图**也是 planner 要看见的**
	// ——它得知道"我派了、但它在等谁"，否则只会反复重派或以为系统没跑。
	var openIntents []*db.Node
	for _, n := range all {
		if n.State == "open" {
			openIntents = append(openIntents, n)
		}
	}
	sort.SliceStable(openIntents, func(i, j int) bool {
		if openIntents[i].Priority != openIntents[j].Priority {
			return openIntents[i].Priority > openIntents[j].Priority
		}
		return openIntents[i].ID < openIntents[j].ID
	})
	if len(openIntents) > openIntentsCap {
		openIntents = openIntents[:openIntentsCap]
	}
	out["open_intents"] = compactIntents(openIntents, parentsOf, yieldsOf, lineageKinds)
	var running, recentDone []*db.Node
	for _, n := range all {
		switch n.State {
		case "running":
			running = append(running, n)
		case "done", "negative", "out-of-scope", "blocked", "exhausted", "stopped", "no_output":
			if hidden(n.ID) {
				continue // in a cold_digest and still cold — shown via cold_digests (§6.2)
			}
			recentDone = append(recentDone, n) // 最新在前（all 按 id 降序）；折叠的已剔除，输出时截最新 N
		}
	}
	out["running_intents"] = compactIntents(running, parentsOf, yieldsOf, lineageKinds)
	// paused 既不是"待领"也不是"已结束"：此前三个桶都不收它，planner 在态势里完全看不到，
	// 容易把一条被人为暂缓的方向当成从未派过而重派。
	var pausedIntents []*db.Node
	for _, n := range all {
		if n.State == "paused" && !hidden(n.ID) {
			pausedIntents = append(pausedIntents, n)
		}
	}
	if len(pausedIntents) > 0 {
		out["paused_intents"] = compactIntents(pausedIntents, parentsOf, yieldsOf, lineageKinds)
	}
	// 串行链：前置未满足的意图**不会**被 worker 领走（Frontier/ClaimIntent 都有闸门）。
	// 必须把这件事摆到规划层面前——否则它会以为"我派了、怎么没人跑"，或者反复重派。
	if deps, derr := t.ts.IntentDependencies(); derr == nil && len(deps) > 0 {
		stateByID := make(map[int64]string, len(all))
		summaryByID := make(map[int64]string, len(all))
		for _, n := range all {
			stateByID[n.ID] = n.State
			var p map[string]any
			if json.Unmarshal(n.Payload, &p) == nil {
				summaryByID[n.ID] = asText(p["summary"])
			}
		}
		blocked := 0
		if items, ok := out["open_intents"].([]map[string]any); ok {
			for _, it := range items {
				id, _ := it["id"].(int64)
				depIDs := deps[id]
				if len(depIDs) == 0 {
					continue
				}
				blocked++
				by := make([]map[string]any, 0, len(depIDs))
				for _, d := range depIDs {
					item := map[string]any{
						"id": d, "state": stateByID[d], "summary": clipLine(summaryByID[d], 60),
					}
					switch stateByID[d] {
					case "out-of-scope", "blocked", "exhausted", "stopped", "no_output", "deleted":
						item["dead"] = true // 前置永远不会满足：下游不能等，要重派/拆链/丢弃
					}
					by = append(by, item)
				}
				it["blocked_by"] = by
			}
		}
		if total, cerr := t.ts.CountBlockedOpenIntents(); cerr == nil {
			out["blocked_intents_total"] = total // 全量，不是 open_intents 窗口内的计数
		} else if blocked > 0 {
			out["blocked_intents_total"] = blocked
		}
	}
	// 永远等不到的排队意图：前置已全部收场、且至少一个不会有结论。它们仍是 open，
	// unblock_intent 有效；单独列出来是因为 open_intents 窗口（30 条）外的那些只能靠计数猜。
	if stranded, serr := t.ts.StrandedDependents(); serr == nil && len(stranded) > 0 {
		out["stranded_dependents_total"] = len(stranded)
		ids := make([]int64, 0, len(stranded))
		deadIDs := make([]int64, 0, len(stranded))
		for id, ds := range stranded {
			ids = append(ids, id)
			deadIDs = append(deadIDs, ds...)
		}
		deadState := map[int64]string{}
		if nodes, nerr := t.ts.NodesByIDs(deadIDs); nerr == nil {
			for _, n := range nodes {
				deadState[n.ID] = n.State
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		items := make([]map[string]any, 0, 8)
		for _, id := range ids {
			if len(items) >= 8 {
				break
			}
			var p map[string]any
			if n, nerr := t.ts.GetNode(id); nerr == nil && n != nil {
				_ = json.Unmarshal(n.Payload, &p)
			}
			dead := make([]map[string]any, 0, len(stranded[id]))
			for _, d := range stranded[id] {
				dead = append(dead, map[string]any{"id": d, "state": deadState[d]})
			}
			items = append(items, map[string]any{
				"id": id, "summary": clipLine(asText(p["summary"]), 80), "dead_deps": dead,
			})
		}
		out["stranded_dependents"] = items
	}
	// 队列深度与容量：planner 判断"还能不能再派"唯一该看的数。
	// frontier_open 仍保留（含被前置挡住的），但判定请用 claimable_intents。
	if claimable, err := t.ts.CountClaimableIntents(); err == nil {
		out["claimable_intents"] = claimable
	}
	if t.workerCap > 0 {
		// idle_slots 直接给出「还有几个 worker 闲着」。此前只给 workers/running 两个数，
		// 要模型自己做减法：结果是"有 1 条在跑"就当成"已经有人在干活"，空转的槽位
		// 没人认领（任务 14 实测：4 个 worker 只跑 1 条，planner 连续几轮不派活）。
		idle := t.workerCap - len(running)
		if idle < 0 {
			idle = 0
		}
		q := map[string]any{
			"workers": t.workerCap, "soft_limit": intentQueueFactor * t.workerCap,
			"running": len(running), "idle_slots": idle,
		}
		if v, ok := out["claimable_intents"].(int); ok {
			q["claimable"] = v
			if v >= intentQueueFactor*t.workerCap {
				q["saturated"] = true
			}
		}
		if v, ok := out["blocked_intents_total"].(int); ok {
			q["blocked"] = v
		}
		out["intent_queue"] = q
	}
	// done_intents_total：已结束意图（done/blocked/exhausted）总数，与 recent_done_intents
	// 平行命名——后者只是它的最新窗口截断视图。两键并排即自描述："看到的是 N/总数"，
	// 让 planner 去重时别把"没显示"当成"没派过"，无需在提示词里另行解释。
	if dt, err := t.ts.CountFinishedIntents(); err == nil {
		out["done_intents_total"] = dt
	}
	// frontier_open：开放意图真实总数（open_intents 只是其中优先级最高的前 N 条截断视图）。
	if fo, err := t.ts.CountOpenIntents(); err == nil {
		out["frontier_open"] = fo
	} else {
		out["frontier_open"] = len(fr)
	}
	// findings (confirmed vulns) and facts (worker exploration results) are
	// now distinct node kinds. recent_facts surfaces fact summaries (esp.
	// negative results) so the planner sees them in one call; full content
	// via node_detail(id).
	//
	// 计数走 COUNT 而不是 len(列表)：ListByKind 有行数上限，任务超过上限后 len() 报的是
	// 截断窗口的大小而不是总数——findings_total 是目标判定的依据，不能骗人。findings 列表
	// 只取要展示的那一窗（findingListCap），也不必为此多拉行。
	const findingListCap = 10
	// 只数仍然有效的漏洞（撤回/误报/重复/排除已关闭）：findings_total 是目标判定的
	// 依据，把被推翻的结论算进去会让 planner 误以为目标已达成。
	vulnNodes, _ := t.ts.ListActiveFindings(findingListCap) // newest first
	factNodes, _ := t.ts.ListByKind(db.KindFact, 1000)      // newest first；recent_facts 的候选窗
	if total, err := t.ts.CountActiveFindings(); err == nil {
		out["findings_total"] = total // 有效确认漏洞总数（目标判定看它）；明细见 finding_list（最新一窗）
	} else {
		out["findings_total"] = len(vulnNodes)
	}
	factsTotal := len(factNodes)
	if total, err := t.ts.CountByKind(db.KindFact); err == nil {
		factsTotal = total
	}
	out["facts"] = factsTotal // 探索事实/结论数（含否定结论）——真 COUNT，不是窗口长度
	// findings 是任务里最高价值的产物 → 概览带最新一窗（≤10 条，vulnNodes 已按 id 降序即最新在前），
	// 让 planner 每轮判目标时一眼看到最近确认的漏洞；全量/更早的用 list_findings 取。
	// 每条只留 {id, summary, from_intent?}：from_intent 是产生本漏洞的意图。
	// evidence/assets/vulnclass/severity/state 等仍可用 list_findings / node_detail(id) 取。
	findingList := make([]map[string]any, 0, findingListCap)
	for _, n := range vulnNodes {
		if len(findingList) >= findingListCap {
			break
		}
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		m := map[string]any{"id": n.ID, "summary": fp["summary"]}
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // 本漏洞由哪个意图产生
		}
		findingList = append(findingList, m)
	}
	out["finding_list"] = findingList
	// recent_facts：非折叠事实里最新的一窗（≤N，factNodes 按 id 降序即最新在前）。已折进
	// digest 且仍冷的（hidden）走 cold_digests，不在此重复。每条 {id, summary, from_intent?,
	// confidence?}；evidence 等详情用 node_detail(id)。更早的用 list_facts 翻。
	recentFacts := make([]map[string]any, 0, recentFactsCap)
	for _, n := range factNodes {
		if len(recentFacts) >= recentFactsCap {
			break
		}
		if hidden(n.ID) {
			continue // 已折进 digest 且仍冷 —— 见 cold_digests
		}
		m := compactNode(n)
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // 本事实由哪个意图产生
		}
		// confidence 带进概览：让规划者一眼看出哪条结论只是 inferred（尤其否定结论
		// 别当铁案）；evidence 较长，留给 node_detail(id)。
		var fp map[string]any
		if json.Unmarshal(n.Payload, &fp) == nil {
			if c, ok := fp["confidence"].(string); ok && c != "" {
				m["confidence"] = c
			}
			if pol, ok := fp["polarity"].(string); ok && pol != "" {
				m["polarity"] = pol // positive/negative/inconclusive：否定收尾不催办
			}
			if en, ok := fp["enables"].([]any); ok && len(en) > 0 {
				m["enables"] = en // 这条事实给了什么能力
			}
			if d, ok := fp["detail"].(string); ok {
				m["detail_len"] = len([]rune(d))
			}
			if e, ok := fp["evidence"].(string); ok && strings.TrimSpace(e) != "" {
				m["has_evidence"] = true
			}
		}
		recentFacts = append(recentFacts, m)
	}
	out["recent_facts"] = recentFacts
	// 续翻游标：recent_facts 只是最新一窗（还会跳过已折叠的），planner 此前看不出
	// "这是全部还是窗口"，也不知道该从哪里接着翻——只能自己猜 id。直接给下一页的
	// before，够不着时不给这个键（空键会让模型以为"有更多"却翻不动）。
	if len(recentFacts) > 0 && factsTotal > len(recentFacts) {
		out["recent_facts_next_before"] = recentFacts[len(recentFacts)-1]["id"]
		out["recent_facts_note"] = "recent_facts 是最新一窗（已折叠进 digest 的不在此重复，见 cold_digests）。" +
			"更旧的 facts 用 list_facts 翻页：传 before=recent_facts_next_before；全文用 node_detail(id)。"
	}
	// 线索账本：leads 存在 fact payload 里，id 由事实 id + 下标生成。
	// converted 由意图 payload 的 converted_lead_ids 决定；dismissed 由事实 payload 的
	// dismissed_leads 决定——都不靠「来源 fact 有没有子意图」猜。
	convertedLeads := map[string]bool{}
	for _, n := range all {
		var p struct {
			Converted []string `json:"converted_lead_ids"`
		}
		if json.Unmarshal(n.Payload, &p) == nil {
			for _, id := range p.Converted {
				convertedLeads[strings.ToUpper(strings.TrimSpace(id))] = true
			}
		}
	}
	childIntents := map[int64][]int64{}
	for iid, ps := range parentsOf {
		for _, pid := range ps {
			childIntents[pid] = append(childIntents[pid], iid)
		}
	}
	leadsTotal, leadsOpen, leadsConverted, leadsDismissed := 0, 0, 0, 0
	recentLeads := make([]map[string]any, 0, 20)
	unconsumed := make([]map[string]any, 0, 5)
	capCounts := map[string]int{}
	for _, n := range factNodes {
		if n.State == "origin" || n.State == "dismissed" {
			continue
		}
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		if en, ok := fp["enables"].([]any); ok {
			for _, v := range en {
				if sv, ok := v.(string); ok {
					capCounts[sv]++
				}
			}
		}
		leads, _ := fp["leads"].([]any)
		dismissed := map[string]bool{}
		if arr, ok := fp["dismissed_leads"].([]any); ok {
			for _, v := range arr {
				if sv, ok := v.(string); ok {
					dismissed[strings.ToUpper(strings.TrimSpace(sv))] = true
				}
			}
		}
		for i, raw := range leads {
			lm, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id := leadID(n.ID, i)
			key := strings.ToUpper(id)
			leadsTotal++
			switch {
			case convertedLeads[key]:
				leadsConverted++
				continue
			case dismissed[key]:
				leadsDismissed++
				continue
			}
			leadsOpen++
			if len(recentLeads) < 20 {
				item := map[string]any{"id": id, "text": asText(lm["text"]), "from_fact": n.ID}
				if from := factFrom[n.ID]; from > 0 {
					item["from_intent"] = from
				}
				if ids, ok := lm["asset_ids"].([]any); ok && len(ids) > 0 {
					item["asset_ids"] = ids
				}
				item["age_min"] = int(time.Since(n.CreatedAt).Minutes())
				recentLeads = append(recentLeads, item)
			}
		}
		// 未被接手、且不是否定收尾的事实：给 planner 一个「这些线索还没人接」的窗口。
		pol, _ := fp["polarity"].(string)
		if pol != factPolarityNeg && len(childIntents[n.ID]) == 0 && !hidden(n.ID) && len(unconsumed) < 5 {
			item := map[string]any{
				"id": n.ID, "summary": asText(fp["summary"]), "polarity": pol,
				"confidence": asText(fp["confidence"]), "age_min": int(time.Since(n.CreatedAt).Minutes()),
			}
			if d, ok := fp["detail"].(string); ok && strings.TrimSpace(d) != "" {
				item["detail_excerpt"] = FenceUntrusted("事实详情", clipLine(d, 200))
			}
			unconsumed = append(unconsumed, item)
		}
	}
	if len(recentLeads) > 0 {
		out["recent_leads"] = recentLeads
	}
	out["leads_total"] = leadsTotal
	out["leads_open"] = leadsOpen
	out["leads_converted"] = leadsConverted
	out["leads_dismissed"] = leadsDismissed
	if len(unconsumed) > 0 {
		out["unconsumed_facts"] = unconsumed
	}
	if len(capCounts) > 0 {
		out["capabilities"] = capCounts
	}
	// ruled_out：已否定的方向（否定事实 + ruled_out 发现），一行式，供 planner 避免重派。
	ruled := make([]map[string]any, 0, 8)
	for _, n := range factNodes {
		if len(ruled) >= 6 {
			break
		}
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		if pol, _ := fp["polarity"].(string); pol == factPolarityNeg {
			ruled = append(ruled, map[string]any{"id": n.ID, "kind": "fact", "summary": clipLine(asText(fp["summary"]), 100)})
		}
	}
	if fs, err := t.ts.ListByKind(db.KindFinding, 200); err == nil {
		for _, n := range fs {
			if len(ruled) >= 8 {
				break
			}
			if n.State != "ruled_out" {
				continue
			}
			var fp map[string]any
			_ = json.Unmarshal(n.Payload, &fp)
			ruled = append(ruled, map[string]any{"id": n.ID, "kind": "finding", "summary": clipLine(asText(fp["summary"]), 100)})
		}
	}
	if len(ruled) > 0 {
		out["ruled_out"] = ruled
	}
	// folded_facts：折叠进冷摘要的事实索引（id + 截断摘要 + state + digest_id）。
	// 事实没有 folded_intents 那样的索引，大图里折叠后只剩 digest body。
	var foldedFacts []map[string]any
	var foldedFactIDs []int64
	for _, n := range factNodes {
		if hidden(n.ID) {
			foldedFactIDs = append(foldedFactIDs, n.ID)
		}
	}
	digestOf := map[int64]int64{}
	if len(foldedFactIDs) > 0 {
		if m, err := t.ts.FoldedDigestByNode(foldedFactIDs); err == nil {
			digestOf = m
		}
	}
	for _, id := range foldedFactIDs {
		if len(foldedFacts) >= 50 {
			break
		}
		n, err := t.ts.GetNode(id)
		if err != nil || n == nil {
			continue
		}
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		item := map[string]any{"id": id, "summary": clipLine(asText(fp["summary"]), 60), "state": n.State}
		if d := digestOf[id]; d > 0 {
			item["digest_id"] = d
		}
		foldedFacts = append(foldedFacts, item)
	}
	if len(foldedFacts) > 0 {
		out["folded_facts"] = foldedFacts
		if total, err := t.ts.CountCoveredFacts(); err == nil {
			out["folded_facts_total"] = total
		}
	}

	// folded_intents：折叠进冷摘要、且仍然冷的已结束意图索引（id + 截断摘要 + 状态）。
	//
	// 为什么需要：recent_done_intents 会剔除折叠成员，而 cold_digests 只展开前 15 个摘要
	// 的正文、其余只给裸 id。实测任务 6：82 条已结束意图里 31 条只剩一个 digest id——
	// planner 要判断"这个方向是不是试过"，得逐个 expand_digest，27 个摘要就是 27 次往返。
	// 这段索引让它一眼扫到方向名与终态（done / no_output / blocked 的区别决定要不要重派）。
	//
	// 体积控制：摘要截 80 字、硬上限 100 条；超出只给计数，更旧的用 list_worker_traces 翻页。
	folded := make([]map[string]any, 0, 32)
	for _, n := range all {
		switch n.State {
		case "done", "negative", "out-of-scope", "blocked", "exhausted", "stopped", "no_output":
		default:
			continue
		}
		if !hidden(n.ID) {
			continue // 未折叠：已由 recent_done_intents 覆盖
		}
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		summary, _ := p["summary"].(string)
		folded = append(folded, map[string]any{
			// 字段名用 summary 而不是 s：单字母键在模型眼里太容易被当成序号/缩写，
			// 而这里存的是摘要文本（曾经叫 "s"，和 seq 混在一起读更容易出错）。
			"id": n.ID, "summary": clipLine(summary, foldedIntentSummaryMax), "state": n.State,
		})
	}
	if total := len(folded); total > 0 {
		if total > foldedIntentsCap {
			folded = folded[:foldedIntentsCap]
		}
		out["folded_intents"] = folded
		if realTotal, cerr := t.ts.CountCoveredIntents(); cerr == nil {
			out["folded_intents_total"] = realTotal // SQL 真计数，不是窗口长度
		} else {
			out["folded_intents_total"] = total
		}
		out["folded_intents_note"] = "折叠区已结束意图索引（摘要截断）：这些方向已经试过，state 决定要不要换打法重派。" +
			"更旧的用 list_worker_traces 翻页；要看某个摘要的完整叙述用 expand_digest(digest_id)。"
	}

	// recent_done_intents：非折叠的已结束意图里最新的一窗（≤N，recentDone 已按 id 降序）。
	// 更早的看 done_intents_total 计数 + node_detail(id)。
	// 窗口大小见包级常量 recentDoneCap。
	if len(recentDone) > recentDoneCap {
		recentDone = recentDone[:recentDoneCap]
	}
	out["recent_done_intents"] = compactIntents(recentDone, parentsOf, yieldsOf, lineageKinds)
	// 战果是否算「已被动用」，取决于用过它的那些意图是怎么收场的——只在 edges 里看到一条
	// uses 是不够的（见 lootConsumingState）。intentState 复用上面已加载的 all。
	intentState := make(map[int64]string, len(all))
	for _, n := range all {
		intentState[n.ID] = n.State
	}
	usedLoot := map[int64]bool{}
	for lootID, users := range lootUsers {
		for _, uid := range users {
			if db.LootConsumingIntentState(intentState[uid]) {
				usedLoot[lootID] = true
				break
			}
		}
	}
	// ATT&CK 技术覆盖：本任务各技术出现在几条意图里。对 planner 的用处是「哪些面已经打过、
	// 哪些技术还没试过」；对交付物的用处是报告里那张技术表的同一份数据。
	// 软删除的意图不计（用户主动拿掉了），其余都计——被 blocked 的方向也算「试过」。
	if tally := db.TechniqueTally(all); len(tally) > 0 {
		out["technique_tally"] = tally
	}
	// 战果（据点 / 凭据）：利用成功之后的产物。计数走 COUNT，理由同 findings_total——列表有
	// 行数上限，len() 报的是窗口大小而不是总数，而「手上有几个据点」正是 planner 判断能不能
	// 继续往深打的依据，一个被封顶的数字会静默低估战果。
	//
	// compactLoot 用的是**字段白名单**：凭据的 secret 因此结构性地进不了概览——概览会被折进
	// 每一轮 planner 与 worker 的提示词、还可能进日志，那里不该出现任务里最敏感的那一行。
	// 要看凭据原文，用 node_detail(id)。
	//
	// **outstanding**：在手、且还没有任何意图以 uses 指向它的战果——「拿到了、但还没动用的能力」。
	// 这正是 planner 自己看不出的一件事：它看得到战果**是什么**，看不出哪一条**已经被别的意图
	// 接手了**。所以这一层给的是一条信息（历史事实），不是一句劝告。
	//
	// 计数按**全量**算（战果稀疏，一个任务通常就几条），只有展示窗口才截断——否则 outstanding
	// 计数会和当年 findings_total 一样，在条目超过窗口后开始骗人。
	const lootListCap = 8
	allFootholds, _ := t.ts.ListByKind(db.KindFoothold, 1000) // newest first
	allCreds, _ := t.ts.ListByKind(db.KindCredential, 1000)
	if total, err := t.ts.CountByKind(db.KindFoothold); err == nil {
		out["footholds_total"] = total
	} else {
		out["footholds_total"] = len(allFootholds)
	}
	if total, err := t.ts.CountByKind(db.KindCredential); err == nil {
		out["credentials_total"] = total
	} else {
		out["credentials_total"] = len(allCreds)
	}
	outstanding := 0
	for _, n := range allFootholds {
		if lootOutstanding(n, usedLoot) {
			outstanding++
		}
	}
	for _, n := range allCreds {
		if lootOutstanding(n, usedLoot) {
			outstanding++
		}
	}
	out["outstanding_loot_total"] = outstanding
	// AD 已控对象：路径重建的起点依据。报告与管理页都按 owned 选起点，概览让规划层
	// 也能看到——"我们拿下了域里的哪几台"是决定下一步往哪打的关键输入。
	if ads, err := t.ts.ListByKind(db.KindADObject, 500); err == nil && len(ads) > 0 {
		owned := 0
		names := make([]string, 0, 5)
		adNames := make(map[int64]string, len(ads))
		for _, n := range ads {
			var p map[string]any
			if json.Unmarshal(n.Payload, &p) != nil {
				continue
			}
			name := payloadString(p, "name")
			if name != "" {
				adNames[n.ID] = name
			}
			if o, _ := p["owned"].(bool); !o {
				continue
			}
			owned++
			if len(names) < 5 && name != "" {
				names = append(names, name)
			}
		}
		if owned > 0 {
			out["ad_owned_total"] = owned
			out["ad_owned"] = names
		}
		// 「可达」：哪条战果通到域里的哪台机器（reaches 边）。与 owned 分开——
		// 凭据能登某台机器不等于那台机器已经在手，但规划层要同时看到这两件事：
		// 一条是"我能进去"，一条是"我已经在里面"。
		kindOfLoot := make(map[int64]string, len(allFootholds)+len(allCreds))
		for _, n := range allFootholds {
			kindOfLoot[n.ID] = db.KindFoothold
		}
		for _, n := range allCreds {
			kindOfLoot[n.ID] = db.KindCredential
		}
		var reach []map[string]any
		seen := map[int64]bool{}
		for _, e := range edges {
			if e.Rel != db.RelReaches {
				continue
			}
			kind := kindOfLoot[e.From]
			name := adNames[e.To]
			if kind == "" || name == "" || seen[e.To] {
				continue
			}
			seen[e.To] = true
			reach = append(reach, map[string]any{
				"object": name, "via": fmt.Sprintf("%s-%d", kind, e.From), "ad_object_id": e.To,
			})
			if len(reach) >= 10 {
				break
			}
		}
		if len(reach) > 0 {
			out["ad_reachable"] = reach
		}
	}
	// 隧道：跨 worker 共享的能力——A 开的隧道 B 也能用，看不见就没人用得上。
	// 从**在跑的执行**里挑出带登记标记的（隧道本身就是一条 ssh -D 长任务）。
	if t.execService != nil && t.taskID > 0 {
		if rows := t.execService.ActiveForTask(t.taskIDStr()); len(rows) > 0 {
			var tunnels []map[string]any
			for _, r := range rows {
				if !strings.HasPrefix(r.Description, tunnelDescPrefix) {
					continue
				}
				tunnels = append(tunnels, map[string]any{
					"execution_id": r.ID, "description": r.Description, "status": r.Status,
				})
			}
			if len(tunnels) > 0 {
				out["tunnels"] = tunnels
			}
		}
	}
	// 战果不会被折进 digest（coldgraph.foldableKind 只认 intent/fact），所以这里不需要
	// 像 recent_facts 那样给 hidden() 留出口。
	footholdWindow, credWindow := allFootholds, allCreds
	if len(footholdWindow) > lootListCap {
		footholdWindow = footholdWindow[:lootListCap]
	}
	if len(credWindow) > lootListCap {
		credWindow = credWindow[:lootListCap]
	}
	// 白名单带上结构化连法：规划层要能一眼看出"这条据点能怎么用"（ssh 走哪条凭据 /
	// webshell 打哪个 URL），否则它还是只能从 summary 猜。
	out["footholds"] = compactLoot(footholdWindow, factFrom, usedLoot, "access", "privilege", "protocol", "host", "port", "url")
	out["credentials"] = compactCredentials(credWindow, factFrom, usedLoot)
	// 检测验证：这一手做完之后，防御侧有没有响。三态分别计数而不是合成一个"检出率"——
	// **把 unknown 混进分母就是把「测不到」读成「没响」**，那会给出一个比不给更危险的结论。
	const detectionListCap = 8
	allDetections, _ := t.ts.ListByKind(db.KindDetection, 1000) // newest first
	if total, err := t.ts.CountByKind(db.KindDetection); err == nil {
		out["detections_total"] = total
	} else {
		out["detections_total"] = len(allDetections)
	}
	if results := detectionResults(allDetections); len(results) > 0 {
		out["detection_results"] = results
	}
	detectionWindow := allDetections
	if len(detectionWindow) > detectionListCap {
		detectionWindow = detectionWindow[:detectionListCap]
	}
	// 复用 compactLoot：检测的状态永远不是 active，所以「outstanding」那套对它天然不生效。
	out["detections"] = compactLoot(detectionWindow, factFrom, nil, "technique")
	// 检出热点：**真的响了**的技术（triggered），连同命中的资产。被检出过的组合不要原样
	// 重复——静默模式下这是硬要求，常规模式下也能省下一次白挨的尝试。
	if triggered := triggeredDetections(allDetections); len(triggered) > 0 {
		ids := make([]int64, 0, len(triggered))
		for _, n := range triggered {
			ids = append(ids, n.ID)
		}
		assetOf, _ := t.ts.NodeAssets(ids)
		hotspots := make([]map[string]any, 0, 5)
		for _, n := range triggered {
			var p map[string]any
			_ = json.Unmarshal(n.Payload, &p)
			item := map[string]any{
				"id": n.ID, "technique": payloadString(p, "technique"),
				"summary": clipLine(payloadString(p, "summary"), 80),
			}
			if as := assetOf[n.ID]; len(as) > 0 {
				item["asset_ids"] = as
			}
			hotspots = append(hotspots, item)
			if len(hotspots) >= 5 {
				break
			}
		}
		out["detection_hotspots"] = hotspots
	}
	// cold-digest §6.1: 折叠冷区的 digest body，按最新成员时间降序取前 N；被截的更旧 digest
	// 只给裸 id（仍可 expand_digest 展开），避免冷区唯一出口被无限拉长。
	const coldDigestsCap = 15
	if cds, more := coldDigestsRecent(t.ts, coldDigestsCap); len(cds) > 0 {
		out["cold_digests"] = cds // [{id, body, member_count}] —— 直接读 body (§6.1)
		if len(more) > 0 {
			out["cold_digests_more"] = more // 被截断的更旧 digest 的 id；用 expand_digest(id) 展开
		}
	}
	// AD 域内路径（P32）：导入过 BloodHound/SharpHound 采集结果时，给一条**摘要**——
	// 有多少域内对象、几条能走到高价值目标的现成路、最省事的那条长什么样。
	// 只给摘要不给全量：路径可能几十条，全塞进每一轮提示词是纯浪费；要看全部用 ad_paths 工具。
	// 域内数据为空时这一段不出现（没导过采集结果的任务，提示词里不该多出无关内容）。
	if t.ts != nil {
		if adNodes, err := loadTopologyNodes(t.ts); err == nil && len(adNodes) > 0 {
			if adEdges, err := t.ts.Edges(20000); err == nil {
				res := db.ADPaths(adNodes, adEdges, 0)
				ad := map[string]any{
					"objects": res.Objects, "highvalue_targets": res.HighValue,
					"paths_total": len(res.Paths),
				}
				const adPathPreview = 3
				if len(res.Paths) > 0 {
					preview := res.Paths
					if len(preview) > adPathPreview {
						preview = preview[:adPathPreview]
					}
					ad["shortest_paths"] = preview
				}
				out["ad_graph"] = ad
			}
		}
	}
	// the original task (root) so the planner always has it, not just the
	// decomposed goals.
	if description, goal, err := t.ts.Root(); err == nil {
		out["task"] = map[string]any{"description": description, "goal": goal}
	}
	// Direct source tasks are a live, read-only blackboard view. Keep their
	// summaries in a separate field so their intents never enter this task's
	// frontier or get mistaken for locally claimable work.
	out["related_tasks"] = t.relatedTaskOverviews()
	// scope_surface：范围面（范围内注册域 + 同根域下还没被任何意图碰过的资产）。
	// 「目标正面打不进去就换同主体的其它资产试」这条策略靠它驱动，所以每轮都给。
	if t.constraintOn("breadth") {
		if ss := t.scopeSurface(); ss != nil {
			out["scope_surface"] = ss
		}
	}
	// coverage：粗略的资产测试覆盖度参考——范围(task_scope)内的资产里，被 fact 碰过的
	// 占比 + by_type(按类型的 总数/已测)。要看未测的具体资产由 agent 按需调 list_untested_assets 自行判断。仅任务上下文有。
	// 资产覆盖度功能关闭时(coverageDisabled)：只保留 host_count(目标主机数的感知信息)，
	// 丢弃 denominator/tested/pct/by_type/note 等覆盖度度量，避免污染上下文、也不诱导
	// 已隐藏的 add_task_scope/list_untested_assets。
	if t.as != nil && t.ts != nil && t.taskID > 0 {
		{
			m := map[string]any{}
			if !t.coverageDisabled {
				if cov, err := t.as.TaskCoverageWithSources(t.taskID); err == nil {
					m["denominator"] = cov.Denominator
					m["tested"] = cov.Tested
					m["by_type"] = cov.ByType
					m["note"] = "coverage 是事实锚点口径的粗略参考（含直接关联任务），不是终态验收；具体资产的广度缺口看 breadth。list_untested_assets 只是 fact 级 backlog，按需参考，不要拿它代替 breadth。"
					if cov.Denominator == 0 {
						m["pct"] = nil
						m["status"] = "范围未锚定"
					} else {
						m["pct"] = cov.Pct
					}
				}
			}
			if hosts, err := t.as.HostsByTaskWithSources(t.taskID); err == nil {
				// 只给主机总数，不再把 host 列表平铺进 graph_overview（大范围任务里那是每轮
				// 都重复携带的大量字符串，对规划决策价值有限）；具体主机按需 list_assets 查。
				m["host_count"] = len(hosts)
			}
			if len(m) > 0 {
				out["coverage"] = m
			}
		}
	}
	if health := t.graphHealth(); len(health) > 0 {
		out["graph_health"] = health
	}
	// 兜底：分组关闭时这些字段一律不出现在 agent 上下文（查询可能已在上面跑过，
	// 但提示里不泄漏）。worker 视图另有更严格的 drop。
	if !t.constraintOn("dispatch") {
		delete(out, "intent_queue")
	}
	if !t.constraintOn("depth") {
		for _, k := range []string{
			"recent_leads", "leads_total", "leads_open", "leads_converted", "leads_dismissed",
			"unconsumed_facts", "capabilities",
		} {
			delete(out, k)
		}
	}
	if !t.constraintOn("breadth") {
		delete(out, "breadth")
		delete(out, "scope_surface")
	}
	if !t.constraintOn("ledger") {
		delete(out, "ledger")
		delete(out, "chains")
	}
	return out
}

func inheritedMap(m map[string]any, sourceTaskID int64) map[string]any {
	m["source_task_id"] = sourceTaskID
	m["inherited"] = true
	return m
}

const (
	relatedOverviewTotalTextRunes      = 48_000
	relatedOverviewMaxTextPerSource    = 8_000
	relatedOverviewMaxGoalsPerSource   = 8
	relatedOverviewMaxHintsPerSource   = 6
	relatedOverviewMaxFactsPerSource   = 12
	relatedOverviewMaxFindingsPerTask  = 6
	relatedOverviewMaxIntentsPerTask   = 8
	relatedOverviewMaxScopePerSource   = 12
	relatedOverviewMaxDigestsPerSource = 6
)

// overviewTextBudget bounds inherited prompt text while preserving a fair slice
// for every direct source. Full evidence remains available through the on-demand
// read tools, so truncation here does not discard persisted blackboard data.
type overviewTextBudget struct {
	remaining int
	truncated bool
}

func relatedOverviewBudgetForSources(sourceCount int) int {
	if sourceCount <= 0 {
		return 0
	}
	if sourceCount > db.MaxTaskSourceCount {
		sourceCount = db.MaxTaskSourceCount
	}
	perSource := relatedOverviewTotalTextRunes / sourceCount
	if perSource > relatedOverviewMaxTextPerSource {
		perSource = relatedOverviewMaxTextPerSource
	}
	return perSource
}

func (b *overviewTextBudget) take(value any, fieldLimit int) string {
	var text string
	switch value := value.(type) {
	case string:
		text = strings.TrimSpace(value)
	case nil:
		return ""
	default:
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "" {
		return ""
	}
	if b.remaining <= 0 || fieldLimit <= 0 {
		b.truncated = true
		return ""
	}
	runes := []rune(text)
	limit := fieldLimit
	if limit > b.remaining {
		limit = b.remaining
	}
	if len(runes) > limit {
		b.truncated = true
		if limit == 1 {
			text = "…"
		} else {
			text = string(runes[:limit-1]) + "…"
		}
		runes = []rune(text)
	}
	b.remaining -= len(runes)
	return text
}

func recentTerminalIntents(store *db.ExplorationStore, limit int) []*db.Node {
	if limit <= 0 {
		return []*db.Node{}
	}
	const batch = 300
	cursor := int64(0)
	out := make([]*db.Node, 0, limit)
	for len(out) < limit {
		page, more, err := store.ListByKindPage(db.KindIntent, cursor, batch)
		if err != nil || len(page) == 0 {
			break
		}
		for _, intent := range page {
			if db.IntentStateTerminal(intent.State) {
				out = append(out, intent)
			}
			if len(out) >= limit {
				break
			}
		}
		if !more {
			break
		}
		cursor = page[len(page)-1].ID
	}
	return out
}

// relatedTaskOverviews distills persistent blackboard state from direct source
// tasks. It intentionally reads each source's local store methods, never its own
// related sources, so inheritance is one level only.
func (t *ToolSet) relatedTaskOverviews() []map[string]any {
	sources, err := t.ts.DirectSourceStores()
	if err != nil {
		return []map[string]any{}
	}
	if len(sources) > db.MaxTaskSourceCount {
		sources = sources[:db.MaxTaskSourceCount]
	}
	perSourceTextBudget := relatedOverviewBudgetForSources(len(sources))
	out := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		ts := source.Store
		// §2 cross-task: render the source task's OWN folded view — fold out the
		// members it has already folded, and surface its cold_digests read-only.
		hidden := hiddenMembersFor(ts)
		budget := overviewTextBudget{remaining: perSourceTextBudget}
		item := map[string]any{
			"source_task_id": source.Task.TaskID,
			"inherited":      true,
			"task": map[string]any{
				"description": budget.take(source.Task.Description, 800),
				"goal":        budget.take(source.Task.Goal, 800),
				"status":      source.Task.Status,
			},
		}
		stats, statsErr := ts.Stats()

		edges, _ := ts.Edges(5000)
		parentsOf := map[int64][]int64{}
		yieldsOf := map[int64][]int64{}
		factFrom := map[int64]int64{}
		for _, edge := range edges {
			switch edge.Rel {
			case db.RelDerivedFrom, db.RelSpawns:
				parentsOf[edge.To] = append(parentsOf[edge.To], edge.From)
			case db.RelYields:
				yieldsOf[edge.From] = append(yieldsOf[edge.From], edge.To)
				factFrom[edge.To] = edge.From
			}
		}

		goals, _ := ts.ListByKind(db.KindGoal, relatedOverviewMaxGoalsPerSource)
		goalSummary := make([]map[string]any, 0, len(goals))
		for _, goal := range goals {
			var payload map[string]any
			_ = json.Unmarshal(goal.Payload, &payload)
			goalSummary = append(goalSummary, inheritedMap(map[string]any{
				"id": goal.ID, "state": goal.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["goals"] = goalSummary

		hints, _ := ts.ListByKindState(db.KindHint, db.StateHintActive, relatedOverviewMaxHintsPerSource)
		hintSummary := make([]map[string]any, 0, len(hints))
		for _, hint := range hints {
			var payload map[string]any
			_ = json.Unmarshal(hint.Payload, &payload)
			m := inheritedMap(map[string]any{
				"id": hint.ID, "state": hint.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID)
			if payload["asset_ids"] != nil {
				m["asset_ids"] = payload["asset_ids"]
			}
			hintSummary = append(hintSummary, m)
		}
		item["hints"] = hintSummary

		facts, _ := ts.ListByKind(db.KindFact, relatedOverviewMaxFactsPerSource)
		findings, _ := ts.ListByKind(db.KindFinding, relatedOverviewMaxFindingsPerTask)
		intentNodes, _ := ts.ListByKind(db.KindIntent, 300)
		terminalIntent := make(map[int64]bool, len(intentNodes))
		for _, intent := range intentNodes {
			terminalIntent[intent.ID] = inheritedIntentSummaryState(intent.State)
		}
		item["facts"] = len(facts)
		item["findings"] = len(findings)
		if statsErr == nil {
			item["facts"] = stats[db.KindFact]
			item["findings"] = stats[db.KindFinding]
			// hints 窗口只取 active：拿 active 总数比窗口长度，避免"有 consumed 历史"
			// 被误判成这个来源被截断。
			activeHints, _ := ts.CountByKindState(db.KindHint, db.StateHintActive)
			if stats[db.KindGoal] > len(goals) || activeHints > len(hints) ||
				stats[db.KindFact] > len(facts) || stats[db.KindFinding] > len(findings) {
				budget.truncated = true
			}
		}
		recentFindings := make([]map[string]any, 0, len(findings))
		for _, finding := range findings {
			entry := inheritedMap(compactFinding(finding), source.Task.TaskID)
			entry["summary"] = budget.take(entry["summary"], 400)
			recentFindings = append(recentFindings, entry)
		}
		item["recent_findings"] = recentFindings
		recentFacts := make([]map[string]any, 0, len(facts))
		for _, fact := range facts {
			if hidden(fact.ID) {
				continue // folded into this source's cold_digests — shown there (§2/§6.2)
			}
			m := inheritedMap(compactNode(fact), source.Task.TaskID)
			m["summary"] = budget.take(m["summary"], 400)
			if from := factFrom[fact.ID]; from > 0 && terminalIntent[from] {
				m["from_intent"] = from
			}
			var payload map[string]any
			if json.Unmarshal(fact.Payload, &payload) == nil {
				if confidence, ok := payload["confidence"].(string); ok && confidence != "" {
					m["confidence"] = confidence
				}
			}
			recentFacts = append(recentFacts, m)
		}
		item["recent_facts"] = recentFacts

		recentDoneRaw := recentTerminalIntents(ts, relatedOverviewMaxIntentsPerTask)
		recentDone := recentDoneRaw[:0] // in-place filter: drop this source's folded intents (§2)
		for _, intent := range recentDoneRaw {
			if hidden(intent.ID) {
				continue
			}
			recentDone = append(recentDone, intent)
		}
		for _, intent := range recentDone {
			intent.Inherited = true
			intent.SourceTaskID = source.Task.TaskID
		}
		intentResults := compactIntents(recentDone, parentsOf, yieldsOf)
		for i, intent := range recentDone {
			intentResults[i]["summary"] = budget.take(intentResults[i]["summary"], 400)
			acts, _, err := ts.ActivityPageForTerminalIntent(intent.ID, 0, 20)
			if err != nil {
				continue
			}
			var resultSummary, textFallback string
			for _, activity := range acts {
				switch activity.Kind {
				case "result":
					resultSummary = activity.Summary
				case "text":
					textFallback = activity.Summary
				}
			}
			if resultSummary == "" {
				resultSummary = textFallback
			}
			if resultSummary != "" {
				intentResults[i]["result_summary"] = budget.take(resultSummary, 800)
			}
		}
		item["recent_intent_results"] = intentResults
		// §2 cross-task: the source task's folded cold region, read-only, newest-member
		// first & capped like the current task's. Members (and overflow digests) are
		// resolvable via expand_digest(id)/node_detail(id), which search source tasks.
		if cds, more := coldDigestsRecent(ts, relatedOverviewMaxDigestsPerSource); len(cds) > 0 {
			for _, cd := range cds {
				cd["inherited"] = true
				cd["source_task_id"] = source.Task.TaskID
			}
			item["cold_digests"] = cds
			if len(more) > 0 {
				item["cold_digests_more"] = more // 被截断的更旧 digest 的 id；expand_digest(id) 展开
			}
		}
		if statsErr == nil {
			item["node_stats"] = stats
		}

		if t.as != nil {
			if scopeRows, err := t.as.ListTaskScope(source.Task.TaskID); err == nil && len(scopeRows) > 0 {
				scopeCount := len(scopeRows)
				if len(scopeRows) > relatedOverviewMaxScopePerSource {
					scopeRows = scopeRows[:relatedOverviewMaxScopePerSource]
					budget.truncated = true
				}
				scope := make([]map[string]any, 0, len(scopeRows))
				for _, row := range scopeRows {
					entry := map[string]any{"kind": row.Kind, "source": budget.take(row.Source, 300)}
					switch {
					case row.Domain != "":
						entry["value"] = budget.take(row.Domain, 400)
					case row.Net != "":
						entry["value"] = budget.take(row.Net, 400)
					case row.Value != "":
						entry["value"] = budget.take(row.Value, 400)
					case row.CompanyID != nil:
						entry["company_id"] = *row.CompanyID
					}
					scope = append(scope, entry)
				}
				item["asset_scope"] = scope
				item["asset_scope_count"] = scopeCount
			}
			if coverage, err := t.as.TaskCoverage(source.Task.TaskID, source.Task.ExplorationID); err == nil {
				item["asset_coverage"] = map[string]any{
					"denominator": coverage.Denominator,
					"tested":      coverage.Tested,
					"pct":         coverage.Pct,
					"by_type":     coverage.ByType,
				}
			}
		}
		if budget.truncated {
			item["summary_truncated"] = true
		}
		out = append(out, item)
	}
	return out
}

func inheritedIntentSummaryState(state string) bool { return db.IntentStateTerminal(state) }

// compactNode distills any exploration node to id + summary + state, dropping the
// big detail/evidence (fetch that on demand via node_detail).
func compactNode(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	return m
}

// detectionResults 统计检测验证的三态各有多少条。三态**分别计数、不合成"检出率"**：
// 把 unknown 混进分母就是把「测不到」读成「没响」。
// triggeredDetections 只取"确实响了"的那一档（unknown 不算——测不到 ≠ 没响，更 ≠ 响了）。
func triggeredDetections(nodes []*db.Node) []*db.Node {
	out := make([]*db.Node, 0, 4)
	for _, n := range nodes {
		if n.State == db.StateDetectionTriggered {
			out = append(out, n)
		}
	}
	return out
}

func detectionResults(nodes []*db.Node) map[string]int {
	out := map[string]int{}
	for _, n := range nodes {
		switch n.State {
		case db.StateDetectionTriggered, db.StateDetectionNotTriggered, db.StateDetectionUnknown:
			out[n.State]++
		}
	}
	return out
}

// lootOutstanding 报告一条战果是不是「在手、但还没有被真正动用过」。
// 「算不算真动用」的判定在 db.LootConsumingIntentState（agent 与 server 共用一份口径）。
// 已失效(state != active)的不算：不能用就不是机会。
func lootOutstanding(n *db.Node, used map[int64]bool) bool {
	return n.State == db.StateLootActive && !used[n.ID]
}

// compactLoot 把战果节点压成概览行：{id, state, summary, from_intent?, outstanding?, keep…}。
//
// keep 是**白名单**，不是黑名单：只有列出的 payload 字段会被带出。凭据的 secret 因此
// 不可能因为「忘了删」而漏进概览——加字段时得显式写进来，那一步是能看见的。
//
// outstanding 只在为真时出现（在手 + 未被任何意图 uses）。false 不写出来：概览是给模型读的，
// 缺省即「已接手/已失效」，多一个 false 只是噪声。
func compactLoot(nodes []*db.Node, from map[int64]int64, used map[int64]bool, keep ...string) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		var p map[string]any
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
		for _, k := range keep {
			if v, ok := p[k]; ok && v != nil && v != "" {
				m[k] = v
			}
		}
		if fromID := from[n.ID]; fromID > 0 {
			m["from_intent"] = fromID
		}
		if lootOutstanding(n, used) {
			m["outstanding"] = true
		}
		if n.Inherited {
			inheritedMap(m, n.SourceTaskID)
		}
		out = append(out, m)
	}
	return out
}

// compactCredentials 是凭据版的 compactLoot（P36）：在战果那套字段之上补三样**规划层真正需要**的
// 东西——指纹（可指认但**不含明文**）、能登哪（targets）、以及"哪条目标上验证过"。
//
// 明文照旧不进概览：概览会被折进每一轮提示词、还可能进日志。指纹让模型能在多轮之间指认
// 「就是那条 wp_user 的口令」，而不需要看到它。
func compactCredentials(nodes []*db.Node, from map[int64]int64, used map[int64]bool) []map[string]any {
	out := compactLoot(nodes, from, used, "kind", "username", "confidence")
	for i, n := range nodes {
		if i >= len(out) {
			break
		}
		v := db.ParseCredential(n)
		if fp := v.Fingerprint(); fp != "" {
			out[i]["fingerprint"] = fp
		}
		if len(v.Targets) > 0 {
			targets := make([]map[string]any, 0, len(v.Targets))
			for _, t := range v.Targets {
				m := map[string]any{"host": t.Host}
				if t.Port > 0 {
					m["port"] = t.Port
				}
				if t.Service != "" {
					m["service"] = t.Service
				}
				if t.Verified {
					m["verified"] = true
				}
				targets = append(targets, m)
			}
			out[i]["targets"] = targets
			if len(v.VerifiedTargets()) == 0 {
				// 明确说清"没验证过"：缺省不写会让人把它读成"能登"，而它只是个候选目标。
				out[i]["unverified"] = true
			}
		}
	}
	return out
}

// compactFinding is compactNode plus the vuln-specific vulnclass/severity.
func compactFinding(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	if vc, ok := p["vulnclass"]; ok && vc != nil && vc != "" {
		m["vulnclass"] = vc
	}
	if sv, ok := p["severity"]; ok && sv != nil && sv != "" {
		m["severity"] = sv
	}
	return m
}

func (t *ToolSet) listFindings() actool.CoreTool {
	return t.readExpTool("list_findings", "列本任务及直接关联任务的【确认漏洞】(紧凑：id+task_id+intent_id+vulnclass+severity+摘要+状态)。关联任务条目带 source_task_id/inherited=true 且只读。这里只含漏洞；普通探索事实用 list_facts，详情用 node_detail(id)。",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			f, _ := t.ts.ListByKindWithSources(db.KindFinding, 500)
			// 已撤回/误报/重复/排除的漏洞不再作为“确认漏洞”出现；墓碑仍可 node_detail 查看。
			ids := make([]int64, 0, len(f))
			for _, n := range f {
				ids = append(ids, n.ID)
			}
			if closed, cerr := t.ts.ClosedFindingNodeIDs(ids); cerr == nil {
				active := f[:0]
				for _, n := range f {
					if !closed[n.ID] {
						active = append(active, n)
					}
				}
				f = active
			}
			if err := t.ts.PopulateFindingTrafficIDs(f); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			intentOf, _ := t.ts.FindingIntentsWithSources() // finding id -> 产生它的 intent id
			taskID := t.taskID
			if taskID <= 0 {
				taskID, _ = t.ts.TaskID()
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				m := compactFinding(n)
				if n.FindingID > 0 {
					m["finding_id"], m["finding_node_id"], m["traffic_count"] = n.FindingID, n.ID, n.TrafficCount
				}
				if n.Inherited {
					m["task_id"] = n.SourceTaskID
				} else {
					m["task_id"] = taskID
				}
				if iid, ok := intentOf[n.ID]; ok {
					m["intent_id"] = iid
				}
				out = append(out, m)
			}
			return jsonResult(out)
		})
}

// factsPageSize is the default page size for list_facts. Facts pile up on long
// tasks; returning all of them at once (the old behaviour) could blow up the
// context, so default to the newest page and let the agent page/filter for more.
const factsPageSize = 20

func (t *ToolSet) listFacts() actool.CoreTool {
	return t.readExpTool("list_facts", "分页列本任务及直接关联任务的【探索事实/结论】，最新在前(紧凑：id+摘要+状态，摘要过长会截断，全文用 node_detail(id))。参数均可选：limit(默认 20，上限 100)、before(游标，传上一页返回的 next_before 取更旧的一页；省略/0=最新一页)、q(按摘要关键词过滤)、include_folded(默认 true；已折进冷 digest 的事实仍返回，并带 folded_into_digest 标注——折叠 ≠ 删除)。返回 {facts, total, has_more, next_before}：total 是过滤后的总数，has_more=true 时用 next_before 继续翻页。关联任务条目带 source_task_id/inherited=true 且只读。漏洞看 list_findings。",
		obj(map[string]any{
			"limit":          intp("返回条数，默认 20，上限 100"),
			"before":         intp("分页游标：只返回 id 小于该值的更旧事实；省略或 0 = 最新一页"),
			"q":              str("按事实摘要关键词过滤（不区分大小写）；省略 = 不过滤"),
			"include_folded": map[string]any{"type": "boolean", "description": "是否包含已折叠进冷 digest 的事实，默认 true；false 只过滤当前页（分页计数不变）"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Limit         int    `json:"limit"`
				Before        int64  `json:"before"`
				Q             string `json:"q"`
				IncludeFolded *bool  `json:"include_folded"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = factsPageSize
			}
			if limit > 100 {
				limit = 100
			}
			f, hasMore, total, err := t.ts.ListByKindPageWithSources(db.KindFact, a.Before, limit, strings.TrimSpace(a.Q))
			if err != nil {
				return actool.Result{}, err
			}
			ids := make([]int64, 0, len(f))
			for _, n := range f {
				ids = append(ids, n.ID)
			}
			folded, _ := t.ts.FoldedDigestByNode(ids)
			includeFolded := a.IncludeFolded == nil || *a.IncludeFolded
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				if digest, ok := folded[n.ID]; ok {
					if !includeFolded {
						continue
					}
					item := compactFact(n)
					item["folded_into_digest"] = digest
					out = append(out, item)
					continue
				}
				out = append(out, compactFact(n))
			}
			res := map[string]any{"facts": out, "total": total, "has_more": hasMore}
			if !includeFolded {
				res["filtered_note"] = "已按 include_folded=false 过滤当前页；total 未调整，完整历史仍以 include_folded=true 为准"
			}
			if hasMore && len(f) > 0 {
				res["next_before"] = f[len(f)-1].ID // 传回它取下一页(更旧的)
			}
			return jsonResult(res)
		})
}

// factSummaryMax caps a fact summary in list_facts output. Facts carry one-line
// conclusions, but nothing enforces brevity; a runaway summary must not bloat a
// whole page. Full text stays available via node_detail(id).
const factSummaryMax = 160

// compactFact is compactNode with the summary rune-capped for list_facts, so a
// page of facts stays bounded regardless of how long any single summary grew.
func compactFact(n *db.Node) map[string]any {
	m := compactNode(n)
	if s, ok := m["summary"].(string); ok && len([]rune(s)) > factSummaryMax {
		m["summary"] = string([]rune(s)[:factSummaryMax]) + "…"
		m["summary_truncated"] = true
	}
	return m
}

func (t *ToolSet) nodeDetail() actool.CoreTool {
	return t.readExpTool("node_detail", "按 id 取本任务或直接关联任务的【探索图节点】完整内容。继承节点带 source_task_id/inherited=true 且只读。仅限 list_facts/list_findings/graph_overview 返回的探索节点 id；资产请用 list_assets/asset_neighbors。",
		obj(map[string]any{"id": idp("探索图节点 id(非资产 id)")}, "id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.ID)
			if id <= 0 {
				return actool.Errorf("id 必填"), nil
			}
			n, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil {
				return actool.Errorf(fmt.Sprintf("未找到探索节点 %d。若你想查的是资产，请用 list_assets / asset_neighbors（资产与探索节点是不同的 id 空间，资产 id 不能传给 node_detail）。", id)), nil
			}
			if err := t.ts.PopulateFindingTrafficIDs([]*db.Node{n}); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(n) // full payload incl. detail / evidence, plus explicit finding IDs
		})
}

// --- planner write tools ---

// intentItem 是 add_intent 批量/单条的一条探索方向。
type intentItem struct {
	Summary     string            `json:"summary"`
	AssetIDs    []json.RawMessage `json:"asset_ids"`
	ParentIDs   []json.RawMessage `json:"parent_ids"`
	UsesLootIDs []json.RawMessage `json:"uses_loot_ids"`
	// DependsOn 是串行链的前置意图：它们有结论（done/negative）之前，本条不会被派给 worker。
	DependsOn []json.RawMessage `json:"depends_on"`
	GoalID    json.RawMessage   `json:"goal_id"`
	Priority  int               `json:"priority"`
	RiskArea  string            `json:"risk_area"`
	// Kind 是完成口径：discovery（资产发现/侦察，允许只登记资产）| verify（验证/利用，
	// 必须写回事实或发现才算做完）。空 = 旧意图，沿用历史判定（任何写回都算产出）。
	Kind       string   `json:"kind"`
	Techniques []string `json:"techniques"`
	// IdempotencyKey 让重试/重复提交落到同一条意图，而不是靠摘要相似度猜重。
	// 非空时写进 payload；同键已存在则直接返回已有 id。
	IdempotencyKey string `json:"idempotency_key"`
	// ConvertsLeadIDs 是本意图要接手的线索 id（L<factID>-<n>，见 graph_overview
	// 的 recent_leads）。lead 的 converted 状态由它决定，不靠「来源 fact 有没有子意图」猜。
	ConvertsLeadIDs []string `json:"converts_lead_ids"`
	// Hypothesis / SuccessCriteria / StopCondition 是意图的假设与验收标准：
	// worker 按 success_criteria 判 done/no_output，复盘轮按它比较候选路线。
	Hypothesis      string `json:"hypothesis"`
	SuccessCriteria string `json:"success_criteria"`
	StopCondition   string `json:"stop_condition"`
	// Contract 是可选意图契约（P0）：显式给 mechanism 才参与方向去重；
	// produces/acceptance 是给 worker 的验收声明；缺省时完全沿用旧行为。
	Contract *intentContract `json:"contract,omitempty"`
}

// intentContract 把「一句话方向」补成可去重、可验收的声明。所有字段都是软契约：
// 缺省不改变旧 add_intent 行为；只有 mechanism 非空才计算 direction_key 去重。
type intentContract struct {
	Mechanism       string   `json:"mechanism,omitempty"`        // 稳定打法键：同目标同打法保持一致
	Route           string   `json:"route,omitempty"`            // 路线/链路名，用于态势聚合
	Produces        []string `json:"produces,omitempty"`         // 约定产物 claim
	Acceptance      string   `json:"acceptance,omitempty"`       // 产物验收条件
	Class           any      `json:"class,omitempty"`            // 0-3 或语义标签（auth/recon/web…），原样记录
	EstimatedTokens int64    `json:"estimated_tokens,omitempty"` // 预估成本（只作信号）
	Variant         string   `json:"variant,omitempty"`          // 已有方向的实质变化
	ChangeEvidence  []int64  `json:"change_evidence,omitempty"`  // 支持 variant 的事实/发现 id
	RecheckOf       int64    `json:"recheck_of,omitempty"`       // 复核哪条已结束意图
}

func canonicalIntentContractText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// normalizeIntentContract validates the optional contract before any node is
// written. Errors are reserved for structurally invalid input (bad ids/kinds);
// soft-contract issues (variant without evidence, produces without acceptance)
// come back as warnings so classic mode never turns a suggestion into a hard gate.
func (t *ToolSet) normalizeIntentContract(c *intentContract) (map[string]any, []string, error) {
	if c == nil {
		return nil, nil, nil
	}
	out := map[string]any{}
	var warns []string
	mech := strings.TrimSpace(c.Mechanism)
	if len([]rune(mech)) > 128 {
		return nil, nil, fmt.Errorf("contract.mechanism 最多 128 字符")
	}
	if mech != "" {
		out["mechanism"] = mech
	}
	route := strings.TrimSpace(c.Route)
	if len([]rune(route)) > 128 {
		return nil, nil, fmt.Errorf("contract.route 最多 128 字符")
	}
	if route != "" {
		out["route"] = route
	}
	produces := make([]string, 0, len(c.Produces))
	seen := map[string]bool{}
	for _, p := range c.Produces {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		key := canonicalIntentContractText(p)
		if seen[key] {
			continue
		}
		if len([]rune(p)) > 128 {
			return nil, nil, fmt.Errorf("contract.produces 单项最多 128 字符")
		}
		seen[key] = true
		produces = append(produces, p)
		if len(produces) >= 8 {
			break
		}
	}
	if len(produces) > 0 {
		out["produces"] = produces
	}
	acceptance := strings.TrimSpace(c.Acceptance)
	if len([]rune(acceptance)) > 1200 {
		return nil, nil, fmt.Errorf("contract.acceptance 最多 1200 字符")
	}
	if acceptance != "" {
		out["acceptance"] = acceptance
	}
	if c.Class != nil {
		switch v := c.Class.(type) {
		case float64:
			n := int(v)
			if v != float64(n) || n < 0 || n > 3 {
				return nil, nil, fmt.Errorf("contract.class 只能是 0（目标推进）/1（解除依赖）/2（新证据）/3（复核），或语义标签字符串")
			}
			out["class"] = n
		case string:
			label := strings.TrimSpace(v)
			if label == "" || len([]rune(label)) > 40 {
				return nil, nil, fmt.Errorf("contract.class 语义标签不能为空且最多 40 字符")
			}
			out["class"] = label
		default:
			return nil, nil, fmt.Errorf("contract.class 只能是 0..3 或语义标签字符串")
		}
	}
	if c.EstimatedTokens < 0 {
		return nil, nil, fmt.Errorf("contract.estimated_tokens 不能为负数")
	}
	if c.EstimatedTokens > 0 {
		out["estimated_tokens"] = c.EstimatedTokens
	}
	variant := strings.TrimSpace(c.Variant)
	if len([]rune(variant)) > 200 {
		return nil, nil, fmt.Errorf("contract.variant 最多 200 字符")
	}
	if variant != "" {
		out["variant"] = variant
	}
	if len(c.ChangeEvidence) > 0 {
		ids := make([]int64, 0, len(c.ChangeEvidence))
		seenID := map[int64]bool{}
		for _, id := range c.ChangeEvidence {
			if id <= 0 || seenID[id] {
				continue
			}
			n, err := t.ts.GetNodeWithSources(id)
			if err != nil || n == nil {
				return nil, nil, fmt.Errorf("contract.change_evidence 里的 %d 不存在于本任务或直接关联任务", id)
			}
			if n.Kind != db.KindFact && n.Kind != db.KindFinding {
				return nil, nil, fmt.Errorf("contract.change_evidence 里的 %d 是 %q 节点：只能用事实(fact)或发现(finding)", id, n.Kind)
			}
			if n.Kind == db.KindFact && n.State != "confirmed" && n.State != "origin" {
				return nil, nil, fmt.Errorf("contract.change_evidence 里的 %d 事实状态是 %q：只能用已确认(confirmed)的事实", id, n.State)
			}
			if n.Kind == db.KindFinding && n.State != "confirmed" {
				return nil, nil, fmt.Errorf("contract.change_evidence 里的 %d 发现状态是 %q：只能用已确认(confirmed)的发现", id, n.State)
			}
			seenID[id] = true
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			out["change_evidence"] = ids
		}
	}
	if c.RecheckOf > 0 {
		n, err := t.ts.GetNode(c.RecheckOf)
		if err != nil || n == nil || n.Kind != db.KindIntent {
			return nil, nil, fmt.Errorf("contract.recheck_of 必须是本任务的意图 id")
		}
		if !db.IntentStateTerminal(n.State) {
			return nil, nil, fmt.Errorf("contract.recheck_of 指向的意图 #%d 还在 %s：只能复核已结束的方向", n.ID, n.State)
		}
		out["recheck_of"] = c.RecheckOf
	}
	if variant != "" && len(c.ChangeEvidence) == 0 {
		warns = append(warns, "contract_warning: variant 没有 change_evidence 支撑；方向去重会把「只换措辞」当成同一条，建议补事实/发现 id。")
	}
	if (len(produces) > 0) != (acceptance != "") {
		warns = append(warns, "contract_warning: produces 与 acceptance 最好同时填写（产物名 + 验收条件），否则 worker 只能按一侧理解契约。")
	}
	if len(out) == 0 {
		return nil, nil, nil
	}
	return out, warns, nil
}

// intentDirectionKey fingerprints the stable direction. Only an explicit
// mechanism opts in, so old callers keep the historical add_intent behavior.
func intentDirectionKey(contract map[string]any, goalID int64, anchors []int64) string {
	if len(contract) == 0 {
		return ""
	}
	mech, _ := contract["mechanism"].(string)
	if canonicalIntentContractText(mech) == "" {
		return ""
	}
	ids := append([]int64{}, anchors...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	variant, _ := contract["variant"].(string)
	route, _ := contract["route"].(string)
	recheck, _ := contract["recheck_of"].(int64)
	raw, _ := json.Marshal(map[string]any{
		"goal_id":    goalID,
		"asset_ids":  ids,
		"mechanism":  canonicalIntentContractText(mech),
		"route":      canonicalIntentContractText(route),
		"variant":    canonicalIntentContractText(variant),
		"recheck_of": recheck,
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// 队列背压的两个旋钮。上限按「可领意图 ≥ factor×workers」算：worker 数是本任务的运行快照；
// urgent 及以上是逃生口——材料性新发现（新战果、新漏洞链）驱动的跟进不该被队列挡住。
const (
	intentQueueFactor         = 3
	intentQueueUrgentPriority = 8
)

// addOneIntent 创建一条意图节点并连上游血缘，返回 id 与一条可选的目标挂载提示。
// 血缘有三条正交的轴：
//   - 知识轴：parent_ids → derived_from（只能锚已确认的 fact/finding），顶层留空时兜底连 origin fact；
//   - 目标归属：goal_id 只作为意图的归属记录，不再写「目标→意图」边。目标只保留
//     origin→goal 的初始派生链；目标达成后由 prove_goal 写证据→目标的 proves 边。
//     意图本身始终从事实/漏洞派生，顶层意图兜底连 origin fact（与 ARTEX 一致）。
//   - 战果轴：uses_loot_ids → uses（本条方向在动用哪次已得的访问：据点/凭据）。只接受本任务里
//     仍在手(active)的战果；这条边是「哪些战果还没被推进过」的唯一依据。
func (t *ToolSet) addOneIntent(it intentItem, q *intentQuota) (int64, string, error) {
	// 本轮已判定全部目标达成（prove_goal/goal_met）后不得再派意图：否则同一轮里
	// 先收官、后面又冒出几条永远没人领的 open 意图，任务结束时 frontier 留下假待办。
	if t.GoalMet {
		return 0, "", fmt.Errorf("本轮已判定目标全部达成（prove_goal/goal_met），不能再新增意图；确有新方向，请先把目标重新打开/登记为新目标（set_goals）再派")
	}
	if strings.TrimSpace(it.Summary) == "" {
		return 0, "", fmt.Errorf("summary 不能为空")
	}
	// risk_area 是覆盖矩阵的键：空值/未知词会让这条方向落进 other，报告与覆盖度
	// 从此失真（任务 14 实测 537/580 条意图是 other）。
	// 漏填不再硬拒：按 summary 推断一个风险类并在回执里说明——开局轮整批被打回的
	// 代价是白烧一次调用（任务 30 实测：4 条意图因缺 risk_area 全部重交）。显式填写
	// 仍然优先，填错（不在词表内）照旧报错，不猜。
	riskWarn, kindWarn, queueWarn, dupWarn := "", "", "", ""
	contractPayload, contractWarns, cerr := t.normalizeIntentContract(it.Contract)
	if cerr != nil {
		return 0, "", cerr
	}
	rawRisk := strings.TrimSpace(it.RiskArea)
	riskArea := rawRisk
	riskInferred := false
	if rawRisk == "" {
		riskArea, riskInferred = inferRiskArea(it.Summary), true
	} else if normalized := db.NormalizeRiskArea(rawRisk); normalized != "other" || strings.EqualFold(rawRisk, "other") {
		riskArea = normalized
	} else if t.constraintOn("dispatch") {
		return 0, "", fmt.Errorf("risk_area=%q 不在词表内：%s", rawRisk, riskAreaEnum())
	} else {
		riskArea = "other"
		riskWarn = fmt.Sprintf("risk_warning: risk_area=%q 不在词表内，已回落 other；增强模式会拒收，这里只提醒。", rawRisk)
	}
	intentKind := strings.TrimSpace(it.Kind)
	switch intentKind {
	case "", "discovery", "verify":
	default:
		if t.constraintOn("dispatch") {
			return 0, "", fmt.Errorf("kind 只能是 discovery（资产发现/侦察，可只登记资产）或 verify（验证/利用，必须写回事实或发现），收到 %q", it.Kind)
		}
		kindWarn = fmt.Sprintf("kind_warning: kind=%q 非法，已省略（不落 payload）；增强模式会拒收，这里只提醒。", it.Kind)
		intentKind = ""
	}
	if t.reviewRound && q.round >= 3 && t.constraintOn("review_endgame") {
		return 0, "", fmt.Errorf("复盘轮最多派 3 条意图：先完成复盘（node_detail / list_facts + TodoWrite 路线池），其余方向留到下一轮")
	}
	// 意图粒度门：任何一条意图都要点名具体入口。目录型 summary（「做认证面测试」
	// 「测一遍注入」）在零事实时最容易写，但 worker 只能把每个路径各探一次就收工——
	// 任务 28/29 实测：首轮目录型意图的深度全部停在了最浅处。
	// 开局轮硬拒（此时还没有事实，重写没有代价）；中后段只警告不拦，避免白烧一轮。
	coarseWarn := ""
	if coarse, why := coarseIntentSummary(it.Summary, intentKind, len(pidList(it.AssetIDs)) > 0); coarse {
		if t.openingStrict && t.constraintOn("opening") {
			return 0, "", fmt.Errorf("%s", why)
		}
		coarseWarn = "coarse_warning: " + firstLine(why, 200)
	}
	// 意图形态：verify 意图最好是「一条验证链」，不是单点检查。只警告不拦——
	// 单点方向仍然有价值，但复盘轮会把它们合并成链（任务 30 的单意图深度 33.6 vs ARTEX 67）。
	chainWarn := ""
	if intentKind == "verify" && !hasVerificationChain(it.Summary) {
		chainWarn = "chain_warning: 这条 verify 意图看起来是单点检查（没有「先…再…/命中后…」的验证链）。若它只是测一个入口的一个点，考虑并入更长的利用链，或改标 discovery/广度意图。"
	}
	// ── P20 探索策略门（意图层，硬拒）──
	// 放在所有 DB 结构校验之前：额度用尽/方向被禁时，不该再为一条注定被拒的意图
	// 去查 parent/goal，模型也该先听到"别再往这个方向想"。
	if t.pol != nil && !t.pol.IsZero() {
		in := policy.IntentInput{Summary: it.Summary, OpenCount: q.open, RoundUsed: q.round, TotalUsed: q.total}
		v := t.pol.CheckIntentWithOptions(in, policy.CheckOptions{IgnoreRoundCap: !t.constraintOn("dispatch")})
		if !v.Allow {
			t.reportPolicyReject(it, v)
			return 0, "", fmt.Errorf("%s", policy.RejectText(v, it.Summary))
		}
		if v.Benign {
			// 命中词表但处于否定语境（"检查是否有爆破防护"）：放行，但留痕——
			// 事后能看出策略是不是太糙、误放了多少。
			t.reportPolicyReject(it, v)
		}
	}
	// 显式幂等键：重试/重复提交不该造第二条意图。只有非空键才生效——不能用
	// summary 相似度冒充幂等（换措辞的同一方向由防打转逻辑处理）。
	idempotencyKey := strings.TrimSpace(it.IdempotencyKey)
	if idempotencyKey != "" {
		if existing, err := t.ts.FindNodeByPayload(db.KindIntent, "idempotency_key", idempotencyKey); err == nil && existing > 0 {
			return existing, fmt.Sprintf("幂等键 %q 已对应意图 #%d，未重复创建", idempotencyKey, existing), nil
		}
	}
	// 先校验锚点（建节点前，避免坏锚点留下孤儿意图）。
	parents := pidList(it.ParentIDs)
	hasFindingParent := false
	for _, pidv := range parents {
		n, err := t.ts.GetNodeWithSources(pidv)
		if err != nil || n == nil {
			return 0, "", fmt.Errorf("parent_id %d 不存在于本任务或直接关联任务：parent_ids 必须是已存在的【事实(fact)/发现(finding)】节点 id；顶层全新方向请留空 parent_ids", pidv)
		}
		if n.Kind != db.KindFact && n.Kind != db.KindFinding {
			return 0, "", fmt.Errorf("parent_id %d 是 %q 节点，不能作为意图锚点：意图只能锚在已确认的【事实(fact)/发现(finding)】上，不能挂在意图/目标/提示上；顶层全新方向请留空 parent_ids", pidv, n.Kind)
		}
		if n.Kind == db.KindFinding {
			hasFindingParent = true
		}
	}
	// 目标轴校验（同样在建节点前）：显式 goal_id 必须是本任务的 open 目标。
	goalID := pid(it.GoalID)
	if goalID > 0 {
		g, err := t.ts.GetNode(goalID)
		if err != nil || g == nil || g.Kind != db.KindGoal {
			return 0, "", fmt.Errorf("goal_id %d 不是本任务的目标节点（目标 id 见 graph_overview 的 goals；只有确实没有明确目标时才留空）", goalID)
		}
		if g.State != "open" {
			return 0, "", fmt.Errorf("goal_id %d 已是 %s 状态：意图只能挂到未达成(open)的目标上", goalID, g.State)
		}
	}
	// 多目标时不许留空：没有归属的意图不会推进任何目标，图上也看不出它服务于哪条线。
	if goalID == 0 {
		if goals, gerr := t.ts.ListByKind(db.KindGoal, 1000); gerr == nil {
			var openIDs []int64
			for _, g := range goals {
				if g.State == "open" {
					openIDs = append(openIDs, g.ID)
				}
			}
			if len(openIDs) > 1 && t.constraintOn("dispatch") {
				return 0, "", fmt.Errorf("本任务有 %d 个未达成目标（%v）：goal_id 必填，指明这条方向服务哪个目标；只有确实不属于任何目标时才可留空", len(openIDs), openIDs)
			}
		}
	}
	// 战果轴校验（同样在建节点前）：uses_loot_ids 必须是本任务里真实存在、且仍在手的战果。
	// 拒收已失效的战果是有意义的——据点掉了/凭据失效了还按它派意图，等于派一条注定跑空的活，
	// 不如让 planner 当场听到「这条已经不能用了」。
	lootIDs := pidList(it.UsesLootIDs)
	for _, lid := range lootIDs {
		n, err := t.ts.GetNode(lid)
		if err != nil || n == nil {
			return 0, "", fmt.Errorf("uses_loot_ids 里的 %d 不存在于本任务：只能填 graph_overview 里 footholds/credentials 的节点 id", lid)
		}
		if n.Kind != db.KindFoothold && n.Kind != db.KindCredential {
			return 0, "", fmt.Errorf("uses_loot_ids 里的 %d 是 %q 节点，不是战果：只能填据点(foothold)或凭据(credential)的节点 id", lid, n.Kind)
		}
		if n.State != db.StateLootActive {
			return 0, "", fmt.Errorf("战果节点 %d 当前状态是 %q（已失效）：不能按它派意图，请先确认它是否还能用", lid, n.State)
		}
	}
	// 串行链：前置必须是本任务的意图。**不必查环**——新意图此刻还没有任何出边，
	// 环只能由"先建 A 再给 B 加边指向 A"之外的路径形成，而本入口只在新节点上建边。
	deps := pidList(it.DependsOn)
	depsSatisfied := true
	for _, dep := range deps {
		n, err := t.ts.GetNode(dep)
		if err != nil || n == nil {
			return 0, "", fmt.Errorf("depends_on 里的 %d 不存在于本任务：前置只能是本任务的意图 id", dep)
		}
		if n.Kind != db.KindIntent {
			return 0, "", fmt.Errorf("depends_on 里的 %d 是 %q 节点，不是意图：串行链的前置只能是意图", dep, n.Kind)
		}
		if n.State != "done" && n.State != "negative" {
			depsSatisfied = false
		}
		// 前置已经是“永远不会有结论”的收场（blocked/exhausted/no_output/…）：这条链
		// 一定跑不起来。当场拒收，比让它默默排在那里（永远进不了 frontier）好——
		// 后者才是“深度链凭空消失”的真实来路。
		if db.IntentStateDead(n.State) {
			return 0, "", fmt.Errorf("depends_on 里的前置意图 #%d 已经以 %q 收场（不会有结论）：这条链永远跑不起来。"+
				"先重派前置，或去掉 depends_on、把这一步需要的输入自己写进意图", n.ID, n.State)
		}
	}
	priority := it.Priority
	if priority == 0 {
		priority = 5
		// 深度跟进（动用已得战果 / 由已确认发现派生）默认排在广度意图前面。
		// 7 < 队列背压的 urgent 阈值 8，所以仍受背压约束——只调默认，不改排序规则。
		if len(pidList(it.UsesLootIDs)) > 0 || hasFindingParent {
			priority = 7
		}
	}
	// ── 队列背压（软上限）──
	// 生产端每次唤醒最多 4 条，消化端只有 workers 个 worker、每条跑几分钟——不设上限时
	// 待领意图会一路涨。按**可领**计数：被前置挡住的链路步骤不消耗 worker，也不占额度
	// （它们此刻进不了 frontier）；而带已满足前置的意图立即可领，照样受上限约束。
	if t.workerCap > 0 && depsSatisfied && priority < intentQueueUrgentPriority && q.claimable >= intentQueueFactor*t.workerCap {
		msg := fmt.Sprintf("队列里已有 %d 条可领方向（worker %d 个，上限 %d）：本轮先让 worker 消化，不再接常规新方向。"+
			"确有材料性新发现驱动的跟进，把 priority 提到 %d 以上再提交；已过时的排队方向用 drop_intent 清掉。"+
			"**这个方向本身不必丢**：把它写进 TodoWrite（注明为什么先不派），等 worker 消化后再派——别直接忘掉",
			q.claimable, t.workerCap, intentQueueFactor*t.workerCap, intentQueueUrgentPriority)
		if t.constraintOn("dispatch") {
			return 0, "", fmt.Errorf("%s", msg)
		}
		queueWarn = "queue_warning: " + msg
	}
	anchors := pidList(it.AssetIDs)
	// 资产拦截：意图绑定的资产若命中系统资产拦截规则，则禁止下发该意图。
	if t.as != nil && len(anchors) > 0 {
		hits, err := t.as.CheckAssetsIntercept(t.taskID, anchors)
		if err != nil {
			return 0, "", fmt.Errorf("资产拦截校验失败：%w", err)
		}
		if len(hits) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "意图「%s」绑定的资产未通过测试范围校验，请停止对相关资产进行测试：", it.Summary)
			for _, h := range hits {
				fmt.Fprintf(&b, "\n - %s", h.Describe())
			}
			return 0, "", fmt.Errorf("%s", b.String())
		}
	}
	// 防打转：同资产 + 同风险类 + 同义描述，要么已有在跑的、要么已经结束过 3 次。
	// 判定放在策略/资产/血缘/战果校验之后、建节点之前：前面的校验说的是"能不能做"，
	// 这一条说的是"这件事是不是已经有人在做/已经做过了"。
	if rej := t.checkIntentSpin(q, it.Summary, riskArea, anchors, strings.TrimSpace(it.RiskArea) != "", it.Techniques, it.Kind); rej != "" {
		if t.constraintOn("dispatch") {
			return 0, "", fmt.Errorf("%s", rej)
		}
		dupWarn = "dup_warning: " + rej
	}
	leadIDs, lerr := t.validateLeadIDs(it.ConvertsLeadIDs)
	if lerr != nil {
		return 0, "", lerr
	}
	payload := map[string]any{"summary": it.Summary, "risk_area": riskArea}
	if len(leadIDs) > 0 {
		payload["converted_lead_ids"] = leadIDs
	}
	if h := strings.TrimSpace(it.Hypothesis); h != "" {
		payload["hypothesis"] = h
	}
	if sc := strings.TrimSpace(it.SuccessCriteria); sc != "" {
		payload["success_criteria"] = sc
	}
	if stop := strings.TrimSpace(it.StopCondition); stop != "" {
		payload["stop_condition"] = stop
	}
	if intentKind != "" {
		// 完成口径随意图落库：收尾判定按它区分「资产发现」与「验证」——
		// verify 意图只登记资产不算测过（见 server.settleIntentState）。
		payload["intent_kind"] = intentKind
	}
	if idempotencyKey != "" {
		payload["idempotency_key"] = idempotencyKey
	}
	if len(contractPayload) > 0 {
		payload["contract"] = contractPayload
	}
	// ATT&CK 技术标注：**标错不该让整条方向作废**，所以非法项由 NormalizeTechniques 丢弃，
	// 不在这里返错。它是给交付物用的分类，不是约束。
	if techs := db.NormalizeTechniques(it.Techniques); len(techs) > 0 {
		payload["techniques"] = techs
	}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	// 目标轴：没显式指名时，恰有一个 open 目标就自动记录归属；多个不猜（回执提示）。
	// 注意：这里只记录 goal_id，不建 goal→intent 边（目标只连 origin，完成时才连证据）。
	// 必须在建边前确定，否则原子提交时还不知道要写哪条 goal 边。
	if goalID == 0 {
		if only, err := t.ts.SingleOpenGoal(); err == nil && only > 0 {
			goalID = only
		}
	}
	// 方向键只在显式给 mechanism 时生效：同目标 + 同资产 + 同机制 + 同 variant
	// 视为同一方向，直接返回已有意图，避免换措辞重派。要重开必须用
	// contract.variant + change_evidence 说明实质变化（键会随之改变）。
	if key := intentDirectionKey(contractPayload, goalID, anchors); key != "" {
		payload["direction_key"] = key
		if existing, err := t.ts.FindNodeByPayload(db.KindIntent, "direction_key", key); err == nil && existing > 0 {
			if n, nerr := t.ts.GetNode(existing); nerr == nil && n != nil && n.State != "deleted" {
				note := fmt.Sprintf("direction_dedup: 同目标/同资产/同机制方向已存在 #%d（state=%s），未重复创建；要重开请用 contract.variant + change_evidence 说明实质变化。", existing, n.State)
				if len(contractWarns) > 0 {
					note += "\n" + strings.Join(contractWarns, "\n")
				}
				return existing, note, nil
			}
		}
	}
	if goalID > 0 {
		payload["goal_id"] = goalID
	}
	// 节点、锚点与全部血缘边一次提交：任何一条边写失败都会回滚整条意图，
	// 不会出现"意图已 open、worker 已能领取、但 depends_on/goal 边还没写"的窗口。
	// 0 端点表示"正在创建的这条意图"（见 db.IntentEdge）。
	edges := make([]db.IntentEdge, 0, len(parents)+len(lootIDs)+len(deps)+2)
	for _, parent := range parents {
		edges = append(edges, db.IntentEdge{From: parent, Rel: db.RelDerivedFrom, To: 0})
	}
	for _, lid := range lootIDs {
		edges = append(edges, db.IntentEdge{From: 0, Rel: db.RelUses, To: lid})
	}
	for _, dep := range deps {
		edges = append(edges, db.IntentEdge{From: 0, Rel: db.RelDependsOn, To: dep})
	}
	// 兜底血缘：意图没有显式 fact/finding 父节点时，统一连任务起点（origin fact）。
	// 这与 ARTEX 的起点派生一致；目标不参与意图链（目标只保留 origin→goal 初始边，
	// 达成后才由 prove_goal 连证据）。图里已有事实却漏填 parent_ids 时仍会提示补真实来源，
	// 但不会再让意图从目标长出来。
	fellBackToOrigin := false
	if len(parents) == 0 {
		if origin, _ := t.ts.OriginFactID(); origin > 0 {
			edges = append(edges, db.IntentEdge{From: origin, Rel: db.RelDerivedFrom, To: 0})
			fellBackToOrigin = true
		}
	}
	id, err := t.ts.AddIntentGraph(payload, priority, anchors, "planner", edges)
	if err != nil {
		return 0, "", err
	}
	q.round++
	q.open++
	q.total++
	if depsSatisfied {
		q.claimable++
	}
	t.intentsRound++
	// 同一批里刚建的方向也要参与后续判定：否则一次 add_intent 带 4 条同义意图时，
	// 它们互相都看不见，等于没拦。
	newAssets := make(map[int64]bool, len(anchors))
	for _, a := range anchors {
		newAssets[a] = true
	}
	q.rememberIntent(intentFP{id: id, summary: it.Summary, riskArea: riskArea, state: "open",
		kind: it.Kind, assets: newAssets, techniques: techniqueSetOfStrings(it.Techniques)})
	// 目标轴：显式 goal_id 已在上面校验；自动挂载已在建边前完成。这里只补
	// "有多个 open 目标却没指名"的回执提示（不猜，也不阻止创建）。
	note := ""
	if goalID == 0 {
		if goals, gErr := t.ts.ListByKind(db.KindGoal, 1000); gErr == nil {
			open := 0
			for _, g := range goals {
				if g.State == "open" {
					open++
				}
			}
			if open > 1 {
				note = "本任务有多个未达成目标，本条意图未记录目标归属：请在 goal_id 指名它服务的目标（见 graph_overview 的 goals）；goal_id 只作归属记录，不会生成目标→意图边。"
			}
		}
	}
	if fellBackToOrigin {
		hint := "本条意图没有给 parent_ids，已兜底连到任务起点（与 ARTEX 的起点派生一致）。" +
			"若它其实由某条事实/漏洞驱动，请用 parent_ids 指明来源，或用 link_intent_source 补回真实来源。"
		if note != "" {
			note += "\n" + hint
		} else {
			note = hint
		}
	}
	// 血缘边已随节点在同一事务里提交（见上面的 edges / AddIntentGraph）。
	if len(deps) > 0 {
		parts := make([]string, 0, len(deps))
		for _, d := range deps {
			parts = append(parts, fmt.Sprintf("#%d", d))
		}
		warn := fmt.Sprintf("本条在等前置 %s 有结论（done/negative）之前不会被派给 worker；前置卡住时可用 unblock_intent 拆链。",
			strings.Join(parts, "、"))
		if note == "" {
			note = warn
		} else {
			note += " " + warn
		}
	}
	if riskInferred {
		hint := fmt.Sprintf("risk_area 未填，已按 summary 推断为 %s（覆盖矩阵按它统计；要精确请在下次显式填写）", riskArea)
		if note == "" {
			note = hint
		} else {
			note += "\n" + hint
		}
	}
	if coarseWarn != "" {
		if note == "" {
			note = coarseWarn
		} else {
			note += "\n" + coarseWarn
		}
	}
	if chainWarn != "" {
		if note == "" {
			note = chainWarn
		} else {
			note += "\n" + chainWarn
		}
	}
	for _, w := range append([]string{riskWarn, kindWarn, queueWarn, dupWarn}, contractWarns...) {
		if w == "" {
			continue
		}
		if note == "" {
			note = w
		} else {
			note += "\n" + w
		}
	}
	return id, note, nil
}

// breadthWarning is the soft breadth-floor nudge returned after an add_intent
// batch that only targeted already-covered (or in-flight) assets while concrete
// assets still have no terminal intent. It never rejects the batch.
func breadthWarning(b *db.BreadthSummary, covered map[int64]bool) string {
	if b == nil {
		return ""
	}
	remaining := 0
	var samples []string
	for _, g := range b.TopGaps {
		if !b.DebtIDs[g.AssetID] || covered[g.AssetID] {
			continue
		}
		remaining++
		if len(samples) < 3 {
			samples = append(samples, fmt.Sprintf("%s(%s,%s)", g.Label, g.Type, g.State))
		}
	}
	if remaining == 0 {
		return ""
	}
	// TopGaps is capped, so debt may exceed the sampled window.
	if b.Debt-len(covered) > remaining {
		remaining = b.Debt - len(covered)
	}
	pct := "null"
	if b.Pct != nil {
		pct = fmt.Sprintf("%.1f%%", *b.Pct*100)
	}
	return fmt.Sprintf("本轮新增意图没有绑定仍有广度缺口的具体资产（debt=%d，coverage=%s；样本：%s）。"+
		"下一轮请至少派 1 条广度意图：带 asset_ids + risk_area，写清入口/参数与要验证的假设，"+
		"不要只写“看一下/补一下覆盖”；完整缺口用 coverage_matrix 查看。",
		remaining, pct, strings.Join(samples, "、"))
}

// dismissLeads 把不再跟进的线索标记为 dismissed。线索不是任务，不需要走
// drop_intent；但「为什么放弃」必须留下，复盘时才能分清「没价值」与「漏掉了」。
func (t *ToolSet) dismissLeads() actool.CoreTool {
	return t.writeExpTool("dismiss_leads",
		"把不再跟进的线索标记为 dismissed（reason 必填）。线索来自 record_fact 的 leads 或 graph_overview 的 recent_leads；\n"+
			"已 dismiss 的线索不再出现在 recent_leads，也不能再用 converts_lead_ids 转意图。",
		obj(map[string]any{
			"lead_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "要 dismiss 的线索 id（L<事实id>-<序号>，来自 recent_leads）"},
			"reason":   str("【必填】为什么不再跟进：已否定/重复/超出范围/代价过高"),
		}, "lead_ids", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				LeadIDs []string `json:"lead_ids"`
				Reason  string   `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			reason := strings.TrimSpace(a.Reason)
			if reason == "" {
				return actool.Errorf("reason 必填：写明为什么不再跟进这条线索"), nil
			}
			byFact := map[int64][]int{}
			for _, raw := range a.LeadIDs {
				fid, idx, ok := parseLeadID(raw)
				if !ok {
					return actool.Errorf(fmt.Sprintf("lead_ids 里的 %q 不是合法 lead id（格式 L<事实id>-<序号>）", raw)), nil
				}
				byFact[fid] = append(byFact[fid], idx)
			}
			if len(byFact) == 0 {
				return actool.Errorf("lead_ids 必填：至少给一条 recent_leads 里的线索 id"), nil
			}
			dismissed := 0
			for fid, idxs := range byFact {
				n, err := t.ts.GetNode(fid)
				if err != nil || n == nil || n.Kind != db.KindFact {
					return actool.Errorf(fmt.Sprintf("线索 %d 指向的事实不存在于本任务", fid)), nil
				}
				var p map[string]any
				_ = json.Unmarshal(n.Payload, &p)
				leads, _ := p["leads"].([]any)
				var dis []string
				if arr, ok := p["dismissed_leads"].([]any); ok {
					for _, v := range arr {
						if sv, ok := v.(string); ok {
							dis = append(dis, sv)
						}
					}
				}
				for _, idx := range idxs {
					if idx < 0 || idx >= len(leads) {
						return actool.Errorf(fmt.Sprintf("线索 %s 超出事实 %d 的线索条数（共 %d 条）", leadID(fid, idx), fid, len(leads))), nil
					}
					id := leadID(fid, idx)
					found := false
					for _, d := range dis {
						if strings.EqualFold(d, id) {
							found = true
							break
						}
					}
					if !found {
						dis = append(dis, id)
						dismissed++
					}
				}
				p["dismissed_leads"] = dis
				if err := t.ts.SetNodePayload(fid, db.KindFact, p); err != nil {
					return actool.Errorf(fmt.Sprintf("写回线索状态失败：%v", err)), nil
				}
			}
			return actool.Text(fmt.Sprintf("leads dismissed: %d（%s）", dismissed, firstLine(reason, 100))), nil
		})
}

func (t *ToolSet) addIntent() actool.CoreTool {
	return t.writeExpTool("add_intent", "生成【探索方向】写入 frontier，并连入探索链路。意图是开放的探索方向，不是固定类型——用 summary 一句话自由描述要探索/验证/利用什么；**summary 必须点名具体入口**（路径/主机/参数/版本），只有风险类名称会被拒收。\n"+
		"★summary 只写入口+动作+验证目标：不写纪律/操作规范/速率/UA/账号/请求上限/与其它意图不重叠段落；操作边界由 constraintBlock/policyBlock 统一注入，不要重复。\n"+
		"★优先批量：一轮筛出的多个新方向放进 intents 数组一次提交（比逐条调用省往返）。返回 ids 数组，与 intents 等长同序（失败项 id=0，详情见 errors）。单条则省略 intents 直接给顶层 summary。\n"+
		"★广度回执：若范围内仍有具体资产没有终态覆盖，而本批没有一条绑定这些资产，返回会带 breadth_warning（不拒收）——下一轮至少补 1 条广度意图。",
		obj(map[string]any{
			"intents":           map[string]any{"type": "array", "description": "【优先用这个】要新增的探索方向数组，按顺序处理。每个元素字段同下方顶层字段——**一次带全 summary + risk_area + goal_id（有多个未达成目标时必填）+ asset_ids + kind**；risk_area 可省略（会按 summary 推断）。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"summary":           str("[单条] 一句话描述这个探索方向：在**哪个具体入口**（路径/主机/参数/版本）上做什么+验证什么。**必须点名具体入口**——只写“做认证面测试 / 测一遍注入”这类风险类目录会被拒收：worker 只能每个路径各探一次就收工。**不要写纪律/操作规范/速率/UA/账号/请求上限/与其它意图不重叠段落**（操作约束由平台统一注入）；risk_area 只是台账键，不是方向本身。"),
			"asset_ids":         map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "本方向要测试/攻击的【目标资产 id】（**尽量传**，0/1/多个；是 list_assets 返回的资产 id，不是探索节点 id）：这条探索方向针对哪些资产（站点/接口/参数/主机等）。只要方向围绕某些具体资产就务必传上——它是「这条探索打哪些目标」的结构化标记，用于覆盖去重、把意图连入资产链路。仅当纯全局侦察、确实没有具体目标资产时才留空。"},
			"parent_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "上游锚点 id（可选，0/1/多个）：本方向由哪些【已确认的事实(fact)/发现(finding)】综合得出。**只能填已存在的 fact/finding 节点 id,不能填意图/目标/提示**——意图必须锚在已确认知识上,发现驱动而非凭空规划。多个事实共同产生一个新意图就传多个;顶层全新侦察方向请留空（会自动挂到任务起点 origin fact）。"},
			"goal_id":           idp("【目标归属】本条方向服务的目标 id（graph_overview 的 goals）：仅作归属记录，不生成「目标→意图」边；意图链从起点/事实派生，目标只在 prove_goal 后由证据连边。恰有一个未达成(open)目标时省略会自动记录；**有多个未达成目标时必填**（不猜），无目标的直投任务可留空。"),
			"depends_on":        map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "【串行链】前置意图 id（可选，0/1/多个）：本方向要用到某个前一步的**实际产出**时填。系统在前置以 done/negative 收场前不会把这条派给 worker（下游拿不到前置只会空转）。前置失败时本条会一直停着——重派前置，或用 unblock_intent 拆链。只填本任务的意图 id。"},
			"uses_loot_ids":     map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "【本方向在动用哪次已得的访问】战果节点 id（graph_overview 的 footholds/credentials；可选，0/1/多个）：这条方向的立足点来自某个据点/某条凭据时**务必传**。它让系统能算出「哪些战果还没被推进过」——不传的话那条战果会一直显示为 outstanding，你也看不出它是否已经被别的意图接手了。"},
			"priority":          intp("优先级 0-10，默认5"),
			"risk_area":         str("【覆盖矩阵键】本方向要打的风险类：sqli(注入) | xss | rce(命令执行/反序列化) | ssrf | ssti | xxe | idor(对象级越权) | authz(功能级越权) | auth(认证/会话) | logic(业务逻辑/竞态) | upload | traversal | disclosure | other。**必须填写**——报告与覆盖度按「范围内资产 × 风险类」统计；不填会归入 other，覆盖矩阵与报告都会因此失真。"),
			"kind":              str("【完成口径】discovery：资产发现/侦察，允许只登记资产就结束；verify：验证/利用，必须写回事实(record_fact)、发现(report_finding)或战果才算做完——只登记资产会被判为 no_output。省略 = 历史判定（任何写回都算产出）。"),
			"idempotency_key":   str("可选幂等键：重试或重复提交同一条方向时带上同一个键，平台直接返回已有意图，不重复创建。"),
			"converts_lead_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "【接手的线索】graph_overview.recent_leads 里的 lead id（L<事实id>-<序号>）：这条意图是在把某条线索转成探索方向时填。填了之后该 lead 标记为 converted。"},
			"hypothesis":        str("【假设】这条方向验证的假设（可选但推荐）：如「nopCommerce 4.x 的 PSAjax 堆叠注入可读到 QueuedEmail」"),
			"success_criteria":  str("【验收标准】什么算测到/测完（可选但推荐）：如「拿到 version() 或 current_user 回显」「登录后 /admin 返回 200 且带后台 cookie」"),
			"stop_condition":    str("【停止条件】什么情况下不再投入（可选）：如「连续 3 种编码均被 WAF 拦」「需要管理员会话且无法获得」"),
			"contract": map[string]any{
				"type":        "object",
				"description": "【可选意图契约（P0，软契约）】显式填 mechanism 才参与方向去重：同目标+同资产+同机制+同 variant 会直接返回已有意图；要重开用 variant + change_evidence 说明实质变化。produces/acceptance 是给 worker 的验收声明。旧调用可完全省略。",
				"properties": map[string]any{
					"mechanism":        str("稳定机制键：同目标同资产同打法始终用同一值，换措辞不算新方向。"),
					"route":            str("路线/链路名（如 登录链、上传链），用于态势聚合。"),
					"produces":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "约定产物 claim（最多 8 个）：如 version() 回显、后台 cookie、可读文件路径。"},
					"acceptance":       str("产物验收条件：什么证据算满足 produces。"),
					"class":            str("0 目标推进 / 1 解除依赖 / 2 新证据 / 3 复核；也可填语义标签（auth/recon/web 等），平台原样记录。"),
					"estimated_tokens": intp("预估 token 成本（只作信号，>0 才记录）。"),
					"variant":          str("同一机制重开时的实质变化；建议同时给 change_evidence。"),
					"change_evidence":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "支持 variant 的事实/发现节点 id。"},
					"recheck_of":       intp("复核哪条已结束意图；与 variant 一起构成新的方向键。"),
				},
			},
			"techniques": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "【ATT&CK 技术 ID】本方向用到的技术，如 T1190(利用公开漏洞) / T1078(有效账户) / T1552.001(凭据文件) / T1021(远程服务)。可选但**尽量填**——红队交付物按 ATT&CK 组织，报告会据此汇总「本次作业覆盖了哪些技术」；不标就只剩一堆没有共同语言的方向描述。只写 ID（T#### 或 T####.###），不要写技术名。"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Intents    []intentItem `json:"intents"`
				intentItem              // 单条模式：顶层 summary/asset_ids/parent_ids/priority
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Intents) > 0
			items := a.Intents
			if !batch {
				items = []intentItem{a.intentItem}
			}

			// 一次调用共享一份额度快照（库存量各查一次库，本轮用量在循环里累加）。
			quota := t.beginIntentQuota()
			// 广度快照：通常已由本轮 graphOverviewData 填充；同一 ToolSet 不重复查矩阵。
			var breadth *db.BreadthSummary
			debtIDs := map[int64]bool{}
			if b := t.breadthSummary(); b != nil && b.Debt > 0 {
				breadth = b
				debtIDs = b.DebtIDs
			}
			if t.breadthNewDebt == nil {
				t.breadthNewDebt = map[int64]bool{}
			}
			breadthTouched := false
			ids := make([]int64, len(items))
			errs := map[string]string{}
			createdAny := false
			goalNote := ""
			for i, it := range items {
				id, note, err := t.addOneIntent(it, quota)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				createdAny = true
				for _, assetID := range pidList(it.AssetIDs) {
					if debtIDs[assetID] {
						breadthTouched = true
						t.breadthNewDebt[assetID] = true
					}
				}
				if note != "" {
					if goalNote != "" {
						goalNote += "\n"
					}
					goalNote += note
				}
			}
			breadthWarn := ""
			if createdAny && breadth != nil && !breadthTouched && t.constraintOn("breadth") {
				breadthWarn = breadthWarning(breadth, t.breadthNewDebt)
			}

			// 人经主 agent 直投意图 → 若任务已 done（无 open 目标的 goalless 分支），把它
			// 拉回 running，worker 才能领这条意图执行。resumeTask 仅由主 agent 的 Chat 接入
			// (SetResumeTask)；planner 的 ToolSet 为 nil，故 planner 自己调 add_intent 时此段
			// no-op，不影响其正常产意图。意图节点已在上面建好(open)，复活时不会被误判抽干。
			if createdAny && t.resumeTask != nil {
				t.resumeTask()
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				body := fmt.Sprintf("intent created: %d", ids[0])
				if goalNote != "" {
					body += "\n" + goalNote
				}
				if breadthWarn != "" {
					body += "\nbreadth_warning: " + breadthWarn
				}
				if ew := t.endgameWarning(); ew != "" {
					body += "\nendgame_warning: " + ew
				}
				return actool.Text(body), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			if goalNote != "" {
				out["goal_note"] = goalNote
			}
			if breadthWarn != "" {
				out["breadth_warning"] = breadthWarn
			}
			if ew := t.endgameWarning(); ew != "" {
				out["endgame_warning"] = ew
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) listGoals() actool.CoreTool {
	return t.readExpTool("list_goals", "列出本任务的目标节点及其状态（open/met），用于判断是否达成。",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			g, _ := t.ts.ListByKind(db.KindGoal, 100)
			return jsonResult(g)
		})
}

func (t *ToolSet) proveGoal() actool.CoreTool {
	return t.writeExpTool("prove_goal", "当你判断某个发现/事实证明了某个目标达成时调用：把证据节点连到目标节点，并标记目标 met。",
		obj(map[string]any{
			"goal_id":     idp("目标节点 id"),
			"evidence_id": idp("证明它的发现/事实节点 id"),
			"reason":      str("为什么这个证据满足该目标"),
		}, "goal_id", "evidence_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				GoalID     json.RawMessage `json:"goal_id"`
				EvidenceID json.RawMessage `json:"evidence_id"`
				Reason     string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			goal, ev := pid(a.GoalID), pid(a.EvidenceID)
			if goal == 0 || ev == 0 {
				return actool.Errorf("goal_id 和 evidence_id 必填"), nil
			}
			goalNode, err := t.ts.GetNode(goal)
			if err != nil || goalNode == nil || goalNode.Kind != db.KindGoal {
				return actool.Errorf("goal_id 必须是本任务的目标节点（关联任务目标只读）"), nil
			}
			evidenceNode, err := t.ts.GetNodeWithSources(ev)
			if err != nil || evidenceNode == nil || (evidenceNode.Kind != db.KindFact && evidenceNode.Kind != db.KindFinding) {
				return actool.Errorf("evidence_id 必须是本任务或直接关联任务的事实/漏洞节点"), nil
			}
			// 清理门 + 收官审计：只有这次 prove 会关闭**最后一个**目标时才跑——
			// 中途证明单个目标不该被全局缺口挡住。block 直接拒收；notice 放行并带警告。
			completionNotice := ""
			if t.isLastOpenGoal(goal) {
				if msg := t.cleanupGate(); msg != "" {
					return actool.Errorf(msg), nil
				}
				if t.constraintOn("completion") {
					if gaps := t.completionGaps(); len(gaps) > 0 {
						switch t.completionGateMode() {
						case "block":
							if t.completionBlocked != nil {
								t.completionBlocked()
							}
							if t.forceReview != nil {
								t.forceReview() // 下一轮先复盘，别原地重试
							}
							return actool.Errorf(formatCompletionGaps(gaps)), nil
						case "notice":
							completionNotice = "\n\n⚠ " + formatCompletionGaps(gaps)
						}
					}
				}
			}
			if err := t.ts.Link(ev, db.RelProves, goal); err != nil {
				log.Printf("[goal] 关联证据失败 goal=%d evidence=%d: %v", goal, ev, err)
			}
			if err := t.ts.SetNodeState(goal, "met"); err != nil {
				// 没置成 met 会导致目标反复"已完成却没过"，比缺一条边更严重。
				log.Printf("[goal] 标记目标 %d 为 met 失败: %v", goal, err)
			}
			// 每标记一个目标 met，就检查本任务是否【所有目标】都已 met；若是，自动判定
			// 任务完成（置 GoalMet），无需再依赖模型显式调 goal_met。
			if goals, err := t.ts.ListByKind(db.KindGoal, 1000); err == nil && len(goals) > 0 {
				allMet := true
				for _, g := range goals {
					if g.State != "met" {
						allMet = false
						break
					}
				}
				if allMet {
					t.GoalMet = true
					t.Reason = fmt.Sprintf("所有 %d 个目标均已 met（最后由 goal %d 触发）", len(goals), goal)
					return actool.Text(fmt.Sprintf("goal %d marked met；本任务所有目标均已达成，任务自动判定完成%s", goal, completionNotice)), nil
				}
			}
			return actool.Text(fmt.Sprintf("goal %d marked met%s", goal, completionNotice)), nil
		})
}

func (t *ToolSet) goalMet() actool.CoreTool {
	return writeTool("goal_met", "【立即结束整个任务】——**仅用于没有显式目标(goals)的任务**：有目标时请用 prove_goal 逐个证明（系统会在最后一个目标达成时自动收官）。它必须带上证明任务已达成的【证据节点】(evidence_id)，空口无凭的「完成了」会被拒收。⚠️它不是用来“结束本轮规划”的：本轮没有新意图要派、或在等 worker 产出，都【直接结束本轮即可，不要调本工具】（0 个意图是完全正常的）。",
		obj(map[string]any{
			"reason":      str("达成理由：这个证据为什么说明任务已经完成"),
			"evidence_id": idp("证明任务已达成的事实/漏洞节点 id（必填；必须是前面 work 或主 Agent 已写下的结论，planner 只有 record_probe_fact，不能写通用结论）"),
		}, "reason", "evidence_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("goal_met 需要任务上下文"), nil
			}
			var a struct {
				Reason     string          `json:"reason"`
				EvidenceID json.RawMessage `json:"evidence_id"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Reason) == "" {
				return actool.Errorf("goal_met: reason 必填——写清这个证据为什么说明任务已达成"), nil
			}
			// 有目标的任务只能用 prove_goal：那里有「证据→目标」的边，goal_met 没有。
			if goals, err := t.ts.ListByKind(db.KindGoal, 1000); err == nil && len(goals) > 0 {
				return actool.Errorf(fmt.Sprintf("本任务有 %d 个目标：请用 prove_goal 逐个证明（最后一个达成时系统会自动收官）。"+
					"goal_met 只用于没有显式目标的任务——它会跳过「证据→目标」的关联", len(goals))), nil
			}
			ev := pid(a.EvidenceID)
			if ev <= 0 {
				return actool.Errorf("goal_met: evidence_id 必填——没有证据的「任务完成」是一句口号。" +
					"请指向证明任务已达成的【事实/漏洞】节点（planner 只有 record_probe_fact：先让 worker 把结论写成事实，再来证明）"), nil
			}
			node, err := t.ts.GetNodeWithSources(ev)
			if err != nil || node == nil || (node.Kind != db.KindFact && node.Kind != db.KindFinding) {
				return actool.Errorf("goal_met: evidence_id 必须是本任务（或直接关联任务）的事实/漏洞节点"), nil
			}
			// goal_met 直接结束整个任务，同样要过清理门。
			if msg := t.cleanupGate(); msg != "" {
				return actool.Errorf(msg), nil
			}
			t.GoalMet = true
			t.Reason = a.Reason
			return actool.Text(fmt.Sprintf("acknowledged: goal marked met (evidence #%d)", ev)), nil
		})
}

// --- worker write tools ---

// severityNeedsEvidence 判断这一档严重度是否必须随发现提交证据。
// 只有 critical/high 要求：这两档会进报告头条、也最容易被误报，而 medium/low
// 强行要证据只会把模型推向编造。大小写与空格不敏感——模型常写 "High"。
func severityNeedsEvidence(sev string) bool {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical", "high":
		return true
	}
	return false
}

func (t *ToolSet) addFinding() actool.CoreTool {
	return writeTool("report_finding", "记录确认的漏洞，用 evidence 提供命令输出、日志等可验证证据。任务上下文传当前 intent_id。返回的 finding_id 是独立漏洞记录 ID，finding_node_id 是探索节点 ID（第一行保留该节点编号）。", obj(map[string]any{
		"vulnclass": str("漏洞类别"), "name": str("漏洞名称"), "severity": str("critical|high|medium|low"), "summary": str("发现摘要"),
		"intent_id": idp("当前任务的意图 id"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "受影响资产 id"},
		"evidence":         str("证据/PoC 文本。**severity 为 critical/high 时必填**（触发命令/请求 + 关键输出，最好带正常对照）；medium/low 可省略或在其中写清推断依据"),
		"confidence":       map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}, "description": "置信度诚实标定（必填）：high=可复现的触发链（有对照请求、可重放）；medium=直接观察到强异常但未构造完整对照/未确证可利用；low=据指纹/版本/现象推断、未直接触发。不要虚标——标不高就用 close_candidate 记存疑。"},
		"evidence_hint_id": idp("可选：本任务中对应此漏洞的提示节点 ID，自动携带其结构化 traffic_refs；不能引用继承提示或其他漏洞的提示"),
		"traffic_refs": map[string]any{"type": "array", "description": "可选；HTTP/HTTPS 漏洞先检索并逐条核实请求/响应确实支持漏洞结论，再按复现顺序填写真实 ID。TCP 等非 HTTP 漏洞、未采集或找不到确切记录时省略或传 []，不阻止上报；可在 evidence 说明原因并提供其他可验证证据。不要猜测 ID、按域名/时间推定关联或仅为补包重复探测。用途 baseline 正常对照 / proof 漏洞证明 / verification 补充验证 / supporting 辅助证据。高危/严重漏洞尽量给出【成对证据】：proof（触发漏洞的请求）+ baseline（同接口的正常对照请求）——没有对照时在 note 里说明原因；证据不足的漏洞会在报告里单独列为「待核对」。",
			"items": obj(map[string]any{"traffic_id": str("traffic_search 返回的真实流量 ID"), "role": map[string]any{"type": "string", "enum": []string{"baseline", "proof", "verification", "supporting"}}, "note": str("该流量如何支持漏洞结论")}, "traffic_id")},
	}, "vulnclass", "severity", "summary", "confidence"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		var a struct {
			VulnClass, Name, Severity, Summary, Evidence, Confidence string
			IntentID                                                 json.RawMessage   `json:"intent_id"`
			AssetIDs                                                 []json.RawMessage `json:"asset_ids"`
			TrafficRefs                                              []db.TrafficRef   `json:"traffic_refs"`
			EvidenceHintID                                           json.RawMessage   `json:"evidence_hint_id"`
		}
		if err := json.Unmarshal(in, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.ts == nil {
			return actool.Errorf("report_finding 需要任务上下文；平台对话请通过 add_task_hint 向对应任务交接漏洞，并在提示中携带已有的 traffic_refs，由任务 Agent 登记。已登记漏洞可用 bind_finding_traffic 补绑。"), nil
		}
		if !db.ValidConfidence(a.Confidence) {
			return actool.Errorf("confidence 取值必须是 high|medium|low（诚实标定；缺省=未标定，报告会点名）"), nil
		}
		// 严重度是报告里排序与统计用的键，不是自由文本：写 "High" / "高" / "严重" 时
		// 统计表里没有这一行、排序又会因为查不到而把它当最严重——两头都是错的数据。
		severity := strings.ToLower(strings.TrimSpace(a.Severity))
		if !db.ValidSeverity(severity) {
			return actool.Errorf(fmt.Sprintf("severity 取值必须是 critical|high|medium|low（收到 %q）。"+
				"它决定报告里的排序与统计，不接受别名或中文", a.Severity)), nil
		}
		// 证据分级：critical/high 必须有 evidence。这两档会排在报告最前、也最先被客户
		// 拿去复验——一条"看着像"的高危没有证据，整份报告的可信度都会被拖下水。
		// medium/low 保持可选：逼着给证据只会换来一句编出来的话，比空着更糟。
		if severityNeedsEvidence(severity) && strings.TrimSpace(a.Evidence) == "" {
			return actool.Errorf(fmt.Sprintf("severity=%s 的漏洞必须给 evidence（触发命令/请求与关键输出；有正常对照更好）。"+
				"若目前只有推断、还拿不出可复现的证据，把 severity 降到 medium/low 并在 evidence 里写清依据，"+
				"或先用 close_candidate 记存疑——不要用高危 + 空证据去占报告的头条", a.Severity)), nil
		}
		// Auto-binding off: ignore the evidence params instead of rejecting the call.
		// stripTrafficParameters already removes them from the advertised schema, but
		// models routinely emit fields anyway — failing here would discard a confirmed
		// finding over a stray parameter. The success path below reports evidence_status
		// "not_bound" with the "已关闭，可在页面人工关联" note, which is what the caller needs.
		if !findingTrafficBindingEnabled() {
			a.TrafficRefs, a.EvidenceHintID = nil, nil
		}
		if len(a.EvidenceHintID) > 0 && pid(a.EvidenceHintID) <= 0 {
			return actool.Errorf("evidence_hint_id 必须为有效的提示节点 ID；无交接提示时省略"), nil
		}
		refs, err := t.findingRefsFromHint(pid(a.EvidenceHintID), a.TrafficRefs)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		intentID, err := t.writeIntent(a.IntentID)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		assetIDs := pidList(a.AssetIDs)
		// 重复登记检查：同任务、同 vulnclass、资产有交集、摘要高度相似 → 拒收并指回
		// 已有条目。重复 finding 会让报告里同一条漏洞出现两次、findings_total 虚高，
		// planner 误判目标达成。确实不同请把差异写进 summary。
		if t.ts != nil {
			if prior, perr := t.ts.ActiveFindingsByClass(a.VulnClass, 50); perr == nil {
				if dupID, dupSummary := similarFinding(a.Summary, assetIDs, prior); dupID > 0 {
					return actool.Errorf(fmt.Sprintf("已存在高度相似的漏洞 #%d（%s）：同类别、同资产、摘要相似，判定为重复登记。"+
						"不要重复创建：确有实质差异请把差异写进 summary 再报；确属同一漏洞但描述不同，请让规划者/主 Agent 用 merge_findings 合并。", dupID, firstLine(dupSummary, 80))), nil
				}
			}
		}
		input := db.RecordFindingInput{TaskID: t.taskID, ExplorationID: t.ts.ID(), IntentID: intentID, VulnClass: a.VulnClass, Name: a.Name, Severity: severity, Summary: a.Summary, Evidence: a.Evidence, Worker: t.worker, Confidence: a.Confidence, AssetIDs: assetIDs}
		var recorded *db.RecordedFinding
		if t.findingRecorder != nil {
			recorded, err = t.findingRecorder.Record(ctx, input, refs)
		} else if len(refs) > 0 {
			return actool.Errorf("流量证据存储不可用；未登记漏洞"), nil
		} else {
			recorded, err = t.ts.RecordFinding(ctx, input)
		}
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.notifyFinding != nil {
			iid := input.IntentID
			if iid <= 0 {
				iid = t.ownerNode
			}
			t.notifyFinding(iid, a.Summary)
		} else if t.notify != nil {
			t.notify()
		}
		t.writes.Findings++
		t.writes.noteValidationAssets(input.AssetIDs)
		// Keep the first line's node-ID contract for existing reporter triggers.
		for i := range recorded.Traffic.Bindings {
			recorded.Traffic.Bindings[i].Snapshot.ReqHead = ""
			recorded.Traffic.Bindings[i].Snapshot.RespHead = ""
		}
		result := struct {
			*db.RecordedFinding
			EvidenceStatus string `json:"evidence_status"`
			EvidenceNote   string `json:"evidence_note,omitempty"`
		}{RecordedFinding: recorded, EvidenceStatus: "bound"}
		if len(recorded.Traffic.Bindings) == 0 {
			result.EvidenceStatus = "not_bound"
			result.EvidenceNote = "漏洞已保存，未绑定流量。TCP/无包情形可正常继续；若已有核实的 HTTP 流量，请用可用的 bind_finding_traffic 或漏洞页面补绑，再完成证据交接。不要重复创建漏洞。"
			if !findingTrafficBindingEnabled() {
				result.EvidenceNote = "漏洞已保存。Agent 自动绑定流量已关闭，可在页面人工关联流量。"
			}
		}
		raw, _ := json.Marshal(result)
		return actool.Text(fmt.Sprintf("finding recorded: %d\n%s", recorded.NodeID, raw)), nil
	})
}

// closeCandidate 闭合一条【没成为漏洞】的候选（strix 三态闭合纪律）：候选要么经
// report_finding 成为 confirmed，要么在这里落到 ruled_out（带反证：点名控制点）或
// open_proof_gap（写清缺什么）——不得静默丢弃。"没找到控制点 ≠ 安全"。
func (t *ToolSet) closeCandidate() actool.CoreTool {
	return writeTool("close_candidate", "闭合一条【未成为漏洞】的候选，留下反证或缺口（三态纪律：没找到控制点 ≠ 安全，禁止静默丢弃候选）。ruled_out=已排除，必须用 counterevidence 点名阻断它的具体控制点；open_proof_gap=存疑待补证，必须用 proof_gap 写清缺什么。确认漏洞请用 report_finding。", obj(map[string]any{
		"candidate":       str("候选一句话（假设的漏洞点，如“/login 的 user 参数疑似 SQLi”）"),
		"outcome":         map[string]any{"type": "string", "enum": []string{"ruled_out", "open_proof_gap"}, "description": "ruled_out 已排除（有反证）/ open_proof_gap 存疑待补证（不默关）"},
		"counterevidence": str("outcome=ruled_out 必填：点名阻断该候选的具体控制点/机制（如参数化查询、WAF 规则、鉴权校验），以及它为何不在攻击者路径上"),
		"proof_gap":       str("outcome=open_proof_gap 必填：缺什么信息、为什么本次闭合不了、下一步怎么补"),
		"confidence":      map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}, "description": "必填：high=反证路径已实际验证 / medium=直接观察到阻断现象但未完整复现 / low=据版本/指纹/经验推断"},
		"vulnclass":       str("候选的漏洞类别（如 sqli/xss/idor）"),
		"severity":        str("若成立时的严重度 critical|high|medium|low（可选）"),
		"intent_id":       idp("当前任务的意图 id"),
		"asset_ids":       map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "候选涉及的资产 id（可选）"},
	}, "candidate", "outcome", "confidence"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		// 显式 json tag：Go 的字段名匹配不认下划线（proof_gap ≠ ProofGap），
		// 漏标会让该参数永远绑定不上（静默失效；验收清单抓过这个）。
		var a struct {
			Candidate       string            `json:"candidate"`
			Outcome         string            `json:"outcome"`
			Counterevidence string            `json:"counterevidence"`
			ProofGap        string            `json:"proof_gap"`
			Confidence      string            `json:"confidence"`
			VulnClass       string            `json:"vulnclass"`
			Severity        string            `json:"severity"`
			IntentID        json.RawMessage   `json:"intent_id"`
			AssetIDs        []json.RawMessage `json:"asset_ids"`
		}
		if err := json.Unmarshal(in, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.ts == nil {
			return actool.Errorf("close_candidate 需要任务上下文；平台对话请通过 add_task_hint 向对应任务交接候选结论与反证，由任务 Agent 登记。"), nil
		}
		if strings.TrimSpace(a.Candidate) == "" {
			return actool.Errorf("candidate 不能为空：一句话写清被闭合的候选是什么"), nil
		}
		intentID, err := t.writeIntent(a.IntentID)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		rec, err := t.ts.RecordCandidateClosure(ctx, db.CandidateClosureInput{
			TaskID: t.taskID, IntentID: intentID,
			VulnClass: a.VulnClass, Name: a.Candidate, Severity: a.Severity, Summary: a.Candidate, Worker: t.worker,
			Outcome: a.Outcome, Counterevidence: a.Counterevidence, ProofGap: a.ProofGap, Confidence: a.Confidence,
			AssetIDs: pidList(a.AssetIDs),
		})
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.notifyFinding != nil {
			t.notifyFinding(intentID, a.Candidate)
		} else if t.notify != nil {
			t.notify()
		}
		t.writes.Findings++
		t.writes.noteValidationAssets(pidList(a.AssetIDs))
		raw, _ := json.Marshal(rec)
		return actool.Text(fmt.Sprintf("candidate closed: %s\n%s", a.Outcome, raw)), nil
	})
}

// recordFact writes a general exploration RESULT/conclusion (not a vuln, not a
// new asset) into the EXPLORATION graph, chained to the intent that produced it.
// This is the home for observations and — importantly — negative results
// ("port closed", "param not injectable", "no login found"). Such conclusions
// must NOT be stuffed into the asset graph via upsert_asset.
// factItem 是 record_fact 批量/单条的一条事实。
type factItem struct {
	Summary     string            `json:"summary"`
	Detail      string            `json:"detail"`
	Evidence    string            `json:"evidence"`    // 一行关键证据（命令+关键输出行），支撑结论、便于事后核对
	Confidence  string            `json:"confidence"`  // observed（直接看到）| inferred（据现象推断）
	Polarity    string            `json:"polarity"`    // positive（正向观察）| negative（否定收尾）| inconclusive（存疑）
	Enables     []string          `json:"enables"`     // credential|foothold|access|info|surface
	Limitations string            `json:"limitations"` // 这条结论的适用边界/前提
	Leads       []leadItem        `json:"leads"`       // 本意图之外值得深挖的线索（交给 planner）
	IntentID    json.RawMessage   `json:"intent_id"`
	AssetIDs    []json.RawMessage `json:"asset_ids"`
}

// leadItem 是一条「本意图之外」的线索。线索不是结论：它没有证据门槛，
// 但必须有具体指向（入口/参数/行为），由 planner 决定是否转成意图。
type leadItem struct {
	Text     string            `json:"text"`
	AssetIDs []json.RawMessage `json:"asset_ids"`
}

const (
	maxLeadsPerFact   = 8
	maxLeadRunes      = 300
	maxLeadsPerBatch  = 32
	factPolarityPos   = "positive"
	factPolarityNeg   = "negative"
	factPolarityMaybe = "inconclusive"
)

// normalizeFactPolarity 规范化 polarity；缺省按否定关键词与 confidence 兜底。
func normalizeFactPolarity(raw, summary, confidence string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case factPolarityPos:
		return factPolarityPos
	case factPolarityNeg:
		return factPolarityNeg
	case factPolarityMaybe:
		return factPolarityMaybe
	}
	low := strings.ToLower(summary)
	for _, w := range []string{"未发现", "不存在", "不可", "无法", "失败", "拒绝", "关闭", "均无", "全 404", "全部 404", "not found", "no "} {
		if strings.Contains(low, w) {
			return factPolarityNeg
		}
	}
	if strings.EqualFold(strings.TrimSpace(confidence), "inferred") {
		return factPolarityMaybe
	}
	return factPolarityPos
}

// normalizeEnables 只保留词表内的能力标签。
func normalizeEnables(raw []string) []string {
	allowed := map[string]bool{"credential": true, "foothold": true, "access": true, "info": true, "surface": true}
	var out []string
	seen := map[string]bool{}
	for _, v := range raw {
		v = strings.ToLower(strings.TrimSpace(v))
		if allowed[v] && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// cleanLeads 清洗线索：去空、截断、去重、限量。
func cleanLeads(raw []leadItem) []map[string]any {
	var out []map[string]any
	seen := map[string]bool{}
	for _, l := range raw {
		text := strings.TrimSpace(l.Text)
		if text == "" {
			continue
		}
		if r := []rune(text); len(r) > maxLeadRunes {
			text = string(r[:maxLeadRunes])
		}
		key := strings.ToLower(strings.Join(strings.Fields(text), " "))
		if seen[key] {
			continue
		}
		seen[key] = true
		item := map[string]any{"text": text}
		if ids := pidList(l.AssetIDs); len(ids) > 0 {
			item["asset_ids"] = ids
		}
		out = append(out, item)
		if len(out) >= maxLeadsPerFact {
			break
		}
	}
	return out
}

// leadID 是 lead 的稳定标识：L<事实id>-<下标>。payload 只存 text/asset_ids，
// id 在读取时按同一下标生成，因此不需要在落库后回写。
func leadID(factID int64, index int) string { return fmt.Sprintf("L%d-%d", factID, index) }

// parseLeadID 解析 L<factID>-<index>。
func parseLeadID(raw string) (int64, int, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 4 || (raw[0] != 'L' && raw[0] != 'l') {
		return 0, 0, false
	}
	rest := raw[1:]
	i := strings.IndexByte(rest, '-')
	if i <= 0 || i == len(rest)-1 {
		return 0, 0, false
	}
	fid, err := strconv.ParseInt(rest[:i], 10, 64)
	if err != nil || fid <= 0 {
		return 0, 0, false
	}
	idx, err := strconv.Atoi(rest[i+1:])
	if err != nil || idx < 0 {
		return 0, 0, false
	}
	return fid, idx, true
}

// validateLeadIDs 校验 converts_lead_ids：必须指向本任务某条事实里真实存在的 lead，
// 且尚未被 dismiss。返回去重后的合法 id。
func (t *ToolSet) validateLeadIDs(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range raw {
		id := strings.TrimSpace(item)
		if id == "" {
			continue
		}
		fid, idx, ok := parseLeadID(id)
		if !ok {
			return nil, fmt.Errorf("converts_lead_ids 里的 %q 不是合法 lead id（格式 L<事实id>-<序号>，见 graph_overview 的 recent_leads）", id)
		}
		n, err := t.ts.GetNode(fid)
		if err != nil || n == nil || n.Kind != db.KindFact {
			return nil, fmt.Errorf("converts_lead_ids 里的 %q 指向的事实不存在于本任务", id)
		}
		var p struct {
			Leads     []map[string]any `json:"leads"`
			Dismissed []string         `json:"dismissed_leads"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if idx >= len(p.Leads) {
			return nil, fmt.Errorf("converts_lead_ids 里的 %q 超出该事实的线索条数（共 %d 条）", id, len(p.Leads))
		}
		for _, d := range p.Dismissed {
			if strings.EqualFold(strings.TrimSpace(d), id) {
				return nil, fmt.Errorf("converts_lead_ids 里的 %q 已被 dismiss，不能再转意图", id)
			}
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// recordOneFact 写一条 fact 节点并连到意图（intent→yields→fact）。defaultIntent 为
// 批量时的默认意图（本条未给 intent_id 时用）。
func (t *ToolSet) recordOneFact(it factItem, defaultIntent int64) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary 不能为空")
	}
	payload := map[string]any{"summary": it.Summary}
	if it.Detail != "" {
		payload["detail"] = it.Detail
	}
	if e := strings.TrimSpace(it.Evidence); e != "" {
		payload["evidence"] = e
	}
	if c := strings.TrimSpace(it.Confidence); c != "" {
		payload["confidence"] = c
	}
	// 事实语义：polarity 决定这条事实还能不能开出下文（否定收尾不催办），
	// enables 是「这条事实给了什么能力」，leads 是本意图之外的线索。
	payload["polarity"] = normalizeFactPolarity(it.Polarity, it.Summary, it.Confidence)
	if en := normalizeEnables(it.Enables); len(en) > 0 {
		payload["enables"] = en
	}
	if lim := strings.TrimSpace(it.Limitations); lim != "" {
		payload["limitations"] = lim
	}
	if leads := cleanLeads(it.Leads); len(leads) > 0 {
		payload["leads"] = leads
	}
	intent := pid(it.IntentID)
	if intent <= 0 {
		intent = defaultIntent
	}
	intent, err := t.writeIntentID(intent)
	if err != nil {
		return 0, err
	}
	if intent > 0 {
		node, err := t.ts.GetNode(intent)
		if err != nil || node == nil || node.Kind != db.KindIntent {
			return 0, fmt.Errorf("intent_id 必须是本任务的意图（关联任务意图只读）")
		}
	}
	// a fact is its OWN node kind (distinct from a vuln finding).
	id, err := t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, pidList(it.AssetIDs))
	if err != nil {
		return 0, err
	}
	if intent > 0 {
		_ = t.ts.Link(intent, db.RelYields, id) // chain: intent -> fact
	}
	t.writes.Facts++
	t.writes.noteValidationAssets(pidList(it.AssetIDs))
	return id, nil
}

// concludeIntent 让 worker 对领到的意图声明【语义终态】：negative（探针确实发出且有回包，
// 回包明确否定该假设）/ out-of-scope（该方向超出任务授权范围）/ blocked（平台或外部
// 因素导致本意图无法执行，如出口代理不可用、平台故障、目标整体不可达）。引擎在正常
// 完成时以语义为准；失败/超时收尾仍记 blocked/exhausted——没跑成不是结论。
func (t *ToolSet) concludeIntent() actool.CoreTool {
	return t.writeExpTool("conclude_intent",
		"声明你领到的意图的【语义终态】——只在该方向真正得出结论时调用一次：\n"+
			"  · negative：探针确实发出且有回包，回包【明确否定】了该假设（note 写清是什么证据否定）。\n"+
			"  · out-of-scope：该方向超出了任务授权范围（note 写清越界点）。\n"+
			"  · blocked：平台或外部因素导致本意图**无法执行**——出口代理不可用、平台故障、目标整站不可达等（note 写清是哪一种、证据是什么）。这是「外部阻断」，不是方向结论，也不是「没打进去」的委婉说法。\n"+
			"正常完成时意图将以你声明的语义收尾；若因失败/超时收尾仍是 blocked/exhausted——没测成不得用本工具包装成结论。",
		obj(map[string]any{
			"outcome": str("negative（探针实证否定）| out-of-scope（超出授权范围）| blocked（平台/外部阻断，无法执行）"),
			"note":    str("一句话原因：negative 写清是什么证据否定；out-of-scope 写清越界点；blocked 写清外部阻断的类型与证据"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Outcome string `json:"outcome"`
				Note    string `json:"note"`
			}
			_ = json.Unmarshal(in, &a)
			outcome := strings.TrimSpace(a.Outcome)
			if outcome != "negative" && outcome != "out-of-scope" && outcome != "blocked" {
				return actool.Errorf("conclude_intent: outcome 必须是 negative / out-of-scope / blocked"), nil
			}
			note := strings.TrimSpace(a.Note)
			if outcome == "blocked" && note == "" {
				// blocked 会让覆盖矩阵把它记成「未测成」（不是已测），必须留下
				// 外部阻断的具体原因，否则就是拿它给"没干活"开脱。
				return actool.Errorf("conclude_intent: outcome=blocked 必须在 note 里写清外部阻断的类型与证据（如「平台出口代理不可用，curl 全部 exit 7」）"), nil
			}
			if t.ownerNode <= 0 {
				return actool.Errorf("conclude_intent: 当前没有运行中的意图（ownerNode 未设置）"), nil
			}
			// P13 negative 证据门：negative 是"探针确实发出且回包否定"的断言，不是
			// "我没打进去"的委婉说法。本意图零真实执行/零工具调用记录 → 拒绝，并给出
			// 可操作的分岔：没测成如实说没测成，探过就补上探针证据（工具调用/长任务执行）。
			if outcome == "negative" {
				ran := int64(0)
				if t.execService != nil {
					ran, _ = t.execService.CountRanForIntent(t.ownerNode)
				}
				acts, _ := t.ts.CountNodeToolActivity(t.ownerNode)
				if ran == 0 && acts == 0 {
					return actool.Errorf(fmt.Sprintf("conclude_intent: 意图 #%d 没有任何工具调用/执行记录，不能声明 negative——"+
						"negative 的含义是【探针确实发出且回包明确否定】。确实没测成就如实说明没测成（让意图以 blocked/exhausted 收尾）；"+
						"确实探过就先给出探针证据（真实工具调用，或长任务用 run_execution 起、wait_execution 收），再声明 negative。", t.ownerNode)), nil
				}
			}
			if err := t.ts.SetIntentSemanticOutcome(t.ownerNode, outcome, note); err != nil {
				return actool.Errorf("conclude_intent: " + err.Error()), nil
			}
			msg := fmt.Sprintf("已记录意图 #%d 的语义终态 %s：正常完成时以 %s 收尾；若因失败/超时收尾仍记 blocked/exhausted。", t.ownerNode, outcome, outcome)
			if t.writes.Total() == 0 {
				msg = "注意：本次 run 还没有任何写回（record_fact / report_finding / report_credential / report_artifact / leads）。如果已经拿到结论，先写回再收尾——只留在本地文件里的东西 planner 看不到。\n" + msg
			}
			return actool.Text(msg), nil
		},
	)
}

// recordProbeFact 是 planner 专用的「开局侦察事实」写入口。
//
// 为什么单独开一个而不是把 record_fact 绑给 planner：planner 不执行攻击、也不该把
// 推测写成事实；但 openingProbeBlock 要求它先做只读摸底，这些观察（响应头、跳转、
// 版本、路径）如果不入图，后面的意图只能挂到目标上，血缘变平、其它 worker 也复用
// 不到。这里用 evidence 必填 + source=opening_probe 把口子收窄到「本轮真实看到的
// 侦察观察」，仍然不允许 planner 写漏洞结论或凭据。
func (t *ToolSet) recordProbeFact() actool.CoreTool {
	return t.writeExpTool("record_probe_fact",
		"记录【开局摸底】中真实看到的侦察观察（响应头/跳转/版本/路径/指纹），写入探索图并挂到任务起点。\n"+
			"只给 planner 用，且 evidence 必填：写清命令 + 最能证明的那一两行输出；没有 evidence 的观察会被拒收。\n"+
			"这不是通用 record_fact：不要写推测、漏洞结论、凭据或战果——那些交给 worker 用 report_finding / report_credential。\n"+
			"返回的 fact id 直接放进 add_intent 的 parent_ids，意图就会显示为「由事实派生」而不是从目标凭空长出来。",
		obj(map[string]any{
			"facts":      map[string]any{"type": "array", "description": "【多条观察时用】数组，元素字段同下方顶层字段；一次最多 8 条。", "items": map[string]any{"type": "object"}},
			"summary":    str("一句话观察结论（如：/admin 302 到 /login?ReturnUrl=/admin）"),
			"detail":     str("相关细节：响应头、版本、路径、跳转链等，可合并多个观察"),
			"evidence":   str("【必填】一行关键证据：命令 + 最能证明结论的 1-2 行输出"),
			"confidence": str("observed（输出里直接看到，默认）| inferred（据现象推断）"),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "相关资产 id（可选）"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.worker != "planner" {
				return actool.Errorf("record_probe_fact 只供 planner 记录开局侦察观察；执行层请用 record_fact / report_finding"), nil
			}
			var a struct {
				Facts []factItem `json:"facts"`
				factItem
			}
			_ = json.Unmarshal(in, &a)
			items := a.Facts
			if len(items) == 0 {
				items = []factItem{a.factItem}
			}
			if len(items) > 8 {
				items = items[:8]
			}
			origin, _ := t.ts.OriginFactID()
			ids := make([]int64, 0, len(items))
			for i, it := range items {
				summary := strings.TrimSpace(it.Summary)
				if summary == "" {
					return actool.Errorf(fmt.Sprintf("facts[%d].summary 不能为空", i)), nil
				}
				evidence := strings.TrimSpace(it.Evidence)
				if evidence == "" {
					return actool.Errorf(fmt.Sprintf("facts[%d].evidence 必填：开局侦察事实必须留下命令与关键输出，不能凭记忆写", i)), nil
				}
				conf := strings.ToLower(strings.TrimSpace(it.Confidence))
				if conf == "" {
					conf = "observed"
				}
				if conf != "observed" && conf != "inferred" {
					return actool.Errorf(fmt.Sprintf("facts[%d].confidence 只能是 observed 或 inferred", i)), nil
				}
				payload := map[string]any{
					"summary": summary, "evidence": evidence, "confidence": conf,
					"polarity": factPolarityPos, "source": "opening_probe",
				}
				if d := strings.TrimSpace(it.Detail); d != "" {
					payload["detail"] = d
				}
				id, err := t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, pidList(it.AssetIDs))
				if err != nil {
					return actool.Errorf(fmt.Sprintf("facts[%d] 写入失败：%v", i, err)), nil
				}
				if origin > 0 && origin != id {
					_ = t.ts.Link(origin, db.RelYields, id)
				}
				ids = append(ids, id)
			}
			return jsonResult(map[string]any{"ids": ids, "origin": origin})
		})
}

func (t *ToolSet) recordFact() actool.CoreTool {
	return t.writeExpTool("record_fact", "把探索【事实/结论】写入探索图，连到产生它的意图（intent_id）。用于记录探索结果——包括指纹/枚举等【正向结论】，和'端口关闭'/'参数不可注入'/'未发现登录入口'等【否定结论】。\n"+
		"⚠️一次探索的多个观察要【汇总成一条事实】，不要拆成多条，可以合并成一条事实的就尽量用一条事实表示：summary=对本次结论的总结性一句话，detail=相关细节（可含多个具体项）。例：指纹意图→一条事实 {summary:'识别了 X 站点的技术栈与响应特征', detail:'nginx 1.25 / Vue3 / 200 / title=.. / body_len=..'}，而不是状态码、指纹、标题各记一条。一条意图通常只产出一条事实，拆太碎会让图谱无限膨胀。\n"+
		"★facts 数组用于一次写多条【彼此不同】的结论（每条可省略 intent_id，默认用顶层 intent_id）。返回 ids 数组，与 facts 等长同序。\n"+
		"⚠️只写你在工具输出里【真实看到】的结论，不要脑补。evidence 与 confidence 用来防止不准确的结论污染图谱：\n"+
		"  · evidence=支撑本结论的【一行】关键证据（命令+最能证明的那一两行输出），**务必简洁**——细节已在 detail，这里不要再粘大段输出。\n"+
		"  · confidence=observed（输出里直接看到）| inferred（据现象推断）。\n"+
		"  · **否定类结论**（不可注入/端口关闭/未发现入口等）只写\"观察 + 试探性读法\"——陈述你实际看到什么，方向是否放弃由规划者综合全局定；务必给 evidence，手段没穷尽或证据弱（含只探一次、看起来像）标 inferred，确已穷尽且直接看到才标 observed。",
		obj(map[string]any{
			"facts":       map[string]any{"type": "array", "description": "【有多条不同结论时用】事实数组，元素字段同下方顶层字段（summary/detail/evidence/confidence/polarity/enables/leads/intent_id/asset_ids）；省略 intent_id 则用顶层 intent_id。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"summary":     str("对本次探索结论的【总结性一句话】（是对 detail 的概括）"),
			"intent_id":   idp("产生本事实的意图 id（你领到的意图；批量时作为各条默认）"),
			"detail":      str("本事实的相关细节：把这次探索的多个观察事实都写进这里"),
			"evidence":    str("【一行】关键证据：命令 + 最能证明结论的那一两行输出。务必简洁，不要粘大段输出（细节放 detail）。"),
			"confidence":  str("observed（输出里直接看到）| inferred（据现象推断）。否定结论务必如实标注。"),
			"polarity":    str("【事实语义】positive（正向观察，能开出下文）| negative（否定收尾：不可注入/端口关闭/未发现入口等）| inconclusive（存疑）。缺省按 summary 关键词与 confidence 推断。"),
			"enables":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "【能力标签】这条事实给了什么：credential | foothold | access | info | surface（可多选，没有就省略）"},
			"limitations": str("这条结论的适用边界/前提（可选）：在什么条件下不成立、还缺什么验证。"),
			"leads":       map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "【本意图之外的线索】数组，元素 {text, asset_ids?}：看到的其它入口/参数/行为/资产，值得 planner 下一轮跟进。最多 8 条、每条 ≤300 字；线索不是结论，不需要 evidence。不要把线索塞进 summary。"},
			"asset_ids":   map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "相关资产 id（可选，0/1/多个）：该事实涉及哪些资产"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Facts    []factItem `json:"facts"`
				factItem            // 单条模式 + 批量默认 intent_id
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Facts) > 0
			items := a.Facts
			if !batch {
				items = []factItem{a.factItem}
			}
			// 一批事实的线索总量封顶，避免一次写回把态势撑爆。
			leadBudget := maxLeadsPerBatch
			for i := range items {
				if leadBudget <= 0 {
					items[i].Leads = nil
					continue
				}
				if len(items[i].Leads) > leadBudget {
					items[i].Leads = items[i].Leads[:leadBudget]
				}
				leadBudget -= len(items[i].Leads)
			}
			defaultIntent := pid(a.factItem.IntentID) // 顶层 intent_id = 批量默认

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.recordOneFact(it, defaultIntent)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("fact recorded: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

// =====================================================================
// 战果（据点 / 凭据）
// =====================================================================
//
// 利用成功之后的产物此前无处安放：worker 只能把「拿到了 shell」压成一段 fact 文本，于是
// 规划层看不到「我有哪些据点、能横到哪、握着哪些凭据」——每轮都从外面重新打一遍，报告里的
// 「提权/横向」也只能靠文案。这里给两者各自的 kind，复用既有的节点 / 锚点 / 边机制
// （不新造存储）：graph_overview 与报告按 kind 取用即可。

// lootBase 是据点与凭据共有的字段，形状照 fact：一句话结论 + 细节 + 一行证据 + 置信度，
// 外加归属的意图与资产。
type lootBase struct {
	Summary    string            `json:"summary"`
	Detail     string            `json:"detail"`
	Evidence   string            `json:"evidence"`
	Confidence string            `json:"confidence"`
	IntentID   json.RawMessage   `json:"intent_id"`
	AssetIDs   []json.RawMessage `json:"asset_ids"`
	// State 是节点的初始状态；空 = 用该 kind 的默认（战果一律 StateLootActive）。
	// 只有遗留物会用到它——"落了但没确认还在不在"必须能如实记成 unknown。
	State string `json:"state"`
}

type footholdItem struct {
	lootBase
	Access    string `json:"access"`    // 怎么进去的：反弹 shell / SSH / RDP / webshell
	Privilege string `json:"privilege"` // 进去时的权限：user / root / SYSTEM
	// 结构化连法（可选）：有它，平台才能再进去（foothold_exec），也才能对"连到哪"
	// 做授权范围校验。没有它，据点就只是一段自由文本。
	Protocol     string          `json:"protocol"`
	Host         string          `json:"host"`
	Port         int             `json:"port"`
	URL          string          `json:"url"`
	Param        string          `json:"param"`
	CredentialID json.RawMessage `json:"credential_id"`
}

type credentialItem struct {
	lootBase
	Kind     string `json:"kind"`     // password / hash / key / token
	Username string `json:"username"` // 凭据对应的账号（哈希/密钥可能没有）
	Secret   string `json:"secret"`   // 凭据本体
	// Targets 是「这条凭据能登哪」（P36）。每条可带自己的 verified + evidence：
	// 验证是**对某条目标**的结论——同一条口令在 A 机器能登、B 机器不能很常见。
	Targets []db.CredentialTarget `json:"targets"`
}

// normalizeTargets 清掉空目标、去重合并、稳定排序，并**挡住"声称验证过却没有证据"**。
// 与战果那套证据门同一条理由：verified 会被规划层当成既定事实（"这条能登，直接用它横向"），
// 一条编出来的验证会让后续意图跑空，而且看不出是哪里错的。
func normalizeTargets(in []db.CredentialTarget) ([]db.CredentialTarget, error) {
	var out []db.CredentialTarget
	for _, t := range in {
		t.Host = strings.TrimSpace(t.Host)
		t.Service = strings.TrimSpace(t.Service)
		t.Protocol = strings.TrimSpace(t.Protocol)
		t.Evidence = strings.TrimSpace(t.Evidence)
		t.Note = strings.TrimSpace(t.Note)
		if t.Host == "" {
			continue
		}
		if t.Verified && t.Evidence == "" {
			return nil, fmt.Errorf("目标 %s 标了 verified=true，但没有 evidence：验证过的结论必须留下那一行输出（它会被拿去规划「直接用这条凭据横向」）", t.Host)
		}
		out = db.MergeCredentialTarget(out, t)
	}
	db.SortCredentialTargets(out)
	return out, nil
}

// recordLoot 写一个战果节点并连到产生它的意图（intent→yields→战果，与 fact/finding 同链）。
//
// **故意比 record_fact 严**：summary、evidence、confidence 三者缺一不可。战果会被规划层
// 当作既定事实拿去规划「能从这条通路往下做什么」，一条编出来的 shell 会让后续所有意图跑空，
// 而且看不出是哪里错的——所以宁可拒收，也不要一条没有证据的战果入库。
// recordAssetCredential 把 insert_assets.auth 里的一条认证信息转成凭据节点。
// 与 report_credential 的区别：这里没有「读到它的命令与输出」，所以不设证据门，
// 只标 verification=unverified / confidence=low；按 kind+username+secret 去重，
// 跳过没有 secret 的条目（用户名、服务名这类不算凭据）。
func (t *ToolSet) recordAssetCredential(assetID int64, host string, port int, entry map[string]any) (int64, error) {
	if t.ts == nil || len(entry) == 0 {
		return 0, nil
	}
	field := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := entry[k]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		}
		return ""
	}
	secret := field("secret", "password", "pass", "passwd", "token", "key", "hash", "cookie")
	if secret == "" {
		return 0, nil
	}
	user := field("username", "user", "login", "account", "email")
	kind := strings.ToLower(field("kind", "type"))
	switch {
	case strings.Contains(kind, "token"), strings.Contains(kind, "cookie"):
		kind = "token"
	case strings.Contains(kind, "key"):
		kind = "key"
	case strings.Contains(kind, "hash"):
		kind = "hash"
	default:
		kind = "password"
	}
	service := field("service", "protocol", "proto")
	// 去重：同一 kind+username+secret 已经登记过就不重复造节点。
	if nodes, err := t.ts.ListByKind(db.KindCredential, 1000); err == nil {
		for _, n := range nodes {
			var p struct {
				Kind     string `json:"kind"`
				Username string `json:"username"`
				Secret   string `json:"secret"`
			}
			if json.Unmarshal(n.Payload, &p) != nil {
				continue
			}
			if p.Kind == kind && p.Username == user && p.Secret == secret {
				return n.ID, nil
			}
		}
	}
	label := host
	if label == "" {
		label = fmt.Sprintf("asset#%d", assetID)
	}
	payload := map[string]any{
		"summary":      fmt.Sprintf("资产登记时记录的凭据：%s @ %s", user, label),
		"kind":         kind,
		"username":     user,
		"secret":       secret,
		"verification": "unverified",
		"source":       "asset_registration",
		"confidence":   "low",
		"evidence":     "insert_assets auth 字段",
	}
	if host != "" {
		target := map[string]any{"host": host}
		if port > 0 {
			target["port"] = port
		}
		if service != "" {
			target["service"] = service
		}
		payload["targets"] = []map[string]any{target}
	}
	id, err := t.ts.AddNode(db.KindCredential, payload, 8, db.StateLootActive, t.worker, []int64{assetID})
	if err != nil {
		return 0, err
	}
	t.writes.Credentials++
	return id, nil
}

func (t *ToolSet) recordLoot(kind string, base lootBase, extra map[string]any) (int64, error) {
	summary := strings.TrimSpace(base.Summary)
	if summary == "" {
		return 0, fmt.Errorf("summary 不能为空")
	}
	if strings.TrimSpace(base.Evidence) == "" {
		return 0, fmt.Errorf("evidence 不能为空：战果会被规划层当作既定事实使用，没留下可核对的一行输出就不算数")
	}
	// 注意这里比 report_finding 严一档：db.ValidConfidence("") 是放行的（对漏洞而言空 =
	// 未标定，报告会点名），但战果的 schema 把 confidence 列进了 required——那就让它真的必填，
	// 而不是留一个只有模型才看得见的摆设。
	if strings.TrimSpace(base.Confidence) == "" {
		return 0, fmt.Errorf("confidence 不能为空：战果要标清你有多确定（high 亲手敲过 / medium 成功但未复验 / low 据现象推断）")
	}
	if !db.ValidConfidence(base.Confidence) {
		return 0, fmt.Errorf("confidence 取值必须是 high|medium|low（诚实标定）")
	}
	intent, err := t.writeIntent(base.IntentID)
	if err != nil {
		return 0, err
	}
	if intent > 0 {
		node, err := t.ts.GetNode(intent)
		if err != nil || node == nil || node.Kind != db.KindIntent {
			return 0, fmt.Errorf("intent_id 必须是本任务的意图（关联任务意图只读）")
		}
	}
	payload := map[string]any{"summary": summary}
	for k, v := range map[string]string{
		"detail": base.Detail, "evidence": base.Evidence, "confidence": base.Confidence,
	} {
		if s := strings.TrimSpace(v); s != "" {
			payload[k] = s
		}
	}
	for k, v := range extra {
		switch val := v.(type) {
		case string:
			if s := strings.TrimSpace(val); s != "" {
				payload[k] = s
			}
		case []db.CredentialTarget:
			if len(val) > 0 {
				payload[k] = val
			}
		case nil:
			// 空值不写：payload 里出现 "targets": [] 只会让读的人以为"确实一条都没有"。
		default:
			payload[k] = val
		}
	}
	// priority 8：frontier 只取 intent，这个值不影响调度；它只如实标出这是高价值产物。
	state := strings.TrimSpace(base.State)
	if state == "" {
		state = db.StateLootActive
	}
	id, err := t.ts.AddNode(kind, payload, 8, state, t.worker, pidList(base.AssetIDs))
	if err != nil {
		return 0, err
	}
	if intent > 0 {
		// 痕迹记的是「这条意图在目标上留下了什么」：用 leaves 而不是 yields。
		// yields 是产出物，会被意图删除的独占级联带走；痕迹是交付证据，必须留得住。
		rel := db.RelYields
		if kind == db.KindFootprint {
			rel = db.RelLeaves
		}
		_ = t.ts.Link(intent, rel, id)
	}
	return id, nil
}

// reportFoothold 记录一个真正拿到手的据点。worker 侧。
func (t *ToolSet) reportFoothold() actool.CoreTool {
	return t.writeExpTool("report_foothold",
		"记录一个【真正拿到手、现在还能用】的据点（反弹 shell / SSH / webshell / 会话），连到产生它的意图。"+
			"这是规划层判断「能往哪深打」的依据：**不记等于没拿到**——别的 worker 不知道这条通路存在，会从外面重新打一遍。"+
			"必须给 evidence（命令 + 能证明这条通路真的存在的输出）与 confidence；拿不出可核对的输出就不算数（工具会拒收）。"+
			"据点已失效时不要新记一条，改为用 record_fact 说明它掉了。",
		obj(map[string]any{
			"summary":       str("一句话：【哪台主机】上拿到了【什么权限】的【什么通路】"),
			"access":        str("怎么进去的：反弹 shell / SSH / RDP / webshell / 其他（如实写）"),
			"privilege":     str("进去时的权限：user / root / SYSTEM / 其他（如实标，别拔高）"),
			"detail":        str("细节：连法、端口、会话标识、后续可用的命令等"),
			"protocol":      str("**结构化连法**（可选，但能填就填）：ssh | webshell。填了它平台才能用 foothold_exec 再进去，也才能做授权范围校验；不填就只是自由文本，后续只能由 worker 自己重连。"),
			"host":          str("连哪：目标主机（域名或 IP）。protocol 非空时必填（webshell 可从 url 推导）"),
			"port":          intp("端口（ssh 默认 22）"),
			"url":           str("webshell 的入口 URL（protocol=webshell 时必填）"),
			"param":         str("webshell 的命令参数名（默认 cmd）"),
			"credential_id": idp("ssh 用哪条凭据节点（report_credential 返回的 id）：口令/私钥由它提供，平台执行时临时落盘、读完即删，明文不进命令与台账"),
			"evidence":      str("【一行】关键证据：命令 + 能证明这条通路真的存在的输出（必填）"),
			"confidence":    str("high（亲手敲过、当场有效）| medium（成功但未复验）| low（据现象推断、尚未确认）"),
			"intent_id":     idp("当前任务的意图 id（你领到的意图）"),
			"asset_ids":     map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "被攻陷的资产 id"},
		}, "summary", "evidence", "confidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a footholdItem
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			conn, err := t.normalizeFootholdAccess(a)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			conn["access"] = a.Access
			conn["privilege"] = a.Privilege
			id, err := t.recordLoot(db.KindFoothold, a.lootBase, conn)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			t.writes.Footholds++
			t.writes.noteValidationAssets(pidList(a.AssetIDs))
			// 拿到了机器，域内图也要知道：把对应的 AD 计算机对象标成 owned，
			// 否则攻击路径的起点永远停在"用户手标的"而不是"我们打下来的"。
			note := ""
			if host, _ := conn["host"].(string); host != "" {
				ids := t.markADOwned(host)
				for _, aid := range ids {
					_ = t.ts.Link(id, db.RelReaches, aid)
				}
				if len(ids) > 0 {
					note = fmt.Sprintf("\n已把 %d 个 AD 计算机对象标为 owned 并连上「可达」边（攻击路径可从它们出发）", len(ids))
				}
			}
			return actool.Text(fmt.Sprintf("foothold recorded: %d%s", id, note)), nil
		})
}

// verifyCredential 把一条已有凭据的**可利用性**补上：在哪条目标上实测登入成功过。
//
// 为什么单独一个工具而不是"再 report 一次"：重复 report 会多出一条凭据节点，图上就出现两条
// 一样的凭据、规划层还得自己判断哪条是真的。凭据的身份是它自己（那个节点），验证是对它的补充。
//
// 证据门与战果同一条：没有那一行输出，verified 就只是一个说法——而它会被拿去规划"直接用这条
// 凭据横向"，说不清的验证等于把后续意图往空里送。
func (t *ToolSet) verifyCredential() actool.CoreTool {
	return t.writeExpTool("verify_credential",
		"把一条【已记录】的凭据标成「在某条目标上实测登入成功」。用在：你真的拿它登进去了（ssh/mysql/rdp/smb… 成功）。"+
			"必须给 evidence（登录成功的命令 + 关键输出行）与 host；重复验证同一目标会自动合并，已验证过不会被抹掉。"+
			"**只在真的成功时用**：没成功就如实记 fact（「试过、不通」也是有价值的结论），不要标 verified。",
		obj(map[string]any{
			"credential_id": idp("要标记的凭据节点 id（report_credential 回执里的那个数）"),
			"host":          str("在哪台目标上登成功了（域名或 IP）"),
			"port":          intp("端口（可省略）"),
			"service":       str("什么服务：ssh / rdp / smb / winrm / mysql / http / 其他"),
			"protocol":      str("怎么连（可省略）"),
			"evidence":      str("【一行】登录成功的命令 + 关键输出行（必填）"),
			"note":          str("备注（可省略）"),
		}, "credential_id", "host", "evidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				CredentialID json.RawMessage `json:"credential_id"`
				Host         string          `json:"host"`
				Port         int             `json:"port"`
				Service      string          `json:"service"`
				Protocol     string          `json:"protocol"`
				Evidence     string          `json:"evidence"`
				Note         string          `json:"note"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := pid(a.CredentialID)
			if id <= 0 {
				return actool.Errorf("credential_id 必填：要标记哪一条凭据（report_credential 的回执里给了）"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindCredential {
				return actool.Errorf("credential_id 必须是本任务里的一条凭据节点"), nil
			}
			// 合并后整份写回：payload 里还有 secret/summary 等字段，只改 targets 而不动其余。
			var payload map[string]any
			if err := json.Unmarshal(node.Payload, &payload); err != nil || payload == nil {
				payload = map[string]any{}
			}
			cur := db.ParseCredential(node)
			merged := db.MergeCredentialTarget(cur.Targets, db.CredentialTarget{
				Host: a.Host, Port: a.Port, Service: a.Service, Protocol: a.Protocol,
				Verified: true, Evidence: strings.TrimSpace(a.Evidence), Note: a.Note,
			})
			db.SortCredentialTargets(merged)
			payload["targets"] = merged
			if err := t.ts.SetNodePayload(id, db.KindCredential, payload); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("credential %d verified on %s（指纹 %s）",
				id, merged[len(merged)-1].Label(), cur.Fingerprint())), nil
		})
}

// reportCredential 记录一条从据点上读到的凭据。worker 侧。
func (t *ToolSet) reportCredential() actool.CoreTool {
	return t.writeExpTool("report_credential",
		"记录一条【真实读到】的凭据（口令 / 哈希 / 密钥 / 令牌），连到产生它的意图。"+
			"凭据是横向与提权的抓手：记下来，规划层才会派「拿它去哪试」的意图；不记就只能重挖一遍。"+
			"必须给 evidence（读到它的那条命令与输出）与 confidence；**不要把猜测的、试出来的弱口令当成读到的凭据**——"+
			"后者用 record_fact 记结论。"+
			"\n\n**尽量给 targets（这条凭据能登哪）**：只写「我握着 wp_user 的口令」时，规划层只能派一条"+
			"「拿它去试试」的意图，试出来的结论又会丢掉；写清能登的目标（主机/端口/服务），"+
			"它就能直接派「用它登 X 做 Y」。已经实测登入成功的那条目标标 `verified: true` **并附上那一行输出**"+
			"（没证据的 verified 会被拒收）——verified 是比 confidence 更硬的东西：confidence 说的是"+
			"「我多确定读到的是真凭据」，verified 说的是「我用它真的进去了」。",
		obj(map[string]any{
			"summary":  str("一句话：【在哪台主机/哪个位置】读到了【谁】的【什么凭据】"),
			"kind":     str("password（明文口令）| hash（哈希）| key（私钥/密钥）| token（令牌/票据）"),
			"username": str("凭据对应的账号（哈希、密钥可能没有，可省略）"),
			"secret":   str("凭据本体（原文；报告与交付包只印身份与指纹，不印这一项）"),
			"targets": map[string]any{
				"type":        "array",
				"description": "这条凭据能登哪（可选但强烈建议）：每条 {host, port?, service?, protocol?, verified?, evidence?, note?}。同一条目标重复给会自动合并。",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"host":     str("目标主机（域名或 IP）"),
						"port":     intp("端口（可省略）"),
						"service":  str("什么服务：ssh / rdp / smb / winrm / mysql / http / 其他（如实写）"),
						"protocol": str("怎么连（可与 service 不同，可省略）"),
						"verified": map[string]any{"type": "boolean", "description": "是否在这条目标上**实测登入成功**（true 时必须给 evidence）"},
						"evidence": str("验证的那一行输出（verified=true 时必填）"),
						"note":     str("备注（可省略）"),
					},
				},
			},
			"detail":     str("细节：从哪个文件/配置/内存里读到的等"),
			"evidence":   str("【一行】关键证据：读到它的那条命令 + 关键输出行（必填）"),
			"confidence": str("high（明文直接读到）| medium（格式像但未验证）| low（据线索推断，尚未确认）"),
			"intent_id":  idp("当前任务的意图 id（你领到的意图）"),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "凭据所在的资产 id"},
		}, "summary", "evidence", "confidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a credentialItem
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			targets, err := normalizeTargets(a.Targets)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id, err := t.recordLoot(db.KindCredential, a.lootBase, map[string]any{
				"kind": a.Kind, "username": a.Username, "secret": a.Secret, "targets": targets,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			t.writes.Credentials++
			t.writes.noteValidationAssets(pidList(a.AssetIDs))
			// 能登哪 → 图上连一条 reaches（但**不**标 owned：能认证不等于机器已经在手）。
			if len(targets) > 0 {
				hosts := make([]string, 0, len(targets))
				for _, tg := range targets {
					if strings.TrimSpace(tg.Host) != "" {
						hosts = append(hosts, tg.Host)
					}
				}
				t.linkReaches(id, hosts...)
			}
			return actool.Text(fmt.Sprintf("credential recorded: %d", id)), nil
		})
}

// detectionItem 是一次检测验证的记录。
type detectionItem struct {
	Summary    string            `json:"summary"`
	Technique  string            `json:"technique"`
	Result     string            `json:"result"`
	Detail     string            `json:"detail"`
	Evidence   string            `json:"evidence"`
	Confidence string            `json:"confidence"`
	IntentID   json.RawMessage   `json:"intent_id"`
	AssetIDs   []json.RawMessage `json:"asset_ids"`
}

// reportArtifact 记录一件**我们留在目标上**的东西：webshell、上传的文件、建的账号、计划任务、改过的配置。
//
// 它跟其它战果是反的——据点与凭据记"我们拿到了什么"，它记"我们给客户留下了什么"。
// 没清掉的 webshell 就是留在客户系统上的后门，而交付时也要能说清我们动过什么。
//
// **它不是一道流程要求，是一条信息**：落了东西说一声，仅此而已。工具描述只陈述后果，
// 不排序、不强制——与 report_detection_check 那条反强制条款同调。
func (t *ToolSet) reportArtifact() actool.CoreTool {
	return t.writeExpTool("report_artifact",
		"记一件【你留在目标上】的东西：webshell、上传的工具/脚本、建的账号、计划任务、改过的配置。"+
			"**没清掉的 webshell 是留在客户系统上的后门**，而且交付时说不清我们动过什么——所以落了东西就记一条。"+
			"必须给 evidence（落地/创建的那一行输出）与 confidence（战果同一条证据门）。"+
			"**文件类 locator 必须写目标上的绝对路径**（如 /var/www/html/upload/x.php），url 写访问入口"+
			"（如 http://host/upload/x.php）：绝对路径给清理，URL 给复现。**目标侧标识符（账号/文件名/任务名/服务名）"+
			"必须中性，不得带平台名（BLADE）或项目特征**——客户也会看到这些名字。清掉之后用 mark_artifact_cleared 销账，不要重复 report。",
		obj(map[string]any{
			"summary":    str("一句话：在【哪台主机】的【哪里】放了/建了【什么】"),
			"kind":       str("分类：webshell / upload（上传的文件）/ account（账号）/ scheduled_task / config_change / service / other"),
			"host":       str("落在哪台目标上（域名或 IP）"),
			"locator":    str("具体位置：**文件写目标上的绝对路径**（如 /var/www/html/upload/x.php）/ 账号名 / 任务名 / 注册表键——报告与清理都靠它"),
			"url":        str("访问入口：webshell/上传文件的 URL 或可执行方式（如 http://host/upload/x.php）；文件类尽量填，报告要同时给出绝对路径与访问入口"),
			"cleanup":    str("怎么清（命令或步骤）；写下来，清理的人不用重新推一遍"),
			"state":      str("active（确认还在，默认）| unknown（落了但没确认还在不在）——**别把 unknown 写成 active**"),
			"detail":     str("细节：为什么放它、有没有用上、注意什么"),
			"evidence":   str("【一行】落地/创建时的命令 + 关键输出（必填）"),
			"confidence": str("high（亲手敲过、当场可见）| medium（成功但未复验）| low（据现象推断、尚未确认）"),
			"intent_id":  idp("当前任务的意图 id（你领到的意图）"),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "所在的资产 id"},
		}, "summary", "evidence", "confidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				lootBase
				Kind    string `json:"kind"`
				Host    string `json:"host"`
				Locator string `json:"locator"`
				URL     string `json:"url"`
				Cleanup string `json:"cleanup"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// 状态**不做归一**：猜错会让"没清"显示成"清了"，那是这本账唯一不能犯的错。
			if s := strings.TrimSpace(a.State); s != "" && !db.ValidArtifactState(s) {
				return actool.Errorf("state 必须是 active | removed | unknown（别把没确认的写成 active，也别把没清的写成 removed）"), nil
			}
			id, err := t.recordLoot(db.KindArtifact, a.lootBase, map[string]any{
				"kind":    db.NormalizeArtifactKind(a.Kind),
				"host":    a.Host,
				"locator": a.Locator,
				"url":     a.URL,
				"cleanup": a.Cleanup,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			t.writes.Artifacts++
			t.writes.noteValidationAssets(pidList(a.AssetIDs))
			return actool.Text(fmt.Sprintf("artifact recorded: %d", id)), nil
		})
}

// markArtifactCleared 把一件遗留物标成【已清理】。
//
// 与 verify_credential 是同一个形状、同一条理由：**没有清理时的那行输出，"我清掉了"就只是个说法**，
// 而它决定的是"客户系统上还有没有后门"。所以 evidence 必填，与战果的证据门同一条线。
func (t *ToolSet) markArtifactCleared() actool.CoreTool {
	return t.writeExpTool("mark_artifact_cleared",
		"把一件【已记录】的遗留物标成「已清理」。用在：你真的把它删掉/停掉/恢复了，并留下了输出。"+
			"必须给 evidence（清理命令 + 关键输出行）——没有它，「清掉了」只是个说法，而它决定的是客户系统上还有没有后门。"+
			"**只在真的清掉时用**：没清就说没清，不要销账。",
		obj(map[string]any{
			"artifact_id": idp("要销账的遗留物节点 id（report_artifact 回执里的那个数）"),
			"evidence":    str("【一行】清理命令 + 关键输出行（必填）"),
			"note":        str("备注（可省略）：残留、需人工确认的部分等"),
		}, "artifact_id", "evidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				ArtifactID json.RawMessage `json:"artifact_id"`
				Evidence   string          `json:"evidence"`
				Note       string          `json:"note"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := pid(a.ArtifactID)
			if id <= 0 {
				return actool.Errorf("artifact_id 必填：要销账哪一件遗留物（report_artifact 的回执里给了）"), nil
			}
			if strings.TrimSpace(a.Evidence) == "" {
				return actool.Errorf("evidence 必填：清理的那一行输出——没有它，「清掉了」只是个说法，而它决定的是客户系统上还有没有后门"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindArtifact {
				return actool.Errorf("artifact_id 必须是本任务里的一件遗留物节点"), nil
			}
			var payload map[string]any
			if err := json.Unmarshal(node.Payload, &payload); err != nil || payload == nil {
				payload = map[string]any{}
			}
			payload["removed_evidence"] = strings.TrimSpace(a.Evidence)
			if n := strings.TrimSpace(a.Note); n != "" {
				payload["removed_note"] = n
			}
			if err := t.ts.SetNodePayload(id, db.KindArtifact, payload); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := t.ts.SetNodeState(id, db.StateArtifactRemoved); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("artifact %d marked cleared", id)), nil
		})
}

// reportDetection 记录一次检测验证：这一手做完之后，防御侧有没有响。
//
// 这是本仓唯一一个**主动去问"我们被抓到了吗"**的工具，也是「平台」与「扫描器」的分水岭。
// 三态里 unknown 必须与 not_triggered 分开：**「日志里没这条」和「日志里没有该时段」是两回事**，
// 合成一档就是把"没看见"读成"没拦住"——那时给出的结论比不给更危险。
func (t *ToolSet) reportDetection() actool.CoreTool {
	return t.writeExpTool("report_detection_check",
		"记录一次【检测验证】：你刚做的这一手（按 ATT&CK 技术标），防御侧有没有响。"+
			"三态必须分清：**triggered**=确实响了（有日志/告警为证）、**not_triggered**=确实没响（你查过、该时段确实没有记录）、"+
			"**unknown**=你没法判定（没有日志源、没权限看、时段对不上）——**unknown 不是「没响」**，别把「我没看到」写成「它没拦住」。"+
			"必须给 evidence（你查了哪里、看到了什么）与 confidence；查不出所以然就老实记 unknown。",
		obj(map[string]any{
			"summary":    str("一句话：【这个动作】有没有被【哪一侧的什么】发现"),
			"technique":  str("被验证的 ATT&CK 技术 ID（T#### 或 T####.###；本动作对应哪一手）"),
			"result":     map[string]any{"type": "string", "enum": []string{db.StateDetectionTriggered, db.StateDetectionNotTriggered, db.StateDetectionUnknown}, "description": "triggered=确实响了 / not_triggered=确实没响 / unknown=无从判定（**不是「没响」**）"},
			"detail":     str("细节：查的是哪个日志源/告警平台、时间窗怎么对的、看到了什么"),
			"evidence":   str("【一行】关键证据：查询命令 + 关键输出（或「该时段无记录」这一事实本身）"),
			"confidence": str("high（直接看到日志/告警）/ medium（间接推断）/ low（仅凭现象）"),
			"intent_id":  idp("当前任务的意图 id（你领到的意图）"),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "被测试/被观测的资产 id"},
		}, "summary", "technique", "result", "evidence", "confidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a detectionItem
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			summary := strings.TrimSpace(a.Summary)
			if summary == "" {
				return actool.Errorf("summary 不能为空"), nil
			}
			// 技术 ID 复用意图那套归一化：同一个形状规则，别在两处各写一份。
			techs := db.NormalizeTechniques([]string{a.Technique})
			if len(techs) != 1 {
				return actool.Errorf(fmt.Sprintf("technique 必须是单个 ATT&CK 技术 ID（T#### 或 T####.###），收到 %q", a.Technique)), nil
			}
			switch a.Result {
			case db.StateDetectionTriggered, db.StateDetectionNotTriggered, db.StateDetectionUnknown:
			default:
				return actool.Errorf(fmt.Sprintf("result 必须是 triggered / not_triggered / unknown（三态要分清：unknown 不是「没响」），收到 %q", a.Result)), nil
			}
			if strings.TrimSpace(a.Evidence) == "" {
				return actool.Errorf("evidence 不能为空：你查了哪里、看到了什么——「没看到」本身也要有据（查的哪个源、哪个时段）"), nil
			}
			if strings.TrimSpace(a.Confidence) == "" {
				return actool.Errorf("confidence 不能为空：high 直接看到日志 / medium 间接推断 / low 仅凭现象"), nil
			}
			if !db.ValidConfidence(a.Confidence) {
				return actool.Errorf("confidence 取值必须是 high|medium|low"), nil
			}
			intent, err := t.writeIntent(a.IntentID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if intent > 0 {
				node, err := t.ts.GetNode(intent)
				if err != nil || node == nil || node.Kind != db.KindIntent {
					return actool.Errorf("intent_id 必须是本任务的意图（关联任务意图只读）"), nil
				}
			}
			payload := map[string]any{"summary": summary, "technique": techs[0]}
			for k, v := range map[string]string{
				"detail": a.Detail, "evidence": a.Evidence, "confidence": a.Confidence,
			} {
				if s := strings.TrimSpace(v); s != "" {
					payload[k] = s
				}
			}
			// state 就是结果本身：kind='detection' 的三态在 schema 里已经定死，这里不另立一档。
			id, err := t.ts.AddNode(db.KindDetection, payload, 6, a.Result, t.worker, pidList(a.AssetIDs))
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if intent > 0 {
				_ = t.ts.Link(intent, db.RelYields, id) // 与 fact/finding/战果同一条链
			}
			t.writes.Detections++
			t.writes.noteValidationAssets(pidList(a.AssetIDs))
			return actool.Text(fmt.Sprintf("detection check recorded: %d", id)), nil
		})
}

type hintItem struct {
	Text        string            `json:"text"`
	AssetIDs    []json.RawMessage `json:"asset_ids"`
	TrafficRefs []db.TrafficRef   `json:"traffic_refs"`
}

// addOneHint 挂一条 hint 节点(active/human)到探索图,可锚定资产,返回 id。
func (t *ToolSet) addOneHint(it hintItem) (int64, error) {
	if len(it.TrafficRefs) > 0 && !findingTrafficBindingEnabled() {
		return 0, fmt.Errorf("Agent 自动绑定流量已关闭，未保存携带 traffic_refs 的提示；可在系统设置开启，或仅交接文字")
	}
	if strings.TrimSpace(it.Text) == "" {
		return 0, fmt.Errorf("text 不能为空")
	}
	var anchors []int64
	for _, raw := range it.AssetIDs {
		if tid := pid(raw); tid > 0 {
			anchors = append(anchors, tid)
		}
	}
	refs, err := db.NormalizeTrafficRefs(it.TrafficRefs)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{"text": it.Text}
	if len(refs) > 0 {
		payload["traffic_refs"] = refs
	}
	// 唤醒 planner 不在此处逐条做——由 addHint 在整批写完后统一触发一次（带上提示文本），
	// 避免一次 add_hint 多条提示逐条刷屏 planner 的触发行。
	return t.ts.AddNode(db.KindHint, payload, 0, "active", "human", anchors)
}

// consumeHint marks adopted hints as consumed. It exists so a hint can leave the
// planner's active window after being acted on: without this, every hint stays
// active forever and is re-read each round. Consumption is idempotent — an already
// consumed id is reported as skipped, not an error, so a retried batch still succeeds.
func (t *ToolSet) consumeHint() actool.CoreTool {
	return t.writeExpTool("consume_hint",
		"把已采纳的**战略提示**标记为已采纳（consumed），此后它不再出现在态势的 hints 里。\n"+
			"★ 采纳=你已经据它派了意图，或已判断它与现状重复/不适用并在 note 写明理由。\n"+
			"★ 同一轮里派完意图就调用本工具；幂等——已采纳的 id 会列入 skipped，不报错。\n"+
			"★ 只能处理本任务的提示；不要用它删除提示（删除是人的操作）。",
		obj(map[string]any{
			"hint_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "要标记已采纳的提示 id 数组（graph_overview 的 hints）"},
			"note":     str("可选：采纳理由/对应意图 id；不采纳时写清为什么不适用"),
		}, "hint_ids"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("consume_hint 需要任务上下文"), nil
			}
			var a struct {
				HintIDs []json.RawMessage `json:"hint_ids"`
				Note    string            `json:"note"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if len(a.HintIDs) == 0 {
				return actool.Errorf("consume_hint: hint_ids 不能为空"), nil
			}
			consumed := make([]int64, 0, len(a.HintIDs))
			skipped := make([]map[string]string, 0)
			for _, raw := range a.HintIDs {
				id := pid(raw)
				if id <= 0 {
					skipped = append(skipped, map[string]string{"id": "0", "reason": "不是有效的提示 id"})
					continue
				}
				n, err := t.ts.GetNode(id)
				if err != nil {
					skipped = append(skipped, map[string]string{"id": strconv.FormatInt(id, 10), "reason": err.Error()})
					continue
				}
				if n == nil || n.Kind != db.KindHint {
					skipped = append(skipped, map[string]string{"id": strconv.FormatInt(id, 10), "reason": "不存在于本任务或不是提示"})
					continue
				}
				ok, err := t.ts.ConsumeHint(id, a.Note)
				if err != nil {
					skipped = append(skipped, map[string]string{"id": strconv.FormatInt(id, 10), "reason": err.Error()})
					continue
				}
				if !ok {
					skipped = append(skipped, map[string]string{"id": strconv.FormatInt(id, 10), "reason": "已采纳，无需重复标记"})
					continue
				}
				consumed = append(consumed, id)
			}
			return jsonResult(map[string]any{"consumed": consumed, "skipped": skipped})
		})
}

type goalItem struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass"`
}

// addOneGoal 新增一条 goal 节点(open)。目标初始**不**连任务根：起点与目标之间
// 不该有一条"看起来进度已连上"的边；只有真正被事实/漏洞证明时，prove_goal 才会写
// evidence --proves--> goal。origin 取 t.worker(缺省 system):goals 拆解器写入的记
// "goals"、主 agent 运行时记 "human"。唤醒 planner 由 setGoals 在整批写完后统一做。
func (t *ToolSet) addOneGoal(it goalItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text 不能为空")
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(it.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	origin := t.worker
	if origin == "" {
		origin = "system"
	}
	id, err := t.ts.AddNode(db.KindGoal, payload, 0, "open", origin, nil)
	if err != nil {
		return 0, err
	}
	// 目标只保留「起点 → 目标」这条初始派生链；目标与意图之间不再建边。
	if of, _ := t.ts.OriginFactID(); of > 0 && id > 0 {
		_ = t.ts.Link(of, db.RelSpawns, id)
	}
	return id, nil
}

// setGoals 给【本任务】新增探索目标(goal 节点)。既是目标拆解器的提交工具,也是主
// agent 运行时补目标的工具——同一个受管工具,可在 web 端改描述/schema、按 agent 绑定。
func (t *ToolSet) setGoals() actool.CoreTool {
	return writeTool("set_goals",
		"给【本任务】新增探索目标(goal)。目标=最终可交付/可核验的结果,不是攻击步骤或侦察动作。\n"+
			"★优先批量:多个目标放进 goals 数组一次提交,返回 ids 与之等长同序(失败项 id=0,详情见 errors)。单条则省略 goals 直接给顶层 text。\n"+
			"vulnclass 可选:对应漏洞类(如 SQLi/IDOR),业务逻辑类目标留空。目标是否达成由系统判定标记 met,本工具只负责新增。",
		obj(map[string]any{
			"goals":     map[string]any{"type": "array", "description": "【优先用这个】要新增的目标数组,按顺序处理。每个元素:text(必填,一个独立可验证的最终目标)+ vulnclass(可选)。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"text":      str("[单条] 一个独立可验证的最终目标"),
			"vulnclass": str("[单条] 对应漏洞类(若明确),如 SQLi/IDOR;业务逻辑目标可留空"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_goals 未启用: ExplorationStore 未初始化"), nil
			}
			var a struct {
				Goals    []goalItem `json:"goals"`
				goalItem            // 单条模式:顶层 text/vulnclass
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Goals) > 0
			items := a.Goals
			if !batch {
				items = []goalItem{a.goalItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneGoal(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// 唤醒 planner(整批一次)。优先 notifyGoal:一次 set_goals 记一条「人新增了
				// N 个目标:…」触发,不逐条刷屏;拆解器/worker 无此回调 → 退回纯 notify(拆解器
				// round-0 连 notify 也没接,即无操作,因为此时 planner 尚未启动)。
				switch {
				case t.notifyGoal != nil:
					t.notifyGoal(addedTexts)
				case t.notify != nil:
					t.notify()
				}
				// 主 agent 运行时新增目标 → 把已完成/暂停的任务拉回 running 继续跑(终态门会
				// 吞掉普通 notify,必须显式复活)。仅 mainagent 接了此回调;拆解器/worker 为 nil。
				if t.resumeTask != nil {
					t.resumeTask()
				}
			}

			if !batch { // 单条:保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("goal added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type constraintItem struct {
	Text string `json:"text"`
	Type string `json:"type"` // allow | deny
}

// addOneConstraint 落一条操作约束到 task_constraints。origin 取 t.worker(缺省 system):
// 拆解器写 "goals"、主 agent 写 "human"。
func (t *ToolSet) addOneConstraint(it constraintItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text 不能为空")
	}
	kind := strings.TrimSpace(strings.ToLower(it.Type))
	if kind == "" {
		kind = "deny" // 默认按禁止处理:未标注类型时更保守
	}
	if kind != "allow" && kind != "deny" {
		return 0, fmt.Errorf("type 必须是 allow 或 deny")
	}
	return t.ts.AddConstraint(kind, text, t.worker)
}

// setConstraints 给【本任务】新增操作约束(allow=允许做什么 / deny=禁止做什么)。既是目标
// 拆解器 round-0 抽约束的提交工具,也是主 agent 运行时补约束的工具——同一受管工具,可在 web
// 端改描述/schema、按 agent 绑定。约束会被注入 planner/worker 的系统提示以约束探索边界。
func (t *ToolSet) setConstraints() actool.CoreTool {
	return writeTool("set_constraints",
		"给【本任务】新增操作约束,用来框定探索边界:type=allow(允许做的操作)或 deny(禁止做的操作)。\n"+
			"约束=对『可以/不可以做哪些操作』的规定(如『仅测当前端口,不扫其他端口』『禁止对生产库做写操作』『只允许被动侦察』),不是目标、也不是攻击步骤。\n"+
			"★优先批量:多条放进 constraints 数组一次提交,返回 ids 与之等长同序(失败项 id=0,详情见 errors)。单条则省略 constraints 直接给顶层 text/type。\n"+
			"只登记任务目标/描述里【明确写出】的约束,不要臆造;拿不准类型时用 deny(更保守)。",
		obj(map[string]any{
			"constraints": map[string]any{"type": "array", "description": "【优先用这个】要新增的约束数组,按顺序处理。每个元素:text(必填,一条约束)+ type(allow|deny)。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"text":        str("[单条] 一条操作约束的内容"),
			"type":        str("[单条] allow(允许)或 deny(禁止);缺省按 deny 处理"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_constraints 未启用: ExplorationStore 未初始化"), nil
			}
			var a struct {
				Constraints    []constraintItem `json:"constraints"`
				constraintItem                  // 单条模式:顶层 text/type
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Constraints) > 0
			items := a.Constraints
			if !batch {
				items = []constraintItem{a.constraintItem}
			}
			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.addOneConstraint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}
			if !batch { // 单条:保持简单返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("constraint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) addHint() actool.CoreTool {
	return t.writeExpTool("add_hint", "把人类/主 agent 的战略提示挂到探索图，规划者下次生成意图时会读到它。\n"+
		"★优先批量：多条提示放进 hints 数组一次提交（比逐条调用省往返）。返回 ids 数组，与 hints 等长同序（失败项 id=0，详情见 errors）。单条则省略 hints 直接给顶层 text。",
		obj(map[string]any{
			"hints":        map[string]any{"type": "array", "description": "【优先用这个】要新增的提示数组，按顺序处理。每个元素字段同下方顶层字段（text/asset_ids/traffic_refs）。返回 ids 与本数组等长、同序。", "items": obj(map[string]any{"text": str("提示内容"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": HintTrafficSchema()})},
			"text":         str("[单条] 提示内容，如'重点挖认证后接口'"),
			"traffic_refs": HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "锚定的资产 id（可选，0/1/多个）"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Hints    []hintItem `json:"hints"`
				hintItem            // 单条模式：顶层 text/asset_ids
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Hints) > 0
			items := a.Hints
			if !batch {
				items = []hintItem{a.hintItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneHint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// 唤醒 planner（整批一次）。优先 notifyHint：一次 add_hint 记一条「人新增了
				// N 条战略提示：…」触发，让 planner 明确"本轮由新增 hint 触发"并看到提示内容；
				// 未接该回调时退回纯 notify（bare wake，hint 仍折在图里供其自行读取）。
				switch {
				case t.notifyHint != nil:
					t.notifyHint(addedTexts)
				case t.notify != nil:
					t.notify()
				}
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("hint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

// killWorkTool lets the planner terminate a single running work (by intent id).
func (t *ToolSet) killWorkTool() actool.CoreTool {
	return t.writeExpTool("kill_work", "终止一条正在运行的意图(work)。用于叫停跑偏/无意义的探索；被终止的意图标记为 stopped，不再自动重领。先用 get_worker_output 看看它在干嘛再决定。",
		obj(map[string]any{"intent_id": idp("要终止的意图 id（= work 句柄）")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.killWork == nil {
				return actool.Errorf("kill_work 当前不可用"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id 必须是本任务的意图（关联任务意图只读）"), nil
			}
			if isCleanupIntentPayload(node) {
				return actool.Errorf(fmt.Sprintf("kill_work: 意图 %d 是操作者提交的痕迹清理意图，不能由规划者终止；请在任务详情「痕迹」页签人工处理", id)), nil
			}
			if err := t.killWork(id); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("已向意图 %d 的 work 发送终止信号", id)), nil
		})
}

// steerWorkTool lets the planner inject a mid-run course-correction into a running
// work WITHOUT killing it: the message reaches the worker before its next tool call,
// which re-plans its next step (already-gathered context is kept). For in-intent
// nudges ("停做 X、聚焦 Y"); if the whole direction is wrong use kill_work + a new intent.
func (t *ToolSet) steerWorkTool() actool.CoreTool {
	return t.writeExpTool("steer_work", "给一条正在运行的意图(work)实时注入纠偏指令，不打断它、不丢已有进展：worker 会在下一步动作前收到你的指令并据此调整。用于'别再走 X、聚焦 Y'这类【意图内】纠偏；若方向整个错了应改用 kill_work 再下新意图。建议先用 get_worker_output 看它在干嘛。",
		obj(map[string]any{
			"intent_id": idp("要纠偏的意图 id（= work 句柄）"),
			"message":   str("给 worker 的纠偏指令，明确让它停止什么、转向什么"),
		}, "intent_id", "message"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.steerWork == nil {
				return actool.Errorf("steer_work 当前不可用"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Message  string          `json:"message"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id 必须是本任务的意图（关联任务意图只读）"), nil
			}
			if strings.TrimSpace(a.Message) == "" {
				return actool.Errorf("message 必填"), nil
			}
			if err := t.steerWork(id, a.Message); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("已向意图 %d 的 work 注入纠偏指令（下一步生效）", id)), nil
		})
}

// getWorkerOutput returns a work's final (or截至中止时的) conclusion text by intent id.
func (t *ToolSet) getWorkerOutput() actool.CoreTool {
	return t.readExpTool("get_worker_output", "取本任务或直接关联任务某条意图(work)的最终输出结论。关联任务结果带 source_task_id/inherited=true 且只读。正常结束返回其总结；被终止(stopped)/异常的 work 返回其截至中止时的最后输出。另附 recent_steps（最后 3 步动作与 step_id）与 executions（该意图的后台执行台账：命令/终态/退出码/输出文件路径）——判断「它到底做到哪、是不是真跑过」用这两项，别只看最终那句总结；要看台账里的具体输出行用 grep_execution。",
		obj(map[string]any{"intent_id": idp("意图 id（= work 句柄）")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			intentNode, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id 不属于本任务或其直接关联任务"), nil
			}
			// 取**尾部**：长任务的「卡在哪 / 最后一句结论」在最新那一窗里，升序前 1000 条只给开头。
			acts, err := t.ts.ActivityListTailWithSources(id, 1000)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var chosen, fallback *db.Activity
			for i := range acts {
				switch acts[i].Kind {
				case "result":
					chosen = &acts[i]
					fallback = &acts[i]
				case "text":
					fallback = &acts[i]
				}
			}
			pick := chosen
			if pick == nil {
				pick = fallback
			}
			if pick == nil {
				if intentNode.Inherited {
					return jsonResult(inheritedMap(map[string]any{
						"intent_id": id, "final_text": "（该 work 尚无任何输出）",
					}, intentNode.SourceTaskID))
				}
				return actool.Text("（该 work 尚无任何输出）"), nil
			}
			detail, _ := t.ts.ActivityDetailWithSources(pick.ID)
			if detail == "" {
				detail = pick.Summary
			}
			result := map[string]any{
				"intent_id": id, "final_text": detail,
				"summary": pick.Summary, "is_error": pick.IsError,
			}
			// 光看最终结论看不出"它到底做到哪、是不是真跑了"：被预算掐断或被 blocked
			// 的意图，最终输出常常只是半句话。补两样实测记录——
			//   · recent_steps：最后 3 步（动作轮廓，含 step_id，可接着用 get_worker_trace 取全文）；
			//   · executions：该意图的后台执行台账（命令/终态/退出码/输出文件），
			//     要具体行用 grep_execution(execution_id, pattern)，不必重跑。
			if steps := traceSteps(acts); len(steps) > 0 {
				if len(steps) > 3 {
					steps = steps[len(steps)-3:]
				}
				result["recent_steps"] = steps
			}
			if t.execService != nil {
				if rows, err := t.execService.ListForIntent(id); err == nil && len(rows) > 0 {
					execs := make([]map[string]any, 0, 5)
					for _, r := range rows {
						if len(execs) >= 5 {
							break
						}
						exitCode := -1
						if r.ExitCode != nil {
							exitCode = *r.ExitCode
						}
						execs = append(execs, map[string]any{
							"execution_id": r.ID, "status": r.Status, "command": firstLine(r.Command, 160),
							"exit_code": exitCode, "output_bytes": r.OutputBytes,
							"output_path": r.OutputPath, "error": r.Error,
						})
					}
					result["executions"] = execs
				}
			}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// traceSteps renders summary-only trace rows, re-truncating each summary to 100
// chars — the stored summary is capped at 200 for the UI transcript; the trace
// tools want it tighter since a whole work's step list is many rows.
func traceSteps(acts []db.Activity) []map[string]any {
	steps := make([]map[string]any, 0, len(acts))
	for i := range acts {
		step := map[string]any{
			"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
			"is_error": acts[i].IsError, "summary": firstLine(acts[i].Summary, 100),
		}
		if acts[i].Inherited {
			inheritedMap(step, acts[i].SourceTaskID)
		}
		steps = append(steps, step)
	}
	return steps
}

// getWorkerTrace exposes a work's execution PROCESS (not just its final output):
// list step summaries, keyword-search within one work, or pull full detail of a
// few specific steps. Thinking steps are excluded everywhere.
func (t *ToolSet) getWorkerTrace() actool.CoreTool {
	return t.readExpTool("get_worker_trace",
		"查看某条意图(work)的【执行过程】（区别于 get_worker_output 只给最终结论）。三种用法：\n"+
			"① 只传 intent_id → 返回该 work 每一步的摘要流（summary≤100字，含 step_id；只是动作轮廓，不含完整输出）；\n"+
			"② intent_id + q → 只返回命中关键字的步骤摘要（在摘要和完整输出里都搜；仍只给 summary，要看内容用③）；\n"+
			"③ intent_id + step_ids → 返回这些步骤的完整内容(detail)；一次最多取 5 个，超出只返回前 5 个并在 notice/omitted_step_ids 里告知未取的。\n"+
			"典型流程：先①/②定位可疑步骤的 step_id，再用③取其完整输出。不含思考(thinking)步骤。支持直接关联任务的历史 trace；其结果带 source_task_id/inherited=true 且只读。",
		obj(map[string]any{
			"intent_id": idp("意图 id（= work 句柄）"),
			"q":         str("关键字：只返回摘要/完整输出命中它的步骤（可选；与 step_ids 互斥）"),
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "要取完整内容的 step_id（来自①/②返回；一次最多取 5 个，多传只返回前 5 个，其余在 omitted_step_ids 里列出）"},
			"limit":     intp("摘要流/检索的返回上限（可选）"),
		}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage   `json:"intent_id"`
				Q        string            `json:"q"`
				StepIDs  []json.RawMessage `json:"step_ids"`
				Limit    int               `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			intentNode, nodeErr := t.ts.GetNodeWithSources(id)
			if nodeErr != nil {
				return actool.Errorf(nodeErr.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id 不属于本任务或其直接关联任务"), nil
			}
			// ③ detail drill-down by step ids, thinking excluded by the store.
			if len(a.StepIDs) > 0 {
				// Dedup + drop invalid ids first so garbage/duplicates don't eat into
				// the per-call cap. detail is returned in full (untruncated), so the
				// cap bounds one tool result; over the cap we serve the first N and
				// tell the model exactly which ids were deferred, instead of erroring
				// and forcing it to re-plan the call.
				const maxStepIDs = 5
				var ids []int64
				seen := make(map[int64]bool)
				for _, raw := range a.StepIDs {
					if v := pid(raw); v > 0 && !seen[v] {
						seen[v] = true
						ids = append(ids, v)
					}
				}
				var omitted []int64
				if len(ids) > maxStepIDs {
					omitted = append(omitted, ids[maxStepIDs:]...)
					ids = ids[:maxStepIDs]
				}
				acts, err := t.ts.ActivityByIDsWithSources(ids)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				steps := make([]map[string]any, 0, len(acts))
				for i := range acts {
					if acts[i].NodeID == nil || *acts[i].NodeID != id || acts[i].Inherited != intentNode.Inherited ||
						(acts[i].Inherited && acts[i].SourceTaskID != intentNode.SourceTaskID) {
						continue
					}
					step := map[string]any{
						"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
						"is_error": acts[i].IsError, "detail": acts[i].Detail,
					}
					if acts[i].Inherited {
						inheritedMap(step, acts[i].SourceTaskID)
					}
					steps = append(steps, step)
				}
				result := map[string]any{"intent_id": id, "steps": steps, "returned_step_ids": ids}
				if len(omitted) > 0 {
					// returned_step_ids/omitted_step_ids let the model decide programmatically
					// whether another call is worth it; the notice states the same in prose.
					result["omitted_step_ids"] = omitted
					result["notice"] = fmt.Sprintf(
						"每次最多取 %d 个步骤的完整内容，本次已返回前 %d 个（%v），未取的 %d 个为 %v。"+
							"若这些内容已足够定位，则无需再取剩余步骤；确需继续时，用这些 step_id 再调一次。",
						maxStepIDs, len(ids), ids, len(omitted), omitted)
				}
				if intentNode.Inherited {
					inheritedMap(result, intentNode.SourceTaskID)
				}
				return jsonResult(result)
			}
			// ①/② summary stream, optionally keyword-filtered; 100-char summaries.
			var acts []db.Activity
			var err error
			if strings.TrimSpace(a.Q) != "" {
				acts, err = t.ts.ActivityTraceSearchWithSources(id, a.Q, a.Limit)
			} else {
				acts, err = t.ts.ActivityTraceTailWithSources(id, a.Limit)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			result := map[string]any{"intent_id": id, "steps": traceSteps(acts)}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// searchAllWorkerTraces keyword-searches EVERY work's process in this task — for
// finding what a worker saw but never wrote back as a fact. Returns only matching
// summaries (≤100 chars), each tagged with its intent_id for follow-up drill-down.
func (t *ToolSet) searchAllWorkerTraces() actool.CoreTool {
	return t.readExpTool("search_all_worker_traces",
		"【通常不推荐使用，因为系统中已经给了大部分信息了】在【本任务其他 work 的执行过程】里按关键字(q)检索——用于找回某个 worker 见过、却没写进 fact 的东西（某路径/token/报错等）。"+
			"已自动排除你自己这条意图的步骤（那些本就在你上下文里）。"+
			"只返回命中步骤的摘要(summary≤100字)，每条带 intent_id；据此再用 get_worker_trace(intent_id, step_ids=[...]) 取完整内容。",
		obj(map[string]any{
			"q":     str("关键字（在所有 work 步骤的摘要+完整输出里搜）"),
			"limit": intp("返回上限，默认 100（可选）"),
		}, "q"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Q) == "" {
				return actool.Errorf("q 必填"), nil
			}
			// 排除调用者自身这条意图的步骤（worker 的自有 trace 已在其上下文里）。
			acts, err := t.ts.ActivityTraceSearchAllWithSources(t.ownerNode, a.Q, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			hits := make([]map[string]any, 0, len(acts))
			for i := range acts {
				var intent int64
				if acts[i].NodeID != nil {
					intent = *acts[i].NodeID
				}
				hit := map[string]any{
					"intent_id": intent, "step_id": acts[i].ID, "worker": acts[i].Worker,
					"kind": acts[i].Kind, "tool": acts[i].Tool, "is_error": acts[i].IsError,
					"summary": firstLine(acts[i].Summary, 100),
				}
				if acts[i].Inherited {
					inheritedMap(hit, acts[i].SourceTaskID)
				}
				hits = append(hits, hit)
			}
			return jsonResult(map[string]any{"query": a.Q, "hits": hits})
		})
}

// listWorkerTraces gives a worker (which has no graph_overview and can't see the
// intent graph) a lightweight index of the works in this task — intent_id +
// one-line summary + state — so it can DISCOVER which works to inspect via
// get_worker_trace. Without this a worker only knows intent_ids that come back
// from search_all_worker_traces hits. Excludes still-open intents (not yet run →
// no process to inspect).
func (t *ToolSet) listWorkerTraces() actool.CoreTool {
	return t.readExpTool("list_worker_traces",
		"列出本任务里【已跑过的意图】索引：intent_id + 一句话方向(summary) + 状态，最新在前。"+
			"态势里的 recent_done_intents 只是一窗；要确认「这个方向以前是不是试过」，用它翻全量。"+
			"分页：limit（默认 50，上限 200）+ before（游标，传上一页返回的 next_before 取更旧的一页）。"+
			"只列已执行过的（running/done/negative/out-of-scope/exhausted/blocked/no_output/stopped），不含还没跑的 open。"+
			"要看某条的具体过程用 get_worker_trace(intent_id) / get_worker_output(intent_id)。",
		obj(map[string]any{
			"q":      str("按 summary 关键字过滤（可选）"),
			"limit":  intp("返回上限，默认 50，上限 200（可选）"),
			"before": intp("分页游标：只返回 intent_id 小于该值的更旧意图；省略或 0 = 最新一页"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q      string `json:"q"`
				Limit  int    `json:"limit"`
				Before int64  `json:"before"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = 50
			}
			if limit > 200 {
				limit = 200
			}
			all, err := t.ts.ListByKindWithSources(db.KindIntent, 500)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Q))
			out := make([]map[string]any, 0, limit)
			total := 0
			for _, n := range all {
				if n.Inherited && n.State == "running" {
					continue
				}
				switch n.State {
				case "running", "done", "negative", "out-of-scope", "exhausted", "blocked", "no_output", "stopped": // has run → has a process
				default:
					continue
				}
				var p map[string]any
				_ = json.Unmarshal(n.Payload, &p)
				summary, _ := p["summary"].(string)
				if q != "" && !strings.Contains(strings.ToLower(summary), q) {
					continue
				}
				total++
				if a.Before > 0 && n.ID >= a.Before {
					continue
				}
				if len(out) >= limit {
					continue // 继续数 total，才知道后面还有多少
				}
				item := map[string]any{"intent_id": n.ID, "summary": summary, "state": n.State}
				if n.Inherited {
					inheritedMap(item, n.SourceTaskID)
				}
				out = append(out, item)
			}
			res := map[string]any{"works": out, "total": total}
			if len(out) >= limit && len(out) > 0 {
				res["next_before"] = out[len(out)-1]["intent_id"]
			}
			return jsonResult(res)
		})
}

// PlannerTools is the read + intent-generation + goal-judgement tool set.
func (t *ToolSet) PlannerTools() []actool.CoreTool {
	tools := []actool.CoreTool{
		t.graphOverview(), t.blackboardView(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		// cold-digest §6.1: restore folded cold nodes (digest body → members → detail).
		t.expandDigest(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(),
		// list_worker_traces：态势里的 recent_done_intents 只是一窗（12 条），上百意图的任务里
		// 大部分已结束意图不在视野中。这个工具让 planner 能翻全量已跑意图，确认某方向是否试过。
		t.listWorkerTraces(),
		t.listGoals(), t.addIntent(), t.unblockIntent(), t.dropIntent(), t.proveGoal(), t.goalMet(),
		// consume_hint：采纳战略提示后把它移出 active 窗口（否则每轮重复出现）。
		t.consumeHint(),
		t.acceptArtifactRisk(),
		// 目标侧痕迹：planner 收官前查看并决定接受风险；清理意图由 worker 执行。
		t.listFootprints(), t.acceptFootprintRisk(),
		// 补血缘：add_intent 漏传 parent_ids 时，把已建意图接回真实来源（不动节点、不丢产出）。
		t.linkIntentSource(),
		// 收口：不可达目标 → 放弃（理由必填）。mark_strategy_exhausted 仅在
		// exhaustion 分组开启时注册（见函数末尾）。
		t.abandonGoal(),
		// 出口被 WAF 封禁时切换 Clash 节点（服务端验证新出口 IP，绝不直连）。
		t.rotateEgressTool(),
		// 去重：planner 自己报的重复漏洞要能合并，而不是留着两条污染计数。
		t.mergeFindings(),
		// coverage_matrix / 台账 / 收官豁免仅在对应分组开启时注册（见函数末尾）。
		t.killWorkTool(), t.steerWorkTool(),
		// 执行类工具（run/wait/get/grep/cancel_execution、loot_*、foothold_exec、
		// tunnel_open、oast_*、cve_exploit）与 report_finding 已从规划层移除：
		// planner 只产意图、不亲自打；执行与写漏洞由 worker 做。规划层需要这些动作时
		// 写进意图 summary，交给 worker 执行（见提示词）。
		//
		// promote_knowledge（P23）：把已确认的结论沉淀为公司级知识（跨任务只读背景）。
		t.promoteKnowledge(),
		// list_companies：查看企业列表 + scope + 资产数（拿 company_id / 理解归属范围）。
		t.listCompanies(),
		// list_assets：规划时按 DSL 检索全资产库（配合 list_untested_assets 的"范围内未测"视角，
		// 补上"按域名/指纹/端口/状态码等条件在整库里查"的能力）。
		t.listAssets(), t.assetNeighbors(),
		// add_task_scope / add_company_scope 已从规划层移除：扩面是人的决策（主 Agent /
		// UI）。planner 只在本任务既有范围内补方向，避免自行把授权范围越拉越大。
		// list_untested_assets：按需查本任务范围内未测资产(类型+分页)，自行决定补测。
		t.listUntestedAssets(),
		// ad_paths（P32）：读导入的域内采集结果推出的攻击路径（非特权账号 → 高价值目标）。
		t.adPaths(),
		// opsec_notes（P38）：派意图前查这一手预计会留下什么（事前知识库，与 P31 的实测分开）。
		t.opsecNotes(),
		// 指纹打法库：规划时按资产/指纹查"同类目标以前怎么打穿的"，并把规划层确认的打法
		// 手动沉淀/回填反馈（自动沉淀由 playbook agent 在 report_foothold 时完成）。
		t.lookupPlaybooks(), t.recordPlaybook(), t.recordPlaybookFeedback(),
		// CVE 库：规划时按资产/指纹查已录入 CVE；有脚本时可显式触发，匹配只是线索。
		t.lookupCVEs(),
	}
	// 探索约束工具按分组注册：默认经典模式不暴露记账/补证工具，增强模式逐组恢复。
	if t.constraintOn("opening") {
		tools = append(tools, t.recordProbeFact())
	}
	if t.constraintOn("depth") {
		tools = append(tools, t.dismissLeads())
	}
	if t.constraintOn("exhaustion") {
		tools = append(tools, t.markStrategyExhausted())
	}
	if t.constraintOn("ledger") {
		tools = append(tools, t.coverageMatrix(), t.explorationLedger(), t.dismissCoverage(), t.chainDetail())
	}
	if t.constraintOn("completion") {
		tools = append(tools, t.acceptCompletionRisk())
	}
	return tools
}

// normalizeFindingText 把摘要归一成只含字母/数字（含中文）的串，用于重复判断：
// 空格、标点、大小写差异不应让同一条漏洞绕过检查。
func normalizeFindingText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// similarFinding 判断新发现与已有发现是否「同类 + 同资产 + 摘要相似」。
// 返回命中的已有节点 id 与摘要（0 = 不重复）。
func similarFinding(summary string, assets []int64, prior []db.SimilarFinding) (int64, string) {
	norm := normalizeFindingText(summary)
	if len([]rune(norm)) < 8 {
		return 0, "" // 太短的摘要没有判别力，宁可放过
	}
	for _, p := range prior {
		pn := normalizeFindingText(p.Summary)
		if !findingSummarySimilar(norm, pn) || !findingAssetsOverlap(assets, p.AssetIDs) {
			continue
		}
		return p.NodeID, p.Summary
	}
	return 0, ""
}

func findingSummarySimilar(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	short, long := a, b
	if len([]rune(short)) > len([]rune(long)) {
		short, long = long, short
	}
	if len([]rune(short)) < 8 || !strings.Contains(long, short) {
		return false
	}
	return float64(len([]rune(short)))/float64(len([]rune(long))) >= 0.6
}

func findingAssetsOverlap(a, b []int64) bool {
	if len(a) == 0 && len(b) == 0 {
		return true // 两边都没标资产：同类别 + 摘要相似已经足够可疑
	}
	set := make(map[int64]bool, len(a))
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		if set[id] {
			return true
		}
	}
	return false
}

// riskAreaEnum 返回 risk_area 的合法取值说明（错误信息与提示词共用）。
func riskAreaEnum() string {
	return strings.Join(db.RiskAreas, " | ")
}

// riskAreaKeywords 是 risk_area 缺省时的推断表，按特异性从高到低匹配（先命中先返回）。
// 只用于漏填兜底——显式填写永远优先，填错仍然报错。
var riskAreaKeywords = []struct {
	area  string
	words []string
}{
	{"rce", []string{"rce", "命令执行", "命令注入", "代码执行", "反弹 shell", "getshell", "xp_cmdshell", "反序列化"}},
	{"ssti", []string{"ssti", "模板注入", "模板引擎"}},
	{"xxe", []string{"xxe", "xml 外部实体", "外部实体"}},
	{"ssrf", []string{"ssrf", "服务端请求伪造", "内网探测", "元数据"}},
	{"sqli", []string{"sqli", "sql 注入", "sql注入", "注入点", "盲注", "堆叠注入", "sql"}},
	{"xss", []string{"xss", "跨站", "存储型", "反射型"}},
	{"upload", []string{"上传", "upload", "任意文件写", "文件写入"}},
	{"traversal", []string{"路径穿越", "目录穿越", "traversal", "../"}},
	{"idor", []string{"idor", "对象级越权", "未授权访问", "越权读"}},
	{"authz", []string{"越权", "authz", "角色提权", "权限提升"}},
	{"logic", []string{"业务逻辑", "竞态", "并发", "金额", "优惠券", "支付", "订单"}},
	{"auth", []string{"登录", "认证", "口令", "密码", "账号", "会话", "session", "cookie", "注册", "找回"}},
	{"disclosure", []string{"泄露", "robots", "备份", "配置文件", "目录列举", "敏感文件", "信息暴露", "disclosure"}},
}

// inferRiskArea 从 summary 里推断一个风险类（缺省兜底，不追求完备）。
func inferRiskArea(summary string) string {
	low := strings.ToLower(summary)
	for _, entry := range riskAreaKeywords {
		for _, w := range entry.words {
			if strings.Contains(low, w) {
				return entry.area
			}
		}
	}
	return "other"
}

// intentAnchor* 是「这条意图点名了具体入口」的判据。刻意用宽口径：只要出现路径、
// URL/主机、查询参数、版本号或显式节点 id 之一就放行——目的是挡住"做认证面测试"
// 这类纯风险类目录，而不是要求每条意图都写成完整计划。
var (
	intentPathAnchor    = regexp.MustCompile(`(^|[\s（(：:,，;；、"“”‘’])/[A-Za-z0-9_.-][A-Za-z0-9_./{}$-]*`)
	intentHostAnchor    = regexp.MustCompile(`(?i)(https?://|[a-z0-9][a-z0-9-]*(\.[a-z0-9-]+)+)`)
	intentParamAnchor   = regexp.MustCompile(`([?&][A-Za-z_][A-Za-z0-9_]*=|[A-Za-z_][A-Za-z0-9_]*=(值|参数)|参数[「"“']?[A-Za-z_])`)
	intentVersionAnchor = regexp.MustCompile(`[0-9]+(\.[0-9xX]+)+`)
	intentNodeAnchor    = regexp.MustCompile(`(#|id=|intent-|fact-|finding-)[0-9]{1,9}`)
)

// hasVerificationChain 判断 summary 是否写了「先确认→再据它做→命中后」的链。
// 只用于提示，不做硬门。
func hasVerificationChain(summary string) bool {
	for _, w := range []string{"→", "先", "再", "然后", "接着", "之后", "命中后", "若", "如果", "第一步", "第二步", "确认后"} {
		if strings.Contains(summary, w) {
			return true
		}
	}
	return false
}

// coarseIntentSummary 判断一条意图 summary 是否"只有风险类、没有具体入口"。
// kind=discovery 的意图允许只写发现目标（域名/主机/网段）或带 asset_ids——资产发现
// 本身没有路径可写；其余意图（verify 与未标 kind 的旧口径）必须命中至少一个锚点。
func coarseIntentSummary(summary, kind string, hasAssets bool) (bool, string) {
	if kind == "discovery" {
		if hasAssets || intentHostAnchor.MatchString(summary) || intentPathAnchor.MatchString(summary) {
			return false, ""
		}
		return true, fmt.Sprintf("discovery 意图也要能落地：summary 里要写明要发现什么（域名/主机/网段/证书/服务），或至少带 asset_ids；%q 没有任何可执行目标。", firstLine(summary, 80))
	}
	for _, re := range []*regexp.Regexp{intentPathAnchor, intentHostAnchor, intentParamAnchor, intentVersionAnchor, intentNodeAnchor} {
		if re.MatchString(summary) {
			return false, ""
		}
	}
	return true, fmt.Sprintf("summary 没有点名任何具体入口（路径 / URL 或主机 / 查询参数 / 组件版本 / 节点 id）：%q。\n"+
		"意图是「在哪个入口上验证什么」，不是「把哪一类风险测一遍」——目录型意图会让 worker 每个路径各探一次就收工，深度全丢在最浅处。\n"+
		"改写示例：\n"+
		"  粗：对目标做认证面测试\n"+
		"  细：对 <host>/admin/login（302→/login?ReturnUrl=/admin）测账号枚举与低速口令喷洒，先确认错误回显差异，命中即停\n"+
		"  粗：测试注入\n"+
		"  细：对 <host>/search?q= 做报错/布尔/时间三通道最小验证，只取 version() 作证；被 WAF 拦时记录 challenge 类型后停", firstLine(summary, 80))
}
