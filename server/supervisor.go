package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// 执行审核员（supervisor）：按固定间隔抽查在跑 worker 的最新轨迹，判断它们是否
// 已经偏离意图/任务目标；判偏则通过 steer 通道注入一条纠偏指令，不打断、不丢上下文。
//
// 为什么放在引擎侧而不是 worker/planner 的会话里：
//   - worker 自己看自己"永远在干活"——同一段上下文接着问，模型只会重复它刚做的判断；
//   - planner 的职责是选方向，不是盯执行细节；它轮次稀疏，等它发现偏离时往往已经烧了
//     十几轮。审核员是一个独立的、短周期的视角。
//
// 失败策略是「本轮不纠偏」：审核员是附加保护，它挂了不该影响任务，也不该把不确定的
// 结论变成对 worker 的指令。与穷尽复核（失败向"继续跑"）同向：宁可少纠，不可误打断。

const (
	// defaultSupervisorInterval 是审核周期默认值。只在「有在跑意图且轨迹有新增」时
	// 才真正调模型，所以这个间隔是上限而不是调用频率。
	defaultSupervisorInterval = 3 * time.Minute
	minSupervisorInterval     = 60 * time.Second
	maxSupervisorInterval     = 30 * time.Minute
	// supervisorInitialDelay 是任务启动后第一次审核的延迟：刚领到的意图还没有轨迹，
	// 太早审核只会得到"证据不足"。
	supervisorInitialDelay = 2 * time.Minute
	// supervisorReviewTimeout 是单次审核的硬上限。裁判只读一小段轨迹，不该跑很久。
	supervisorReviewTimeout = 90 * time.Second
	// supervisorMinToolCalls 是「值得审核」的最少工具调用数：更少的意图还在热身。
	supervisorMinToolCalls = 3
	// supervisorSteerCooldown 是同一意图两次纠偏之间的最小间隔。纠偏是打断式注入，
	// 太密会让 worker 一直在"重新理解指令"，反而干不成活。
	supervisorSteerCooldown = 5 * time.Minute
	// supervisorMaxSteers 是同一意图最多纠偏次数。超过说明指令没能改变行为，
	// 该由 planner 决策换方向，而不是审核员继续喊。
	supervisorMaxSteers = 3
	// supervisorLane 是审核调用的用量分档名（与 judge/exhaustion-review 同理）。
	supervisorLane = "supervisor"
	// supervisorWorker 是审核活动在时间线/会话页里的归属名。会话页据此把审核过程
	// 单独成一个会话（session=supervisor），不混进系统审计。
	supervisorWorker = "supervisor"
)

// supervisorFunc 是审核员的可注入实现（测试用）。nil = 未接线（无可用 LLM）。
// raw 是模型的原始输出（解析失败时也返回），用于把「审核时到底说了什么」如实
// 记进时间线，而不是只留下一个解析错误。
type supervisorFunc func(ctx context.Context, t *Task, claim agent.SupervisorClaim) (agent.SupervisorVerdict, string, error)

// supervisorState 是单个任务的审核水位与纠偏节奏。
type supervisorState struct {
	mu sync.Mutex
	// lastAct 记录每条意图已经审核到的最大活动 id：没有新轨迹就不调模型。
	lastAct map[int64]int64
	// lastSteer / steers 是纠偏冷却与上限（每意图）。
	lastSteer map[int64]time.Time
	steers    map[int64]int
}

// SetSupervisor 注入执行审核员（见 server/supervisor.go）。nil = 不启用。
func (e *Engine) SetSupervisor(fn supervisorFunc) { e.supervisor = fn }

// supervisorLoop 是任务级的定时审核循环。它随任务运行时一起启动/拆除；任务暂停时
// 只是空转（tick 会跳过），恢复后无需特殊处理。
func (e *Engine) supervisorLoop(ctx context.Context, t *Task) {
	if e == nil || t == nil || e.supervisor == nil {
		return
	}
	interval := supervisorIntervalSetting(e.pg())
	delay := supervisorInitialDelay
	if interval < delay {
		delay = interval
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		e.supervisorTick(ctx, t)
		timer.Reset(supervisorIntervalSetting(e.pg()))
	}
}

