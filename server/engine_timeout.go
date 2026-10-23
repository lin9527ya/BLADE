package server

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/policy"
)

// 任务级超时协调器(见 docs/任务级超时与收尾设计.md §4/§8.5)。
// 绝对墙钟:每个带 timeout 的任务一个定时 goroutine,到点驱动有序收尾时序:
//   ① settling → ② worker 停领新意图 / ③ planner 丢弃普通 notify
//   ④ 等在跑 worker drain(受 grace) → ⑤ 终局一轮 planner 判定 → ⑥ 定终态(带守卫)

const (
	settleDrainGrace     = 90 * time.Second // 等在跑 worker 优雅收尾的上限;超过则硬 cancel
	deadlinePollInterval = 2 * time.Second  // deadline 未盖章/LLM 未就绪时的轮询间隔
	deadlineMaxSleep     = 30 * time.Second // 单次最长睡眠(便于周期复查终态)
	// intentLeaseRenewInterval 意图租约续租节流：每个活动最多每 TTL/3 续一次租（心跳=进度
	// 证据，见 runIntent）；leaseSweepInterval 是租约看门狗的扫描节奏。
	intentLeaseRenewInterval = db.IntentLeaseTTL / 3
	leaseSweepInterval       = 30 * time.Second
)

// ---------- settling 状态 ----------

func (e *Engine) isSettling(taskID string) bool {
	v, _ := e.settling.Load(taskID)
	b, _ := v.(bool)
	return b
}

// markSettling flips settling on; returns true only for the first caller.
func (e *Engine) markSettling(taskID string) bool {
	_, loaded := e.settling.LoadOrStore(taskID, true)
	return !loaded
}

// ---------- 在跑计数(worker.Execute + planner.Plan),用于 drain ----------

func (e *Engine) inflightCounter(taskID string) *int64 {
	v, _ := e.inflight.LoadOrStore(taskID, new(int64))
	return v.(*int64)
}

// beginTaskOperation atomically registers a task-owned operation unless deletion
// has already installed its barrier. The delete handler can therefore wait for
// inflight==0 without a check-then-start race recreating files after cleanup.
func (e *Engine) beginTaskOperation(taskID string) bool {
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.IsDeleting(taskID) {
		return false
	}
	atomic.AddInt64(e.inflightCounter(taskID), 1)
	return true
}

func (e *Engine) decInflight(taskID string) { atomic.AddInt64(e.inflightCounter(taskID), -1) }

// inflightTotal 是所有任务在跑 run 的总数（planner.Plan + worker.Execute）。关停 drain 用它
// 判断"还有没有人在收尾"。
func (e *Engine) inflightTotal() int64 {
	var total int64
	e.inflight.Range(func(_, v any) bool {
		if n, ok := v.(*int64); ok && n != nil {
			total += atomic.LoadInt64(n)
		}
		return true
	})
	return total
}

func (e *Engine) inflightCount(taskID string) int64 {
	return atomic.LoadInt64(e.inflightCounter(taskID))
}

// ---------- deadline ----------

// taskDeadline returns the task's absolute deadline (unix). Prefers the in-process
// map (stamped this session); falls back to the DB-loaded value (restart), seeding
// the map. 0 = no timeout / not yet stamped.
func (e *Engine) taskDeadline(t *Task) int64 {
	if v, ok := e.deadline.Load(t.ID); ok {
		return v.(int64)
	}
	deadlineAt := t.lifecycleSnapshot().DeadlineAt
	if deadlineAt > 0 {
		e.deadline.Store(t.ID, deadlineAt)
		return deadlineAt
	}
	return 0
}