// supervisorTick 执行一次审核。返回前不会阻塞任务：只有真正需要时才会调用模型。
func (e *Engine) supervisorTick(ctx context.Context, t *Task) {
	if e == nil || e.supervisor == nil || t == nil || t.Store == nil {
		return
	}
	if !supervisorEnabledSetting(e.pg()) || !e.constraintEnabled("supervisor") {
		return
	}
	if e.IsDeleting(t.ID) || e.IsPaused(t.ID) || e.isSettling(t.ID) {
		return
	}
	if e.m != nil && isTerminalStatus(e.m.TaskStatus(t.ID)) {
		return
	}
	running, err := t.Store.ListByKindState(db.KindIntent, "running", agent.SupervisorMaxIntents*2)
	if err != nil || len(running) == 0 {
		return
	}
	claim := agent.BuildSupervisorClaim(t.Store, t.Goal, running, agent.SupervisorStepLimit)
	if len(claim.Intents) == 0 {
		return
	}
	// 只审「有足够动作可判」的意图；刚领到的意图还看不出偏不偏。
	eligible := make([]agent.SupervisorIntent, 0, len(claim.Intents))
	for _, it := range claim.Intents {
		if it.Tools >= supervisorMinToolCalls {
			eligible = append(eligible, it)
		}
	}
	if len(eligible) == 0 {
		return
	}
	runningIDs := make([]int64, 0, len(running))
	for _, n := range running {
		if n != nil && n.Kind == db.KindIntent && n.State == "running" {
			runningIDs = append(runningIDs, n.ID)
		}
	}
	if !e.takeSupervisorFreshness(t.ID, runningIDs, eligible) {
		return
	}
	payload := agent.SupervisorClaim{Goal: claim.Goal, Goals: claim.Goals, Intents: eligible}
	// 审核过程本身写进 supervisor 会话：轮次分隔、送审输入、模型原始输出、纠偏动作。
	// 这是操作者要看的「它在审什么、看到了什么、结论是什么」，也是复核误判的依据。
	e.emitActivity(t, db.Activity{Worker: supervisorWorker, Kind: "round",
		Summary: fmt.Sprintf("审核 · %d 条在跑意图", len(eligible))})
	e.emitActivity(t, db.Activity{Worker: supervisorWorker, Kind: "supervisor_input",
		Summary: "审核输入：" + supervisorIntentRefs(eligible),
		Detail:  supervisorClaimJSON(payload)})

	rctx, cancel := context.WithTimeout(ctx, supervisorReviewTimeout)
	defer cancel()
	verdict, raw, err := e.supervisor(rctx, t, payload)
	if err != nil {
		detail := err.Error()
		if strings.TrimSpace(raw) != "" {
			detail += "\n\n原始输出：\n" + raw
		}
		e.emitActivity(t, db.Activity{Worker: supervisorWorker, Kind: "result", IsError: true,
			Summary: "审核失败（本轮不纠偏）：" + firstLine(err.Error(), 200), Detail: detail})
		log.Printf("[supervisor] task %s 审核失败，本轮不纠偏: %v", t.ID, err)
		return
	}
	// 模型的原始输出是这份会话最该被看见的东西：结论、理由、指令都在里面。
	e.emitActivity(t, db.Activity{Worker: supervisorWorker, Kind: "result",
		Summary: firstLine(raw, 400), Detail: raw})
	e.applySupervisorVerdict(t, eligible, verdict)
}

// supervisorIntentRefs 给「审核输入」一行摘要：这次都审了哪几条意图。
func supervisorIntentRefs(intents []agent.SupervisorIntent) string {
	ids := make([]string, 0, len(intents))
	for _, it := range intents {
		ids = append(ids, fmt.Sprintf("#%d", it.ID))
	}
	return fmt.Sprintf("任务目标 + %d 条在跑意图（%s）", len(intents), strings.Join(ids, "、"))
}

// takeSupervisorFreshness 判断本轮是否有值得调模型的新轨迹，并推进水位。
// 同时按当前在跑集合清理已结束意图的状态，避免 map 只增不减。
func (e *Engine) takeSupervisorFreshness(taskID string, runningIDs []int64, eligible []agent.SupervisorIntent) bool {
	st := e.supervisorStateFor(taskID)
	st.mu.Lock()
	defer st.mu.Unlock()
	fresh := false
	for _, it := range eligible {
		if it.LastActivityID > st.lastAct[it.ID] {
			fresh = true
			break
		}
	}
	running := make(map[int64]bool, len(runningIDs))
	for _, id := range runningIDs {
		running[id] = true
	}
	for _, it := range eligible {
		if it.LastActivityID > st.lastAct[it.ID] {
			st.lastAct[it.ID] = it.LastActivityID
		}
	}
	for id := range st.lastAct {
		if !running[id] {
			delete(st.lastAct, id)
		}
	}
	for id := range st.lastSteer {
		if !running[id] {
			delete(st.lastSteer, id)
			delete(st.steers, id)
		}
	}
	return fresh
}