// resetTimeoutRevival clears only the per-run timeout state after PostgreSQL has
// atomically committed timeout -> running and reset first_run_at/deadline_at. The
// configured TimeoutSeconds remains on Task, so the next real Planner/Worker run
// stamps a fresh full budget. coordStarted is reset because the coordinator that
// produced the timeout has already completed (or is in its final return path).
func (e *Engine) resetTimeoutRevival(taskID string) {
	e.settling.Delete(taskID)
	e.deadline.Delete(taskID)
	e.stamped.Delete(taskID)
	e.coordStarted.Delete(taskID)
	e.budgetStage.Delete(taskID)
}

// stampFirstRun records first_run_at + deadline_at on the FIRST real run (LLM ready)
// of a timeout task, once per process. No-op when the task has no timeout.
func (e *Engine) stampFirstRun(t *Task) {
	if t.TimeoutSeconds <= 0 {
		return
	}
	if _, loaded := e.stamped.LoadOrStore(t.ID, true); loaded {
		return
	}
	dl, err := e.m.StampTaskFirstRun(t.ID)
	if err != nil {
		log.Printf("[deadline] task %s 盖章 first_run 失败: %v", t.ID, err)
		e.stamped.Delete(t.ID) // 允许下次重试
		return
	}
	if dl > 0 {
		e.deadline.Store(t.ID, dl)
		log.Printf("[deadline] task %s 首次运行,截止于 %s", t.ID, time.Unix(dl, 0).Format("2006-01-02 15:04:05"))
	}
}

// clockCtx layers the task's TaskClock (absolute deadline) onto a run's context so
// worker/planner can clamp their wall-clock budget and pick per-run vs task-timeout
// wrap-up words. final marks the coordinator-driven terminal planner round.
func (e *Engine) clockCtx(base context.Context, t *Task, final bool) context.Context {
	dl := e.taskDeadline(t)
	if dl <= 0 && !final {
		return base // no timeout → unchanged behavior
	}
	return agent.WithTaskClock(base, agent.TaskClock{DeadlineUnix: dl, Final: final})
}

// ---------- 协调器 ----------

// startDeadlineCoordinator launches the per-task deadline timer once (idempotent).
// Called from Run() and from the restart reload path, so non-active timeout tasks
// still get settled after their deadline even without live planner/worker loops.
func (e *Engine) startDeadlineCoordinator(ctx context.Context, t *Task) {
	// 超时、token 预算与意图租约看门狗共用这一条协调器：什么都没配的任务也需要它跑
	// 看门狗（预算按设置逐轮判定，未配置则每轮是廉价读）。
	if t == nil {
		return
	}
	e.deleteMu.RLock()
	if e.IsDeleting(t.ID) {
		e.deleteMu.RUnlock()
		return
	}
	if _, loaded := e.coordStarted.LoadOrStore(t.ID, true); loaded {
		e.deleteMu.RUnlock()
		return
	}
	rt := e.registerTaskRoutines(ctx, t.ID, 1)
	e.deleteMu.RUnlock()
	runTaskRoutine(rt, func(loopCtx context.Context) { e.deadlineCoordinator(loopCtx, t) })
}

// deadlineCoordinator waits until the task's absolute deadline, then runs the settle
// sequence. Absolute wall-clock: it keeps counting through pauses.
func (e *Engine) deadlineCoordinator(ctx context.Context, t *Task) {
	var lastLeaseSweep time.Time // 零值 → 首轮立即扫一次，之后按 leaseSweepInterval 节流
	var lastStallCheck time.Time // 同上（P20 策略停摆判定）
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			return // already finished (goals met / failed) — nothing to time out
		}
		// 意图租约看门狗：独立节流(30s)，与 timeout/budget 的检查节奏解耦；收尾期间不扫
		// (drain 正在等 Worker 优雅收尾，别用回收打断它)。
		if time.Since(lastLeaseSweep) >= leaseSweepInterval && !e.isSettling(t.ID) {
			lastLeaseSweep = time.Now()
			e.sweepExpiredLeases(t)
		}
		// token 预算护栏：70/85/95% 各投递一次收敛提示；达到 100% **暂停任务**，
		// 不终止、不出终局报告——由人决定是否继续。用户继续后同一档预算不再重复打断；
		// 预算值被改过则重新生效（budgetAck 存的是当时的预算值）。
		if budget := e.tokenBudget(); budget > 0 {
			if acked, ok := e.budgetAck.Load(t.ID); !ok || acked.(int64) != budget {
				if used, err := e.m.pg.TokenUsedByTask(t.ID); err == nil {
					stage := BudgetStage(used, budget)
					e.budgetNotice(t, stage, used, budget)
					if stage == "exhausted" {
						e.budgetAck.Store(t.ID, budget)
						// 已经暂停（用户手动暂停/其它策略）就不重复打扰；恢复时
						// startAdmittedTask → AckExhaustedBudget 会接住这次确认。
						if !t.lifecycleSnapshot().Paused && !e.IsPaused(t.ID) {
							e.pauseForBudget(t, used, budget)
						}
					}
				}
			}
		}
		// P20 探索策略停摆：策略内已无可行方向（窗口内被拒达阈值、期间没有任何成功
		// 创建的意图、也没有在跑的活）——再唤醒 planner 只会继续烧 token，主动收尾。
		// 终态用 policy_blocked（不是 timeout）：改策略或加目标即可复活，UI 也该显示
		// "策略封死"而不是"超时"。
		if time.Since(lastStallCheck) >= policyStallCheckInterval && !e.isSettling(t.ID) {
			lastStallCheck = time.Now()
			if e.policyStall(t) {
				e.settlePolicyStall(ctx, t)
				return
			}
		}
		dl := e.taskDeadline(t)
		if dl <= 0 {
			if sleepCtx(ctx, deadlinePollInterval) { // not yet stamped (task hasn't really run)
				return
			}
			continue
		}
		if remaining := time.Until(time.Unix(dl, 0)); remaining > 0 {
			nap := remaining
			if nap > deadlineMaxSleep {
				nap = deadlineMaxSleep
			}
			if sleepCtx(ctx, nap) {
				return
			}
			continue
		}
		e.settleTask(ctx, t, "到达超时上限", "timeout")
		return
	}
}

// sweepExpiredLeases is the intent-lease watchdog: a running intent whose heartbeat
// stopped for longer than db.IntentLeaseTTL is a zombie (its worker hung or died). Each
// is cancelled with a named cause — a live-but-stalled run settles itself back to open —
// and reclaimed to 'open' in the DB so the pool re-runs it from its transcript.
func (e *Engine) sweepExpiredLeases(t *Task) {
	leases, err := t.Store.ExpiredIntentLeases()
	if err != nil || len(leases) == 0 {
		return
	}
	for _, l := range leases {
		// P13：这条意图还有托管长任务在跑——心跳停不是因为 worker 僵死，而是收尾门
		// 主动把意图留在 running 等执行落地。租约过期是预期内的（无人续租），回收会
		// 把意图从观察者手里抢走，跳过；执行收束后观察者自会放它回 open。
		if execs := e.m.Executions(); execs != nil && len(execs.ActiveForIntent(l.IntentID)) > 0 {
			continue
		}
		live := e.cancelWork(l.IntentID, agent.AbortWorkLeaseExpired)
		reopened, err := t.Store.ReopenExpiredIntent(l.IntentID, l.Owner)
		if err != nil {
			log.Printf("[lease] task %s 意图 #%d 回收失败: %v", t.ID, l.IntentID, err)
			continue
		}
		if !reopened {
			continue // 扫描与回收之间被续租/收尾——竞态输者静默退出
		}
		seen := "未知"
		if l.LastSeen != nil {
			seen = l.LastSeen.Format("2006-01-02 15:04:05")
		}
		log.Printf("[lease] task %s 意图 #%d(owner=%s) 心跳停止(最后 %s)，回收重派(live work=%v)", t.ID, l.IntentID, l.Owner, seen, live)
		uid := l.IntentID
		e.emitActivity(t, db.Activity{NodeID: &uid, Worker: "system", Kind: "system",
			Summary: "意图租约到期回收",
			Detail:  fmt.Sprintf("owner=%s 心跳停止超过 %s，意图已退回 open 重新领取（worker 从既有 transcript 续跑，已写入的产出保留）", l.Owner, db.IntentLeaseTTL)})
		t.Notify() // 图里出现了重新可领的意图 → 唤醒 planner
	}
}