// applySupervisorVerdict 把「偏离」判定变成对 worker 的纠偏指令。
//
// 只对真实存在于本轮输入里的意图生效：模型可能编出一个 id，拿它去 steer 会误伤。
func (e *Engine) applySupervisorVerdict(t *Task, intents []agent.SupervisorIntent, v agent.SupervisorVerdict) {
	byID := make(map[int64]agent.SupervisorIntent, len(intents))
	for _, it := range intents {
		byID[it.ID] = it
	}
	st := e.supervisorStateFor(t.ID)
	steered := 0
	for _, iv := range v.Intents {
		if iv.OnTrack || strings.TrimSpace(iv.Steer) == "" {
			continue
		}
		it, ok := byID[iv.IntentID]
		if !ok {
			continue
		}
		st.mu.Lock()
		last, n := st.lastSteer[iv.IntentID], st.steers[iv.IntentID]
		active := n < supervisorMaxSteers && (last.IsZero() || time.Since(last) >= supervisorSteerCooldown)
		st.mu.Unlock()
		if !active {
			continue
		}

		// 授权范围由平台 RoE/命令门判定，不由审核员 LLM 判断。模型若仍以「越界/未授权/
		// 第三方/范围外」为由要求停止，只留观察、不下发 steer——一次误判就会把正在推进的
		// 探索打断（用户实测）。真正的越界由命令门拦截，不靠审核员喊停。
		if agent.SupervisorScopeConcern(iv.Reason) || agent.SupervisorScopeConcern(iv.Steer) {
			e.emitActivity(t, db.Activity{Worker: supervisorWorker, Kind: "result",
				Summary: fmt.Sprintf("审核观察：意图 #%d 疑似越界，交平台门/规划者处理（未纠偏）", iv.IntentID),
				Detail:  "审核结论：" + iv.Reason + "\n\n（授权范围由平台 RoE/命令门判定；审核员不下发停止指令）"})
			continue
		}

		msg := strings.TrimSpace(iv.Steer)
		if err := e.SteerWorkSupervisor(iv.IntentID, msg); err != nil {
			// 竞态：意图刚被领取（work 尚未登记）或刚好收场。不消耗冷却/次数——
			// 下一次审核如果它还在跑且仍偏离，应该再试一次。
			log.Printf("[supervisor] task %s 意图 #%d 纠偏未投递: %v", t.ID, iv.IntentID, err)
			continue
		}
		st.mu.Lock()
		st.lastSteer[iv.IntentID] = time.Now()
		st.steers[iv.IntentID] = n + 1
		st.mu.Unlock()
		steered++
		detail := "审核结论：" + iv.Reason + "\n\n纠偏指令（已注入，下一步动作前生效）：\n" + msg + "\n\n" + supervisorEvidence(it)
		if n+1 >= supervisorMaxSteers {
			detail += "\n（本意图已连续纠偏 " + strconv.Itoa(n+1) + " 次，达到上限；若仍无改善，应由 planner 考虑换方向。）"
		}
		e.emitActivity(t, db.Activity{
			Worker: supervisorWorker, Kind: "supervisor_steer",
			Summary: fmt.Sprintf("已下发纠偏：意图 #%d —— %s", iv.IntentID, firstLine(msg, 120)),
			Detail:  detail,
		})
	}
	log.Printf("[supervisor] task %s 审核 %d 条在跑意图：纠偏 %d 条", t.ID, len(intents), steered)
}

// supervisorEvidence 渲染一小段轨迹供人复核（时间线详情用）。
func supervisorEvidence(it agent.SupervisorIntent) string {
	var b strings.Builder
	worker := it.Worker
	if worker == "" {
		worker = "未标注槽位"
	}
	fmt.Fprintf(&b, "意图 #%d（%s）「%s」\n", it.ID, worker, it.Summary)
	if it.Repeated != "" {
		fmt.Fprintf(&b, "重复动作：%s\n", it.Repeated)
	}
	if len(it.Steps) > 0 {
		start := len(it.Steps) - 5
		if start < 0 {
			start = 0
		}
		b.WriteString("最近动作：\n")
		for _, s := range it.Steps[start:] {
			b.WriteString("  " + s + "\n")
		}
	}
	return b.String()
}

func (e *Engine) supervisorStateFor(taskID string) *supervisorState {
	if v, ok := e.supervisorStates.Load(taskID); ok {
		return v.(*supervisorState)
	}
	v, _ := e.supervisorStates.LoadOrStore(taskID, &supervisorState{
		lastAct:   map[int64]int64{},
		lastSteer: map[int64]time.Time{},
		steers:    map[int64]int{},
	})
	return v.(*supervisorState)
}

// pg 返回管理端的数据库句柄（可能为 nil：纯内存测试）。
func (e *Engine) pg() *db.DB {
	if e == nil || e.m == nil {
		return nil
	}
	return e.m.pg
}

// supervisorEnabledSetting 读审核开关（默认开）。每条任务各自触发，关掉后不再调模型。
func supervisorEnabledSetting(pg *db.DB) bool {
	if pg == nil {
		return false
	}
	return pg.GetBool(settingSupervisorEnabled, true)
}

// supervisorIntervalSetting 读审核周期（秒），非法值回落到默认；运行时现读，
// 改设置后下一个周期生效，无需重启。
func supervisorIntervalSetting(pg *db.DB) time.Duration {
	if pg == nil {
		return defaultSupervisorInterval
	}
	v, ok, err := pg.GetSetting(settingSupervisorInterval)
	if err != nil || !ok {
		return defaultSupervisorInterval
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return defaultSupervisorInterval
	}
	d := time.Duration(n) * time.Second
	if d < minSupervisorInterval {
		return minSupervisorInterval
	}
	if d > maxSupervisorInterval {
		return maxSupervisorInterval
	}
	return d
}

// supervisorClaimJSON 把审核输入渲染成给裁判的 user 消息。
func supervisorClaimJSON(claim agent.SupervisorClaim) string {
	b, err := json.MarshalIndent(claim, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", claim)
	}
	return string(b)
}