// settlePolicyStall 是"策略停摆"的收尾动作：先留痕再走与超时/预算共用的收尾时序，
// 只是终态记 policy_blocked（"策略封死"与"超时"是两回事，改策略/加目标即可复活）。
func (e *Engine) settlePolicyStall(ctx context.Context, t *Task) {
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
		Summary: fmt.Sprintf("探索策略内已无可行方向（近 %s 内被拒 ≥ %d 条且无新增意图），任务进入收尾", policyStallWindow, policyStallMinRejects)})
	log.Printf("[policy] task %s 策略停摆，进入收尾时序", t.ID)
	e.settleTask(ctx, t, "探索策略内已无可行方向", "policy_blocked")
}

// policyStall 判定"策略内已无可行方向"：任务有非空策略、真的没活干（无待领/在跑意图、
// 无在跑执行或 run）、窗口内被拒达阈值且期间没有任何成功创建的意图。
// 抽成方法便于验收直接驱动（不用等真跑满一个窗口）。
func (e *Engine) policyStall(t *Task) bool {
	pgTask, err := e.m.pg.GetTask(parseIntDefault(t.ID))
	if err != nil || pgTask == nil {
		return false
	}
	doc := policy.ParseDoc(pgTask.ExplorationPolicy)
	if doc == nil || doc.Effective.IsZero() {
		return false // 无策略的任务永不受此影响
	}
	if active, err := t.Store.HasActiveIntent(); err != nil || active {
		return false
	}
	if x := e.m.Executions(); x != nil && len(x.ActiveForTask(t.ID)) > 0 {
		return false
	}
	if e.inflightCount(t.ID) > 0 {
		return false
	}
	rejects, err := t.Store.RecentPolicyRejects(50)
	if err != nil {
		return false
	}
	cutoff := time.Now().Add(-policyStallWindow)
	recent := 0
	oldest := time.Time{}
	for _, r := range rejects {
		if r.CreatedAt.Before(cutoff) {
			continue
		}
		recent++
		if oldest.IsZero() || r.CreatedAt.Before(oldest) {
			oldest = r.CreatedAt
		}
	}
	if recent < policyStallMinRejects {
		return false
	}
	// 窗口起点之后成功创建过意图 → 探索仍在推进，只是偶尔被拒。
	if last, err := t.Store.LastIntentCreatedAt(); err == nil && last != nil && last.After(cutoff) {
		return false
	}
	return time.Since(oldest) >= policyStallGrace
}

// settleTask runs the ordered settle sequence once (§4 steps ①–⑥).
//
// why 说明"为什么收尾"（超时/预算耗尽/探索策略停摆），进日志与时间线；
// fallback 是非 met 时的终态（默认 timeout；策略停摆用 policy_blocked——
// 把"策略封死"显示成"已超时"是事实错误，而本设计的全部价值就在理由明确）。
func (e *Engine) settleTask(ctx context.Context, t *Task, why string, fallback string) {
	if !e.markSettling(t.ID) {
		return
	}
	if why == "" {
		why = "到达超时上限"
	}
	if fallback == "" {
		fallback = "timeout"
	}
	log.Printf("[deadline] task %s %s,进入收尾时序", t.ID, why)

	// ④ 等在跑 worker/planner drain(在跑 run 因夹逼的 MaxDuration 自行进收尾);
	// 超过 grace 仍未清空 → 硬 cancel 该任务 exec ctx(settling-aware 分支正确归类)。
	hardStop := time.Now().Add(settleDrainGrace)
	for {
		execs := e.m.Executions()
		pendingExecs := 0
		if execs != nil {
			pendingExecs = len(execs.ActiveForTask(t.ID))
		}
		if e.inflightCount(t.ID) == 0 && pendingExecs == 0 {
			break
		}
		if time.Now().After(hardStop) {
			log.Printf("[deadline] task %s drain 超时(%s),硬取消在跑 run(%d 个执行在跑)", t.ID, settleDrainGrace, pendingExecs)
			e.cancelExec(t.ID, agent.AbortSettleDrainTimeout)
			// P13：任务收尾不等长任务——托管执行一并取消（收尾后没人再读它的输出）。
			if n := e.m.CancelTaskExecutions(t.ID, "任务收尾，执行被取消"); n > 0 {
				log.Printf("[deadline] task %s 收尾取消 %d 个在跑执行", t.ID, n)
			}
			_ = sleepCtx(ctx, 3*time.Second) // 给 worker 分支一点时间落库/归类
			break
		}
		if sleepCtx(ctx, 500*time.Millisecond) {
			return // 引擎整体关停
		}
	}

	// ⑤ 终局一轮 planner(任务超时词,最后目标判定,不产新意图)。
	met := e.runFinalPlannerRound(ctx, t)
	if !e.beginTaskOperation(t.ID) {
		return
	}
	defer e.decInflight(t.ID)

	// ⑥ 定终态(带守卫):met → done(completed);否则 timeout。若常规路径已先落 done,
	// 守卫(SetTaskStatusGuarded)会拒绝覆盖,保留 completed 语义。
	status := fallback
	if met {
		status = "done"
	}
	won, err := e.m.SetTaskStatusGuarded(t.ID, status)
	switch {
	case err != nil:
		log.Printf("[deadline] task %s 落终态失败: %v", t.ID, err)
	case won:
		log.Printf("[deadline] task %s 收尾完成,终态=%s", t.ID, status)
	default:
		log.Printf("[deadline] task %s 收尾时已是终态,保留原状态", t.ID)
	}
}

// runFinalPlannerRound drives exactly ONE terminal planner round with the
// task-timeout planner words (final goal judgment; no new intents). Waits for the
// LLM to be ready (bounded by ctx) so a completable task isn't mis-judged timeout.
func (e *Engine) runFinalPlannerRound(ctx context.Context, t *Task) (met bool) {
	if e.IsDeleting(t.ID) {
		return false
	}
	planner, _ := e.snapshotFor(t)
	for planner == nil {
		if sleepCtx(ctx, deadlinePollInterval) {
			return false
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			return false
		}
		if e.IsDeleting(t.ID) {
			return false
		}
		planner, _ = e.snapshotFor(t)
	}
	// 独立 ctx(不挂 execCancel,避免 pause/硬 cancel 打断这最后一轮),带 Final 注入任务超时词。
	fctx := e.clockCtx(ctx, t, true)
	if !e.beginTaskOperation(t.ID) {
		return false
	}
	defer e.decInflight(t.ID)
	emit := func(r db.Activity) { e.emitActivity(t, r) }
	e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
		Summary: fmt.Sprintf("任务超时收尾·终局判定(第 %d 轮)", e.nextPlannerRound(t.ID))})
	tTaskID, _ := strconv.ParseInt(t.ID, 10, 64)
	e.BeginLLMCall(t.ID)
	met, reason, err := planner.Plan(fctx, tTaskID, e.m.assets, t.Store, t.Goal, t.drainTriggers(), emit)
	e.EndLLMCall(t.ID)
	if err != nil {
		log.Printf("[deadline] task %s 终局规划出错: %v", t.ID, err)
	} else if met {
		log.Printf("[deadline] task %s 终局判定目标达成: %s", t.ID, reason)
	}
	return met
}
