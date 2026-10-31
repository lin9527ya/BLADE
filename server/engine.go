package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/intercept"
)

// isFKViolation reports whether err is a Postgres foreign-key violation (SQLSTATE
// 23503) — e.g. an activity insert whose exploration_id has no parent row.
func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// dropReason classifies why an activity write was dropped, so the log can be
// grouped/analysed by cause rather than by raw error text.
func dropReason(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return "fk_violation(23503,父exploration不存在)"
		case "23505":
			return "unique_violation(23505)"
		default:
			return "pg_error(" + pgErr.Code + ")"
		}
	}
	return "write_error"
}

// bumpDrop increments and returns the running count of dropped (unpersistable)
// activity records for a task. Concurrent planner + worker emits race here, so the
// counter is an atomic behind sync.Map. The count in the log shows loss scale at a
// glance instead of forcing a grep-and-count.
func (e *Engine) bumpDrop(taskID string) int64 {
	v, _ := e.dropCnt.LoadOrStore(taskID, new(int64))
	return atomic.AddInt64(v.(*int64), 1)
}

// preview collapses newlines and trims s to a short rune-safe snippet for one-line
// log output (avoids dumping a multi-KB summary/detail into the log).
func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// model_error（provider/API 故障：LLM 层瞬时重试耗尽，或流已开始后中途断流）
// 收场的 work 不是「试过没做完」也不是真失败，而是外部抖动。默认把它当永久
// blocked 会白丢一条意图，所以这里对该终态额外重跑几次，每次之间退避一下，给
// provider 恢复的时间；重试期间若被暂停/终止/取消则立即让位给对应分支处理。
const (
	modelErrorRetries      = 2               // model_error 收场后额外重试的次数
	modelErrorRetryBackoff = 3 * time.Second // 每次重试前的退避
	workControlWaitTimeout = 30 * time.Second

	// P13 执行观察者：托管长任务落终态后把意图退回 open 的轮询节奏与守望上限
	// （上限只是兜底，正常路径由 StopTask/任务收尾取消执行而立即收束）。
	execWatchPoll = 5 * time.Second
	execWatchMax  = 24 * time.Hour
)

var errWorkControlConflict = errors.New("work control conflict")
var errSlotBusy = errors.New("worker slot busy")

// retryableWorkerModelError excludes errors already handled by the task router.
// In particular, a quota error after partial streaming advances the task cursor
// for the next LLM call but must not replay this whole intent on the backup.
func retryableWorkerModelError(reason harness.TerminalReason, err error) bool {
	return reason == harness.ReasonModelError && !isTaskLLMRuntimeError(err)
}

// Engine drives the event-driven exploration loop with real LLM agents
// (docs §4.3/§4.4): on asset/exploration-graph change (debounced) it wakes the
// planner, which reads the route, queries assets, judges goals and emits intents;
// N concurrent work agents claim intents and execute them. There is no
// simulation mode — an LLM provider is required. The planner/worker can be
// (re)installed at runtime (LLM configured from the UI); the loops always run
// but idle until an LLM is set.
type Engine struct {
	m        *Manager
	debounce time.Duration

	// onStop 是 StopTask 在拆掉任务运行时之后调用的钩子（Server 用它等任务名下的
	// 后台压缩收尾）。Engine 本身看不到 Planner，钩子把依赖方向留在 Server 侧。
	onStop func(taskID string)

	// baseMu 保护 baseCtx：任务没有运行时条目时的兜底 ctx（见 runtimeCtx）。
	baseMu  sync.RWMutex
	baseCtx context.Context

	bc *Broadcaster // live activity pub/sub (SSE)

	started  sync.Map // taskID -> bool, so Run is idempotent per task
	lastAct  sync.Map // taskID -> int64 unix, last planner/worker activity (heartbeat)
	llmCalls sync.Map // taskID -> *int64, actual planner/worker/main-agent LLM calls
	paused   sync.Map // taskID -> bool, user-paused (planner + workers idle but loops alive)
	deleting sync.Map // taskID -> bool, delete barrier (no new task-owned writes)
	dropCnt  sync.Map // taskID -> *int64, running count of dropped (unpersistable) activity records
	// completionWarn 记录「收官被遗留物门拦下」已留痕的任务，避免 goalless 分支每次
	// 心跳都重复写同一条活动。
	completionWarn sync.Map
	// WAF 自动切换出口的窗口计数（task|host -> 最近命中时间）。切一次清一次，
	// 避免连续 403 把节点切来切去。
	wafMu       sync.Mutex
	wafHits     map[string][]time.Time
	wafRotating bool

	// deleteMu makes installing the delete barrier atomic with registering a new
	// task operation. Once BeginDelete returns, every admitted writer is reflected
	// in inflight and every later writer is rejected.
	deleteMu sync.RWMutex

	// Every long-lived task goroutine (planner, workers and deadline coordinator)
	// runs under one task-scoped context. Successful deletion cancels that context,
	// waits for all goroutines, then releases every task-level Engine reference.
	runtimeMu sync.Mutex
	runtimes  map[string]*taskRuntime
	// fallbackSlotLocks serializes detached runs for a task that has no live runtime
	// (not started yet, or already torn down). Without it, a human resume on such a
	// task would report every slot busy because slotLock returns nil.
	fallbackSlotMu    sync.Mutex
	fallbackSlotLocks map[string]*sync.Mutex

	// per-task execution context: each planner.Plan / worker.Execute runs under it,
	// so pausing can CANCEL an in-flight run (not just skip the next one). Recreated
	// on resume since cancelling is one-shot. Every cancellation carries a named
	// cause so the activity trace can identify the initiating control path.
	execMu     sync.Mutex
	execCancel map[string]context.CancelCauseFunc
	execCtx    map[string]context.Context

	// Per-work control lets the planner kill a worker and lets the UI pause/cancel
	// one intent without pausing the whole task. The done channel closes only after
	// runWorkerStep has stopped writing and committed its final state.
	workMu sync.Mutex
	work   map[int64]*workExecution

	// steerBox queues mid-run instructions for a running work (keyed by intent id) —
	// planner's steer_work and the operator's message from the sessions page.
	// The worker's PreToolUse hook drains it before its next tool call and hands the
	// message to the model (blocking that call) so it re-plans — no kill needed.
	// 每条带来源，见 steerMsg：worker 看到的归属文案必须如实。
	steerMu  sync.Mutex
	steerBox map[int64][]steerMsg

	plannerRound sync.Map // taskID -> int, planner round counter (for UI round separators)

	// 任务级超时(见 docs/任务级超时与收尾设计.md):
	settling     sync.Map // taskID -> bool, 任务已进入收尾时序(停止派/领新意图)
	deadline     sync.Map // taskID -> int64 unix, 绝对截止时刻(首次运行时盖章;0/缺省=不限)
	stamped      sync.Map // taskID -> bool, first_run_at 是否已盖章(本进程内只盖一次)
	inflight     sync.Map // taskID -> *int64, 在跑的 planner.Plan + worker.Execute 计数(用于 drain)
	coordStarted sync.Map // taskID -> bool, deadline 协调器是否已启动(Run/reload 去重)
	budgetStage  sync.Map // taskID -> string, token 预算提示已投递到的最高档(每档一次)
	budgetAck    sync.Map // taskID -> int64, 用户已确认"超支后继续"的预算值（同档不再重复暂停）
	execWatch    sync.Map // intentID -> bool, P13 执行观察者已注册(去重;执行终态→意图退回 open)
	// budgetPause 由 server 注入：预算耗尽时走完整的"任务暂停"路径（落库、取消主 Agent、
	// 重新对账并发），而不是在引擎里只置一个内存标记。nil = 未接线（测试/旧构造）。
	budgetPause func(*Task) error
	// pauseWithReason 由 server 注入：规划层判定"策略穷尽/停滞"时走完整暂停路径
	// （落库 paused、取消主 Agent、reconcile）。nil 时引擎只做内存暂停。
	pauseWithReason func(*Task, string) error

	// resolve returns a task's dedicated planner/worker (wired by the server as the
	// authoritative task-router). nil,nil means this task is deliberately unavailable
	// (for example an exhausted failover chain) — there is no global-pair fallback.
	resolve              func(t *Task) (*agent.Planner, *agent.Worker)
	resolveAuthoritative bool
	// readiness reports whether a global LLM provider is configured — the signal behind
	// Ready()/the llm_configured indicator. Wired once at startup; nil → not ready.
	readiness func() bool
}

type taskRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// workers 是本任务**启动时**的 worker 池大小（队列背压用）。改设置只影响之后启动的
	// 任务，所以不能让 planner 去读实时设置——那会按错误容量排队。
	workers int
	// slotLocks serializes one worker slot's runs. The N worker loops are already
	// serial per slot; this additionally keeps a detached human-resume run from
	// sharing the slot transcript concurrently with the pool.
	slotMu    sync.Mutex
	slotLocks map[string]*sync.Mutex
}

type workExecution struct {
	cancel context.CancelCauseFunc
	done   chan error
	action string // user action: pause | cancel
}

// nextPlannerRound returns the next planner round number for a task (1-based).
func (e *Engine) nextPlannerRound(taskID string) int {
	v, _ := e.plannerRound.LoadOrStore(taskID, 0)
	n := v.(int) + 1
	e.plannerRound.Store(taskID, n)
	return n
}

// Pause stops a task: marks it paused AND cancels any in-flight planner/worker run
// for it (a long worker.Execute would otherwise keep going until it finishes).
func (e *Engine) Pause(taskID string, cause error) {
	e.paused.Store(taskID, true)
	e.cancelExec(taskID, cause)
}

// BeginDelete installs an execution barrier before task data/files are removed.
// The temporary pause is not a user pause. The server serializes this transition
// with lifecycle admission and tells AbortDelete whether the persisted task is
// paused/queued if cleanup fails.
func (e *Engine) BeginDelete(taskID string) bool {
	e.deleteMu.Lock()
	if _, loaded := e.deleting.LoadOrStore(taskID, true); loaded {
		e.deleteMu.Unlock()
		return false
	}
	e.paused.Store(taskID, true)
	e.deleteMu.Unlock()
	e.cancelExec(taskID, agent.AbortTaskDeleted)
	return true
}

func (e *Engine) AbortDelete(taskID string, keepPaused bool) {
	e.deleteMu.Lock()
	if !e.IsDeleting(taskID) {
		e.deleteMu.Unlock()
		return
	}
	e.deleting.Delete(taskID)
	if !keepPaused {
		e.paused.Delete(taskID)
	}
	e.deleteMu.Unlock()
	if !keepPaused && e.m != nil {
		if t, ok := e.m.Task(taskID); ok {
			t.Notify()
		}
	}
}

func (e *Engine) IsDeleting(taskID string) bool {
	_, ok := e.deleting.Load(taskID)
	return ok
}

// registerTaskRoutines reserves count goroutines in the task runtime. Callers
// hold deleteMu for reading so StopTask cannot race WaitGroup.Add with Wait.
func (e *Engine) registerTaskRoutines(parent context.Context, taskID string, count int) *taskRuntime {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	rt := e.runtimes[taskID]
	if rt == nil {
		ctx, cancel := context.WithCancel(parent)
		rt = &taskRuntime{ctx: ctx, cancel: cancel, slotLocks: map[string]*sync.Mutex{}}
		e.runtimes[taskID] = rt
	}
	rt.wg.Add(count)
	return rt
}

func runTaskRoutine(rt *taskRuntime, fn func(context.Context)) {
	goSafe("engine/task-routine", &rt.wg, func() { fn(rt.ctx) })
}

// setTaskWorkers 记录本任务的 worker 池大小（池在 Run 时定死）。
func (e *Engine) setTaskWorkers(taskID string, n int) {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	if rt := e.runtimes[taskID]; rt != nil {
		rt.workers = n
	}
}

// TaskWorkers 返回本任务启动时的 worker 池大小（0 = 未知：任务没启动或已拆除）。
// planner 用它做队列背压；0 表示不启用上限。
func (e *Engine) TaskWorkers(taskID string) int {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	if rt := e.runtimes[taskID]; rt != nil {
		return rt.workers
	}
	return 0
}

// slotLock returns the per-task mutex for one worker slot. nil when the task runtime
// is already torn down (the caller should stop).
func (e *Engine) slotLock(taskID, name string) *sync.Mutex {
	e.runtimeMu.Lock()
	rt := e.runtimes[taskID]
	if rt == nil {
		e.runtimeMu.Unlock()
		// no pool is running, but detached human runs still share the transcript:
		// serialize them per task+slot too.
		e.fallbackSlotMu.Lock()
		defer e.fallbackSlotMu.Unlock()
		if e.fallbackSlotLocks == nil {
			e.fallbackSlotLocks = map[string]*sync.Mutex{}
		}
		key := taskID + "|" + name
		mu := e.fallbackSlotLocks[key]
		if mu == nil {
			mu = &sync.Mutex{}
			e.fallbackSlotLocks[key] = mu
		}
		return mu
	}
	defer e.runtimeMu.Unlock()
	rt.slotMu.Lock()
	defer rt.slotMu.Unlock()
	if rt.slotLocks == nil {
		rt.slotLocks = map[string]*sync.Mutex{}
	}
	mu := rt.slotLocks[name]
	if mu == nil {
		mu = &sync.Mutex{}
		rt.slotLocks[name] = mu
	}
	return mu
}

// slotNumber parses work#N; 0 when the name is not a slot (e.g. legacy "chat").
func slotNumber(name string) int {
	if !strings.HasPrefix(name, "work#") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(name, "work#"))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func slotName(n int) string { return fmt.Sprintf("work#%d", n) }

func slotPool(n int) []string {
	if n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, slotName(i))
	}
	return out
}

// runtimeCtx returns the task's runtime context; when the task has no registered
// runtime (never started, or already torn down) it falls back to the engine's
// base context (the Server ctx).
//
// 长生命周期助手必须用它，而不是 context.Background()：Background 的 Done() 是 nil
// channel，select 里那个分支永远等不到，等于这个 goroutine 无法被取消——StopTask
// 调 rt.cancel() 也打断不了它。
//
// 兜底用 baseCtx（进程/Server 级）而不是 Background：没有运行时条目时任务可能只是
// 还没启动，不该让观察者立刻退出；但关停时必须能一起停下——此前返回 Background
// 让执行观察者变成不可取消的，每 5s 查库、最长 24 小时，跨过关停与关池。
func (e *Engine) runtimeCtx(taskID string) context.Context {
	e.runtimeMu.Lock()
	rt := e.runtimes[taskID]
	e.runtimeMu.Unlock()
	if rt != nil {
		return rt.ctx
	}
	e.baseMu.RLock()
	base := e.baseCtx
	e.baseMu.RUnlock()
	if base != nil {
		return base
	}
	return context.Background()
}

// SetBaseContext 登记 Engine 级兜底 ctx（Server.New 用 Server 的 ctx 调用）。
// 它只影响「任务没有运行时条目」时的兜底，任务自己的运行时段仍优先。
func (e *Engine) SetBaseContext(ctx context.Context) {
	e.baseMu.Lock()
	e.baseCtx = ctx
	e.baseMu.Unlock()
}

// StopTask permanently stops every long-lived goroutine and removes all Engine
// state for a successfully deleted task. The delete barrier remains installed
// until cleanup finishes, so no new task operation can race the teardown.
func (e *Engine) StopTask(taskID string) {
	e.deleteMu.Lock()
	e.deleting.Store(taskID, true)
	e.deleteMu.Unlock()

	// P13：任务停止/删除时把托管的长任务也收掉（执行进程独立于 agent run 存活，
	// 不会随 run 结束自然消失）。暂停不在此列——暂停不误杀长任务。
	if n := e.m.CancelTaskExecutions(taskID, "任务已停止，执行被取消"); n > 0 {
		log.Printf("[exec] task %s 停止：取消 %d 个在跑执行", taskID, n)
	}

	e.cancelExec(taskID, agent.AbortTaskDeleted)
	e.runtimeMu.Lock()
	rt := e.runtimes[taskID]
	if rt != nil {
		rt.cancel()
	}
	e.runtimeMu.Unlock()
	if rt != nil {
		rt.wg.Wait()
	}
	// 后台冷节点压缩不挂在 run 的 ctx 上（它刻意活过一轮 planner），所以 rt.wg 等不到
	// 它。任务拆掉之后它仍会写探索图，必须在这里有界地等它收尾。
	if e.onStop != nil {
		e.onStop(taskID)
	}

	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel(agent.AbortTaskDeleted)
	}
	delete(e.execCancel, taskID)
	delete(e.execCtx, taskID)
	e.execMu.Unlock()

	e.runtimeMu.Lock()
	if e.runtimes[taskID] == rt {
		delete(e.runtimes, taskID)
	}
	e.runtimeMu.Unlock()

	e.started.Delete(taskID)
	e.lastAct.Delete(taskID)
	e.llmCalls.Delete(taskID)
	e.paused.Delete(taskID)
	e.dropCnt.Delete(taskID)
	e.budgetAck.Delete(taskID)
	e.plannerRound.Delete(taskID)
	e.settling.Delete(taskID)
	e.deadline.Delete(taskID)
	e.stamped.Delete(taskID)
	e.inflight.Delete(taskID)
	e.coordStarted.Delete(taskID)
	e.deleteMu.Lock()
	e.deleting.Delete(taskID)
	e.deleteMu.Unlock()
}

// cancelExec cancels a task's current per-task exec context (any in-flight
// planner.Plan / worker.Execute), if present. Shared by Pause and the settle
// sequence's hard-drain backstop.
func (e *Engine) cancelExec(taskID string, cause error) {
	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel(cause)
	}
	e.execMu.Unlock()
}

// Resume un-pauses a task and nudges a fresh planning round. The next exec under
// it gets a fresh (uncancelled) context.
func (e *Engine) Resume(t *Task) {
	// BeginDelete owns the pause barrier once deletion starts. A concurrent
	// resume must never clear it and let a planner/worker re-enter while cleanup
	// is waiting for task operations to drain.
	if t == nil {
		return
	}
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.IsDeleting(t.ID) {
		return
	}
	e.paused.Delete(t.ID)
	t.Notify()
}

// execContextFor returns a live per-task context derived from parent, recreating
// it if a prior pause cancelled it.
func (e *Engine) execContextFor(parent context.Context, taskID string) context.Context {
	e.execMu.Lock()
	defer e.execMu.Unlock()
	if e.IsPaused(taskID) {
		// never hand out a live context while paused (guards the claim→Execute race)
		c, cancel := context.WithCancelCause(parent)
		cancel(agent.AbortPausedRaceGuard)
		return c
	}
	if c := e.execCtx[taskID]; c != nil && c.Err() == nil {
		return c
	}
	c, cancel := context.WithCancelCause(parent)
	e.execCtx[taskID] = c
	e.execCancel[taskID] = cancel
	return c
}

// IsPaused reports whether a task is user-paused.
func (e *Engine) IsPaused(taskID string) bool {
	v, ok := e.paused.Load(taskID)
	return ok && v.(bool)
}

// Started reports whether the engine loops are running for a task.
func (e *Engine) Started(taskID string) bool {
	_, ok := e.started.Load(taskID)
	return ok
}

// LastActivity returns the unix time of the last planner/worker activity for a
// task (0 if none yet).
func (e *Engine) LastActivity(taskID string) int64 {
	if v, ok := e.lastAct.Load(taskID); ok {
		return v.(int64)
	}
	return 0
}

// BeginLLMCall/EndLLMCall track actual provider calls separately from the
// scheduler's task-operation counter. A task can have live loops while all of
// them are waiting for a trigger; that state must remain idle in the UI.
func (e *Engine) BeginLLMCall(taskID string) {
	v, _ := e.llmCalls.LoadOrStore(taskID, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

func (e *Engine) EndLLMCall(taskID string) {
	if v, ok := e.llmCalls.Load(taskID); ok {
		p := v.(*int64)
		if atomic.AddInt64(p, -1) <= 0 {
			atomic.StoreInt64(p, 0)
		}
	}
}

func (e *Engine) ActiveLLMCalls(taskID string) int64 {
	if v, ok := e.llmCalls.Load(taskID); ok {
		return atomic.LoadInt64(v.(*int64))
	}
	return 0
}

// 单轮规划的重试策略：两次尝试、间隔 3 秒。再多会拖长这一轮，而下一轮心跳本来就会重来。
const (
	plannerAttempts     = 2
	plannerRetryBackoff = 3 * time.Second
)

func (e *Engine) touch(taskID string) { e.lastAct.Store(taskID, time.Now().Unix()) }

func NewEngine(m *Manager) *Engine {
	return &Engine{m: m, debounce: 800 * time.Millisecond, bc: NewBroadcaster(),
		execCancel: map[string]context.CancelCauseFunc{}, execCtx: map[string]context.Context{},
		work: map[int64]*workExecution{}, steerBox: map[int64][]steerMsg{},
		runtimes: map[string]*taskRuntime{}}
}

// registerWork records the cancel for the work currently running intentID.
func (e *Engine) registerWork(intentID int64, cancel context.CancelCauseFunc) {
	e.workMu.Lock()
	e.work[intentID] = &workExecution{cancel: cancel, done: make(chan error, 1)}
	e.workMu.Unlock()
}

// cancelWork cancels the work currently running intentID with the given named cause —
// the lease watchdog's way to stop a stalled run. Returns whether a live work was
// registered at that moment; a dead goroutine has none, and the watchdog's DB-side
// reclaim stands either way.
func (e *Engine) cancelWork(intentID int64, cause error) bool {
	e.workMu.Lock()
	run := e.work[intentID]
	e.workMu.Unlock()
	if run == nil {
		return false
	}
	run.cancel(cause)
	return true
}

// detachWork removes the live control handle once Execute has returned. complete
// must be called after the final intent state write so a waiting cancel handler can
// safely delete the worker's blackboard output without racing a late write.
func (e *Engine) detachWork(intentID int64) (action string, complete func(error)) {
	e.workMu.Lock()
	run := e.work[intentID]
	if run != nil {
		delete(e.work, intentID)
		action = run.action
		run.cancel(agent.AbortWorkFinished) // release resources (no-op if already cancelled)
	}
	e.workMu.Unlock()
	e.steerMu.Lock()
	delete(e.steerBox, intentID) // drop any undelivered steering for a finished work
	e.steerMu.Unlock()
	if run == nil {
		return action, func(error) {}
	}
	return action, func(err error) { run.done <- err }
}

// ControlWork requests a user-visible pause or cancellation and waits until the
// worker has fully stopped writing. Cancellation cleanup is performed by the API
// handler after this returns; pause state is committed by runWorkerStep itself.
func (e *Engine) ControlWork(ctx context.Context, intentID int64, action string) error {
	if action != "pause" && action != "cancel" {
		return fmt.Errorf("unsupported work action %q", action)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.workMu.Lock()
	run := e.work[intentID]
	if run == nil {
		e.workMu.Unlock()
		return fmt.Errorf("%w: 意图 %d 当前没有运行中的 work（可能已结束或未被领取）", errWorkControlConflict, intentID)
	}
	if run.action != "" {
		e.workMu.Unlock()
		return fmt.Errorf("%w: 意图 %d 正在执行 %s 操作", errWorkControlConflict, intentID, run.action)
	}
	run.action = action
	done := run.done
	cause := error(agent.AbortWorkPausedByUser)
	if action == "cancel" {
		cause = agent.AbortWorkCancelledByUser
	}
	run.cancel(cause)
	e.workMu.Unlock()

	timer := time.NewTimer(workControlWaitTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		e.releaseWorkControl(intentID, run, action)
		return fmt.Errorf("等待意图 %d %s 收尾: %w", intentID, action, ctx.Err())
	case <-timer.C:
		e.releaseWorkControl(intentID, run, action)
		return fmt.Errorf("等待意图 %d %s 收尾: %w", intentID, action, context.DeadlineExceeded)
	}
}

// releaseWorkControl drops only this caller's reservation after its wait is
// cancelled. The work context stays cancelled; runWorkerStep recognizes the
// named cancellation cause and settles the intent into the recoverable paused
// state even if the HTTP caller has gone away.
func (e *Engine) releaseWorkControl(intentID int64, run *workExecution, action string) {
	e.workMu.Lock()
	if current := e.work[intentID]; current == run && current.action == action {
		current.action = ""
	}
	e.workMu.Unlock()
}

func transitionIntentState(store *db.ExplorationStore, intentID int64, expected, state string) error {
	changed, err := store.CompareAndSetIntentState(intentID, expected, state)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: 意图 %d 不再是 %s 状态", db.ErrIntentStateConflict, intentID, expected)
	}
	return nil
}

// steerSource 标明这条即时指令是谁下的——它决定 worker 看到的归属文案。两者都是
// "冲突时以此为准"，但来源不同：把人工提示写成规划者发的，等于让 worker 误判上级。
type steerSource int

const (
	steerFromPlanner steerSource = iota // planner / 主 Agent 的 steer_work 工具
	steerFromHuman                      // 人在会话页直接对运行中的 Worker 发的提示
)

// steerMsg 是一条待投递的即时指令（FIFO 队列元素）。
type steerMsg struct {
	from steerSource
	text string
}

// render 把指令渲染成模型看到的那段话。规划者那条的文案**保持原样**（既有提示词与
// 测试都依赖它），人工那条另起一套归属。
func (m steerMsg) render() string {
	const tail = "；本次工具调用未执行，请据此调整下一步。若与你当前打算冲突，以此为准。）"
	if m.from == steerFromHuman {
		return "【人工实时提示】" + m.text + "\n（这是人（操作者）对本意图的即时指令" + tail
	}
	return "【规划者实时纠偏】" + m.text + "\n（这是规划者对本意图的即时指令" + tail
}

// SteerWork queues a mid-run course-correction for the work running intentID (the
// planner's steer_work tool). The worker delivers it before its next tool call and
// re-plans — no kill. Errors if no work is currently running that intent.
func (e *Engine) SteerWork(intentID int64, msg string) error {
	return e.steer(intentID, steerMsg{from: steerFromPlanner, text: msg})
}

// SteerWorkHuman queues a mid-run hint authored by the operator (会话页对运行中的 Worker
// 直接发的那条消息). 与 planner 的纠偏走同一条投递路径与同一个队列，只有归属文案不同。
// 没有在跑的 work 时如实报错，不做静默排队——调用方据此让用户重发。
func (e *Engine) SteerWorkHuman(intentID int64, msg string) error {
	return e.steer(intentID, steerMsg{from: steerFromHuman, text: msg})
}

func (e *Engine) steer(intentID int64, msg steerMsg) error {
	if strings.TrimSpace(msg.text) == "" {
		return fmt.Errorf("提示消息不能为空")
	}
	e.workMu.Lock()
	running := e.work[intentID] != nil
	e.workMu.Unlock()
	if !running {
		return fmt.Errorf("意图 %d 当前没有运行中的 work（可能已结束或未被领取）", intentID)
	}
	e.steerMu.Lock()
	e.steerBox[intentID] = append(e.steerBox[intentID], msg)
	e.steerMu.Unlock()
	return nil
}

// drainSteer pops the oldest queued steering message for intentID (FIFO), if any.
func (e *Engine) drainSteer(intentID int64) (steerMsg, bool) {
	e.steerMu.Lock()
	defer e.steerMu.Unlock()
	q := e.steerBox[intentID]
	if len(q) == 0 {
		return steerMsg{}, false
	}
	msg := q[0]
	if len(q) == 1 {
		delete(e.steerBox, intentID)
	} else {
		e.steerBox[intentID] = q[1:]
	}
	return msg, true
}

// steerHooks wraps the guard's hook runner so the planner can steer a running work:
// before each tool call it drains a queued course-correction (if any) and blocks the
// call, handing the message back to the model — which re-plans its next step instead
// of running the tool. No queued message → the guard behaves exactly as before.
// 它同时负责「空转回合」的续跑，见 Stop。
type steerHooks struct {
	inner harness.HookRunner
	drain func() (steerMsg, bool)
	// nudges 是本条意图已注入的空转续跑次数，上限 limit。指针:harness 持有的是
	// steerHooks 的值拷贝，计数必须共享同一份。
	nudges *atomic.Int64
	// limit 是空转续跑的次数上限，由 Engine.emptyTurnNudgeLimit() 从「空响应重试
	// 次数」解析而来。<=0 = 不介入(用户显式关掉了这层)。
	limit int
	// label 形如 "worker-1 · #42"，只用于日志。
	label string
}

// 空转回合(只有思考、既无正文也无工具调用)续跑次数的默认值，与 SDK 空响应重试的
// 内置默认(norma/llm/openai.go 的 emptyResponseRetries)保持一致——两层共用同一个
// 旋钮，不配置时的行为也该对齐。解析见 Engine.emptyTurnNudgeLimit。
//
// 注意这个数是「一条意图的总量」，不是「连续几次」:harness 自身的 stopHookActive
// 已经限死了连续空转只推一次——推完那一轮若还是空转，Stop 钩子不会再被调到，run 直接
// 收场;只有真正发生过一次工具回合，配额才刷新(norma/harness/query.go:534)。所以这个
// 闸挡的是「工具 → 空转 → 推 → 工具 → 空转」这种病态循环，别让它把意图预算耗光。
const defaultEmptyTurnNudges = 2

// emptyTurnNudge 是空转回合注入的续跑指令。
//
// 这种回合在 harness 眼里是一次自然结束(stop_reason=end_turn 且无 tool_use)，五层
// LLM 重试一层都不适用——它不是错误，是模型「想完了但没动手」。SDK 的空响应重试也
// 够不着:它以「有没有 yield 过事件」判空，而思考增量本身就是事件(norma/llm/openai.go
// 的 SEThinkingDelta)，所以 thinking-only 不算空。何况那层是原样重发整个 prompt，
// 对这种由上下文形状决定的空转，重发只会让模型再想一遍。这里换成追加一条指令，让它
// 带着已经产出的思考继续，输入变了才有理由给出不同的行为。
const emptyTurnNudge = "【空转提醒】你上一轮只输出了思考过程，既没有给出正文回复，也没有调用任何工具，" +
	"这一轮等于没有产出。请直接执行你刚才想好的下一步：要么调用工具，要么给出结论文字。不要重复思考。"

// isThinkingOnlyTurn reports whether the latest assistant turn produced neither
// text nor a tool call — i.e. the model spent the whole round thinking.
func isThinkingOnlyTurn(messages []llm.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != llm.RoleAssistant {
			continue
		}
		return strings.TrimSpace(m.Text()) == "" && len(m.ToolUses()) == 0
	}
	return false
}

func (h steerHooks) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	if msg, ok := h.drain(); ok {
		return true, msg.render(), nil
	}
	if h.inner != nil {
		return h.inner.PreToolUse(ctx, name, input)
	}
	return false, "", nil
}

func (h steerHooks) PostToolUse(ctx context.Context, name string, input, result []byte, isErr bool) {
	if h.inner != nil {
		h.inner.PostToolUse(ctx, name, input, result, isErr)
	}
}

// Stop 在 guard 原有语义之上补一层「空转回合」续跑:模型只输出了思考、既没给正文
// 也没调工具时，harness 会把它当成自然结束并以空 summary 收场(query.go 的
// ReasonCompleted + asst.Text())，一条本来还没做完的意图就这样断在半路。此时注入
// 一条续跑指令，让模型带着已有思考接着走。
func (h steerHooks) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	var (
		prevent  bool
		blocking []string
		msg      string
	)
	if h.inner != nil {
		prevent, blocking, msg = h.inner.Stop(ctx, messages)
	}
	// inner 已经决定硬停、或已经要注入自己的续跑消息 → 尊重它，不再叠加。
	// limit<=0 = 用户把「空响应重试次数」配成了 -1，即显式关掉这层。
	if prevent || len(blocking) > 0 || h.nudges == nil || h.limit <= 0 || !isThinkingOnlyTurn(messages) {
		return prevent, blocking, msg
	}
	n := h.nudges.Add(1)
	if n > int64(h.limit) {
		log.Printf("[work %s] 空转回合(仅思考、无正文无工具)已达续跑上限 %d，放行收场", h.label, h.limit)
		return prevent, blocking, msg
	}
	log.Printf("[work %s] 空转回合(仅思考、无正文无工具)，注入续跑指令 (%d/%d)", h.label, n, h.limit)
	return false, []string{emptyTurnNudge}, ""
}

// KillWork cancels the in-flight work running intentID (planner's kill_work tool).
// The work's agent-core session honors ctx cancellation and aborts promptly.
func (e *Engine) KillWork(intentID int64) error {
	e.workMu.Lock()
	run := e.work[intentID]
	e.workMu.Unlock()
	if run == nil {
		return fmt.Errorf("意图 %d 当前没有运行中的 work（可能已结束或未被领取）", intentID)
	}
	run.cancel(agent.AbortKilledByPlanner)
	return nil
}

// Broadcaster exposes the engine's live activity pub/sub (used by the SSE handler).
func (e *Engine) Broadcaster() *Broadcaster { return e.bc }

// emitActivity persists one captured step AND fans it out to live subscribers,
// from a single point so storage and the SSE stream never diverge.
func (e *Engine) emitActivity(t *Task, r db.Activity) db.Activity {
	if r.SlotNo == nil {
		if n := slotNumber(r.Worker); n > 0 {
			r.SlotNo = &n
		}
	}
	id, err := e.appendActivity(t, r)
	if err != nil {
		// NO LONGER SILENT: dropping a record breaks command↔result pairing in the
		// trace — a tool_use whose tool_result was lost shows as "执行中" forever, and
		// a lost 'result'/'round' record leaves the session with no summary ("无总结").
		// Everything needed to分析根因 goes into ONE error-level line: reason class,
		// summary preview, running drop count for this task, and — on the FK case — a
		// live probe of WHY the parent exploration is unreachable.
		n := e.bumpDrop(t.ID)
		diag := ""
		// On the FK-parent failure (23503) probe the live DB so the log records WHY the
		// exploration is unreachable (row gone / wrong expID) instead of just that it is.
		if isFKViolation(err) {
			storeID := t.Store.ID()
			if exists, refs, maxID, dErr := e.m.pg.ExplorationDiag(storeID); dErr != nil {
				diag = fmt.Sprintf(" | FK诊断查询失败(store.expID=%d task.ExpID=%d): %v", storeID, t.ExpID, dErr)
			} else {
				diag = fmt.Sprintf(" | FK诊断: store.expID=%d task.ExpID=%d exploration存在=%v 引用它的task数=%d MAX(exploration.id)=%d",
					storeID, t.ExpID, exists, refs, maxID)
			}
		}
		log.Printf("[activity] task %s 丢弃活动记录(该任务累计第 %d 条) worker=%s kind=%s tool=%s tuid=%s reason=%s summary=%q: %v%s",
			t.ID, n, r.Worker, r.Kind, r.Tool, r.ToolUseID, dropReason(err), preview(r.Summary, 80), err, diag)
		e.touch(t.ID)
		return r
	}
	r.ID = id
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	e.bc.Publish(t.ID, r)
	e.touch(t.ID)
	return r
}

// appendActivity persists one activity row, retrying briefly on write failure.
// Concurrent planner + worker inserts into the same exploration's activity log
// occasionally fail; a couple of quick retries recover most. Crucially, every
// failure is now LOGGED (it used to be swallowed by an `if err == nil`), so the
// underlying DB error is finally visible for diagnosis.
func (e *Engine) appendActivity(t *Task, r db.Activity) (int64, error) {
	var id int64
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if id, err = t.Store.AppendActivity(r); err == nil {
			if attempt > 1 {
				log.Printf("[activity] task %s 写入第 %d 次重试成功 (worker=%s kind=%s tool=%s)",
					t.ID, attempt, r.Worker, r.Kind, r.Tool)
			}
			return id, nil
		}
		log.Printf("[activity] task %s 写入失败 (第 %d/3 次, worker=%s kind=%s tool=%s expID=%d): %v",
			t.ID, attempt, r.Worker, r.Kind, r.Tool, t.Store.ID(), err)
		time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
	}
	return 0, err
}

// SetReadiness wires the global "an LLM provider is configured" predicate (read by
// Ready() / the llm_configured indicator). Called once at startup.
func (e *Engine) SetReadiness(fn func() bool) { e.readiness = fn }

// SetAgentResolver installs a per-task planner/worker resolver (wired by the server).
// Called once at startup before any task loop runs, so no lock is needed on reads.
func (e *Engine) SetAgentResolver(fn func(t *Task) (*agent.Planner, *agent.Worker)) {
	e.resolve = fn
	e.resolveAuthoritative = false
}

// SetAuthoritativeAgentResolver installs a resolver whose nil result must not
// fall through to the global provider. Task-level failover chains use this so a
// fully exhausted chain cannot silently bypass its configured boundary.
func (e *Engine) SetAuthoritativeAgentResolver(fn func(t *Task) (*agent.Planner, *agent.Worker)) {
	e.resolve = fn
	e.resolveAuthoritative = true
}

// snapshotFor returns the planner/worker a task should run on, from the task-router
// resolver. nil,nil means the task is deliberately unavailable (e.g. an exhausted
// failover chain); there is no global-pair fallback.
func (e *Engine) snapshotFor(t *Task) (*agent.Planner, *agent.Worker) {
	if e.resolve != nil {
		p, w := e.resolve(t)
		if (p != nil && w != nil) || e.resolveAuthoritative {
			return p, w
		}
	}
	return nil, nil
}

// Ready reports whether a global LLM provider is configured (via the readiness
// predicate wired at startup).
func (e *Engine) Ready() bool {
	return e.readiness != nil && e.readiness()
}

// ReadyFor reports whether a specific task can resolve a planner/worker pair.
// An explicit task profile chain can be runnable even when no global default
// provider is configured, so task status must not rely on Ready alone.
func (e *Engine) ReadyFor(t *Task) bool {
	p, w := e.snapshotFor(t)
	return p != nil && w != nil
}

// SetPauseWithReasonHandler wires the server's full pause path for planner-driven
// stops (策略穷尽 / 停滞保护). Without it the engine pauses in memory only.
func (e *Engine) SetPauseWithReasonHandler(fn func(*Task, string) error) { e.pauseWithReason = fn }

// pauseTaskWithReason 走 server 注入的完整暂停路径（落库 + 取消在跑 LLM + 系统活动）；
// 未注入时退回引擎内 Pause，仍然保证不空转。
func (e *Engine) pauseTaskWithReason(t *Task, reason string) {
	if t == nil {
		return
	}
	if e.pauseWithReason != nil {
		if err := e.pauseWithReason(t, reason); err != nil {
			log.Printf("[engine] task %s 暂停失败: %v", t.ID, err)
		}
		return
	}
	e.Pause(t.ID, agent.AbortStrategyExhausted)
}

// Run starts the planner loop + N worker loops for a task. The loops always run
// but no-op until an LLM is configured (so a task created while idle picks up
// automatically once LLM is set from the UI).
func (e *Engine) Run(ctx context.Context, t *Task) {
	workers := e.m.EnsureTaskWorkerSlots(t.ID, e.m.Workers())
	e.deleteMu.RLock()
	if e.IsDeleting(t.ID) {
		e.deleteMu.RUnlock()
		return
	}
	if _, loaded := e.started.LoadOrStore(t.ID, true); loaded {
		e.deleteMu.RUnlock()
		t.Notify() // already running — just nudge a planning round
		return
	}
	rt := e.registerTaskRoutines(ctx, t.ID, 1+workers)
	e.setTaskWorkers(t.ID, workers)
	e.deleteMu.RUnlock()
	e.touch(t.ID)
	runTaskRoutine(rt, func(loopCtx context.Context) { e.plannerLoop(loopCtx, t) })
	for i := 0; i < workers; i++ {
		name := fmt.Sprintf("work#%d", i+1)
		runTaskRoutine(rt, func(loopCtx context.Context) { e.workerLoop(loopCtx, t, name) })
	}
	e.startDeadlineCoordinator(ctx, t) // 任务级协调器(超时/预算/租约看门狗;去重,常开)
	// 仅在「完全没有活动意图(open+running)」时才 kick 首轮规划。带种子意图的任务:种子已
	// 是 open,或已被上面刚起的 worker 抢先 claim 成 running——两种都算「有活干」,一律跳过
	// 首轮 planner,worker 直接领种子意图开跑,跑完由 NotifyDone/心跳唤醒 planner。
	// ⚠️ 不能用 Frontier(只数 open):worker 领取(open→running)与本检查存在竞态,会误 kick。
	// 重启自动恢复时也可能只剩 running 意图,同样应跳过。
	if has, _ := t.Store.HasActiveIntent(); !has {
		t.Notify() // kick the first planning round (acted on once LLM is ready)
	}
}

// plannerHeartbeatInterval 解析任务的 planner 心跳间隔。db.CreateTask 已归一
// (低于 600 一律抬到 600);这里再兜一次底,防内存态异常值。
func plannerHeartbeatInterval(t *Task) time.Duration {
	sec := t.PlanHeartbeatSeconds
	if sec < db.MinPlanHeartbeatSeconds { // 下限=默认=600(10min)
		sec = db.MinPlanHeartbeatSeconds
	}
	return time.Duration(sec) * time.Second
}

// resetPlannerTimer 安全重臂一个可能已触发的 Timer(标准 Stop→drain→Reset 模式)。
func resetPlannerTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func (e *Engine) plannerLoop(ctx context.Context, t *Task) {
	interval := plannerHeartbeatInterval(t)
	// 心跳定时器在 loop 入口臂 = 从任务 start 计时:即使是跳过首轮 planner 的 seed 任务
	// (Run 里 frontier 非空不 kick 首轮)、这里一直阻塞,心跳也会在「任务 start + interval」
	// 触发第一轮规划。之后每次唤醒(边沿/心跳)都重臂 = 距上次任意规划触发的时长。
	heartbeat := time.NewTimer(interval)
	defer heartbeat.Stop()

	// 槽位利用率与停滞保护（每任务 planner loop 内计数，重启即清零足够）。
	//   zeroRoundStreak：连续「本轮 0 意图 + 没有 running/可领意图」的轮数。
	//   capacityRefills：连续因空闲槽位而补派的次数（每轮最多 1 次，最多 2 次）。
	zeroRoundStreak := 0
	capacityRefills := 0
	// plannerFailRetries：连续规划失败时的自动重试预算。只在失败后 2s 重试一次，
	// 避免 provider 抖动时整轮规划白等下一次心跳；连续失败就回到心跳节奏，不刷屏。
	plannerFailRetries := 0

	// runRound 跑一轮规划(含 debounce 合并 + 各 guard)。src 仅用于日志区分触发来源。
	runRound := func(src string) {
		// 尾沿去抖：每来一个通知就重置计时，把一串「worker 完成 / finding / kill」
		// 合并成一轮；但最多等 plannerDebounceMax，避免持续的小事件流把规划无限
		// 后推（旧实现是固定 800ms 窗口，落在窗口边缘的触发会被拖到下一轮）。
		const plannerDebounceMax = 3 * time.Second
		timer := time.NewTimer(e.debounce)
		deadline := time.Now().Add(plannerDebounceMax)
	drain:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-t.notify:
				remain := time.Until(deadline)
				if remain <= 0 {
					break drain
				}
				wait := e.debounce
				if wait > remain {
					wait = remain
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(wait)
			case <-timer.C:
				break drain
			}
		}
		planner, _ := e.snapshotFor(t)
		if planner == nil {
			return // idle until LLM configured
		}
		if e.IsPaused(t.ID) {
			return // user-paused: don't plan
		}
		if e.IsDeleting(t.ID) {
			return
		}
		// terminal task (goals all met → done, or failed): the run is over. A
		// resume/nudge — e.g. auto-resume of the active task on restart — must NOT
		// re-plan (it would burn an LLM round and re-confirm a settled result).
		if isTerminalStatus(t.lifecycleSnapshot().Status) {
			return
		}
		// 任务级超时收尾中:丢弃普通唤醒——worker 收尾写回、Resume 的 Notify 都不再
		// 触发常规规划轮;终局那一轮由协调器(settleTask)直接驱动,不走这里。
		if e.isSettling(t.ID) {
			return
		}
		// goalless（人工直投）分支：任务已无 open 目标时 planner 不跑——跑了会重判
		// met→cancelExec 杀掉用户经主 agent 直投的意图。是否结束改由 frontier 决定：
		// 还有 open/running 意图 → 保持 running、静默等待；意图已全部跑干 → 落 done。
		// 整段纯 Go、不触发任何 LLM 调用，也不打规划轮 marker。
		if open, err := t.Store.HasOpenGoal(); err == nil && !open {
			t.drainTriggers() // 丢弃累积的 done/finding 触发，避免 goalless 长会话里无界增长
			if active, err := t.Store.HasActiveIntent(); err == nil && !active {
				// goalless 收尾不经过 planner 的 prove_goal，遗留物门必须在这里补：
				// 目标上还有没销账的 webshell/账号，不能因为「目标都了结了」就静默收官。
				if blocked, msg := agent.ArtifactCleanupBlocked(t.Store); blocked {
					if _, warned := e.completionWarn.LoadOrStore(t.ID, true); !warned {
						e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
							Summary: "目标已了结，但目标上还有未销账的遗留物，暂不收尾",
							Detail:  msg})
						log.Printf("[goalless] task %s 有未销账遗留物，暂不收尾", t.ID)
					}
					return
				}
				e.completionWarn.Delete(t.ID)
				// frontier 抽干且无在跑意图 → 收尾。用 Guarded 版做 CAS，避免踩到并发的
				// pause/delete/超时收尾的状态转换。
				if won, err := e.m.SetTaskStatusGuarded(t.ID, "done"); err != nil {
					log.Printf("[goalless] task %s 收尾落 done 失败: %v", t.ID, err)
				} else if won {
					// 文案区分「全部达成」与「了结（含放弃）」：放弃不是达成，报告与时间线不能混为一谈。
					met, abandoned := 0, 0
					if goals, gerr := t.Store.ListByKind(db.KindGoal, 10000); gerr == nil {
						for _, g := range goals {
							switch g.State {
							case "met":
								met++
							case "abandoned":
								abandoned++
							}
						}
					}
					summary := "目标已全部达成，直投意图已执行完毕，任务结束"
					if abandoned > 0 {
						summary = fmt.Sprintf("目标已全部了结（达成 %d / 放弃 %d），直投意图已执行完毕，任务结束", met, abandoned)
					}
					e.emitActivity(t, db.Activity{Worker: "system", Kind: "text", Summary: summary})
				}
			}
			return // goalless 分支永不进入 planner.Plan
		}
		if !e.beginTaskOperation(t.ID) {
			return
		}
		defer e.decInflight(t.ID)
		e.stampFirstRun(t) // 首次真正规划 → 盖 first_run_at + 算 deadline(仅带 timeout 的任务)
		e.touch(t.ID)
		emit := func(r db.Activity) { e.emitActivity(t, r) }
		ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; 带任务 deadline
		if ectx.Err() != nil || e.IsDeleting(t.ID) {
			return
		}
		log.Printf("[planner] task %s 规划中…(%s 触发)", t.ID, src)
		// round marker: each Plan() is one planner round; emit a boundary so the
		// UI can separate rounds in the transcript (kind='round').
		e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
			Summary: fmt.Sprintf("第 %d 轮规划", e.nextPlannerRound(t.ID))})
		// what fired this round (worker done / finding; may be several — debounce
		// coalesces a burst; empty for time/heartbeat wakes).
		triggers := t.drainTriggers()
		taskIDInt, _ := strconv.ParseInt(t.ID, 10, 64)
		var met bool
		var reason string
		var err error
		// 规划失败重试：一次瞬时的模型/网络错误此前会让任务空转十分钟（等心跳），
		// 而这一轮的触发事件已经被 drain 掉了——"哪个 work 完成了"的信息随之丢失。
		for attempt := 1; attempt <= plannerAttempts; attempt++ {
			e.BeginLLMCall(t.ID)
			met, reason, err = planner.Plan(ectx, taskIDInt, e.m.assets, t.Store, t.Goal, triggers, emit)
			e.EndLLMCall(t.ID)
			if err == nil || ectx.Err() != nil || attempt == plannerAttempts {
				break
			}
			log.Printf("[planner] task %s 规划出错（第 %d/%d 次）: %v", t.ID, attempt, plannerAttempts, err)
			if sleepCtx(ectx, plannerRetryBackoff) {
				break
			}
		}
		if err != nil && ectx.Err() == nil {
			t.restoreTriggers(triggers)
		}
		if err == nil {
			plannerFailRetries = 0
		}
		// 策略穷尽：planner 已用 mark_strategy_exhausted 写下结论事实。暂停任务等人工，
		// 不在工具调用中途取消自己的 ctx——这里是本轮结束后的后处理。
		if exhausted := planner.TakeStrategyExhaustedReason(); exhausted != "" && !met && ectx.Err() == nil {
			log.Printf("[planner] task %s 策略内已无可行方向: %s", t.ID, clipRunes(exhausted, 120))
			e.pauseTaskWithReason(t, "策略内已无可行方向，任务已暂停等待人工："+clipRunes(exhausted, 300))
			return
		}
		// 槽位利用率 / 停滞保护：一轮结束后看「空闲槽位」和「是否连续空转」。
		// 此前一轮结束就不会再因空闲槽位自我唤醒，只能等 worker 完成或 10 分钟心跳，
		// 于是 4 个 worker 长期只跑 1~2 条（任务 14 实测 31 轮中 17 轮 0 意图）。
		if !met && err == nil && ectx.Err() == nil {
			states, _ := t.Store.CountIntentStates()
			running := states["running"]
			claimable, _ := t.Store.CountClaimableIntents()
			workers := e.TaskWorkers(t.ID)
			idle := workers - running
			hasOpenGoal, _ := t.Store.HasOpenGoal()
			made := planner.LastRoundIntents()
			var refill, stall bool
			zeroRoundStreak, capacityRefills, refill, stall = plannerRoundSignals(
				made, running, claimable, idle, hasOpenGoal, zeroRoundStreak, capacityRefills)
			if refill {
				log.Printf("[planner] task %s 本轮 0 意图且有 %d 个空闲槽位，4s 后补派一轮", t.ID, idle)
				time.AfterFunc(4*time.Second, func() { t.Notify() })
			}
			if stall {
				zeroRoundStreak = 0
				e.pauseTaskWithReason(t, "连续 3 轮规划未产出意图，且没有在跑/可领意图；任务已暂停等待人工检查（目标仍未达成）")
				return
			}
		}
		switch {
		case err != nil && ectx.Err() == nil:
			if plannerFailRetries < 1 {
				plannerFailRetries++
				log.Printf("[planner] task %s 规划出错（触发事件已放回队列，2s 后自动重试一次）: %v", t.ID, err)
				time.AfterFunc(2*time.Second, func() { t.Notify() })
			} else {
				log.Printf("[planner] task %s 规划连续出错（触发事件已放回队列，回到心跳重试）: %v", t.ID, err)
			}
		case met:
			// 人工发起的痕迹清理意图可能让已完成任务重新 running；此时所有 goal 仍是 met，
			// 若直接收官并 cancelExec，会把正在清理的 worker 立刻杀掉。等清理意图收束后
			// 再走这里（worker done 会再次唤醒 planner）。
			if active, cerr := t.Store.HasActiveCleanupIntent(); cerr == nil && active {
				log.Printf("[planner] task %s 目标已达成，但仍有痕迹清理意图在跑，暂缓收官", t.ID)
				e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
					Summary: "目标已达成，但仍有痕迹清理意图在执行，任务暂不收官"})
				break
			}
			// met 是 planner 基于本轮开局快照做的判断；收官前复核一次目标状态：
			// 并发新增/修改目标后不能沿用旧结论把任务关掉。
			if open, gerr := t.Store.HasOpenGoal(); gerr == nil && open {
				log.Printf("[planner] task %s 目标状态在收官前发生变化（出现未达成目标），本轮不收官", t.ID)
				e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
					Summary: "目标状态已变化（出现新的未达成目标），本轮暂不收官"})
				break
			}
			log.Printf("[planner] task %s 判定目标达成: %s", t.ID, reason)
			// 用 Guarded 版 CAS 落终态：与超时收尾/删除/暂停竞态时保留先到的终态语义。
			if won, serr := e.m.SetTaskStatusGuarded(t.ID, "done"); serr != nil {
				log.Printf("[planner] task %s 标记完成落库失败: %v", t.ID, serr)
			} else if !won {
				log.Printf("[planner] task %s 收官时已是终态，保留原状态", t.ID)
			} else {
				e.completionWarn.Delete(t.ID)
				// 任务已判完成 → 立刻取消在跑的 worker：它们手头的意图跑出来也没意义了。
				// 下一轮 worker 循环撞终态门就不再领新意图;被取消的这批走下方"任务已完成"分支
				// 归为 stopped(而非 blocked)。
				e.cancelExec(t.ID, agent.AbortGoalMet)
			}
		default:
			log.Printf("[planner] task %s 规划完成", t.ID)
		}
		e.touch(t.ID)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.notify:
			runRound("edge") // worker 结束 / finding / kill / resume / seed 首轮
		case <-heartbeat.C:
			// 周期兜底:死锁兜底 + 唤醒去监督飞行中的 worker(steer/kill) + 周期复查。
			runRound("heartbeat")
		}
		// 每次唤醒(边沿或心跳)后重臂心跳:任意规划触发都重算这段静置计时。
		resetPlannerTimer(heartbeat, interval)
	}
}

func (e *Engine) workerLoop(ctx context.Context, t *Task, name string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, worker := e.snapshotFor(t)
		if worker == nil {
			if sleepCtx(ctx, 1500*time.Millisecond) {
				return
			}
			continue
		}
		if e.IsPaused(t.ID) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // user-paused: don't claim/execute intents
		}
		if e.IsDeleting(t.ID) {
			return
		}
		if e.isSettling(t.ID) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // 任务超时收尾中:不再领新意图(在跑的自行收尾,协调器等其 drain)
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // 任务已终态(done/failed/timeout):停止领取遗留意图,别在完成后空跑 frontier
		}
		lock := e.slotLock(t.ID, name)
		if lock == nil {
			return
		}
		// 先占槽位锁再 claim：人工续跑可能正持有同一槽位的 transcript。
		if !lock.TryLock() {
			if sleepCtx(ctx, 200*time.Millisecond) {
				return
			}
			continue
		}
		claimed := false
		stop := false
		func() {
			// defer 兜底：runWorkerStep 内部 panic 会被最外层 goSafe 捕获，没有 defer 时
			// 槽位锁与 inflight 永久泄漏，该槽位从此不再领活。
			defer lock.Unlock()
			if !e.beginTaskOperation(t.ID) {
				stop = true
				return
			}
			defer e.decInflight(t.ID)
			claimed = e.runWorkerStep(ctx, t, name, worker)
		}()
		if stop {
			return
		}
		if !claimed && sleepCtx(ctx, 800*time.Millisecond) {
			return
		}
	}
}

// runWorkerStep claims one intent from the frontier and fully settles it via
// runIntent. Returns false when nothing was claimable. The pool worker loop is its
// only caller.
func (e *Engine) runWorkerStep(ctx context.Context, t *Task, name string, worker *agent.Worker) bool {
	intent := e.claimNextForSlot(t, name)
	if intent == nil {
		return false
	}
	log.Printf("[worker %s] task %s 领取意图 #%d", name, t.ID, intent.ID)
	return e.runIntent(ctx, t, name, worker, intent, "", "")
}

// runIntent executes and fully settles one already-claimed (state=running) intent.
// Both the pool worker loop (via runWorkerStep) and the human-message handler (via
// runDetachedIntent, a dedicated goroutine outside the worker pool) call it, so the
// execute/retry/state-write logic lives in exactly one place. A non-empty message
// is injected as this turn's input through ExecuteWithMessage; requestID keys the
// transcript marker that dedups re-injection across model_error retries. The caller
// must already hold one task-operation admission for the whole sequence so a delete
// cannot observe quiescence between the LLM return and the final DB writes.
func (e *Engine) runIntent(ctx context.Context, t *Task, name string, worker *agent.Worker, intent *db.Node, requestID, message string) bool {
	hasChatMessage := message != ""
	e.stampFirstRun(t) // 首次真正执行 → 盖 first_run_at + 算 deadline(仅带 timeout 的任务)
	e.touch(t.ID)
	slotNo := slotNumber(name)
	emit := func(r db.Activity) {
		if r.SlotNo == nil && slotNo > 0 {
			n := slotNo
			r.SlotNo = &n
		}
		e.observeWAF(t, r)
		e.emitActivity(t, r)
	}
	ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; 带任务 deadline
	if ectx.Err() != nil || e.IsDeleting(t.ID) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "open"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 领取后回退失败: %v", name, t.ID, intent.ID, err)
		}
		return true
	}
	// per-work child context so the planner's kill_work can stop just this work.
	workCtx, workCancel := context.WithCancelCause(ectx)
	e.registerWork(intent.ID, workCancel)
	// wrap the guard hooks so steer_work can inject a mid-run course-correction
	// for THIS intent (drained before the worker's next tool call).
	iid := intent.ID
	// 意图租约心跳：每个活动(LLM 回合/工具调用)最多每 TTL/3 续一次租——心跳即进度证据，
	// 长时间无活动的 running 意图由任务协调器的看门狗取消并回收重派。续租失败（租约已
	// 易主）只记日志：本 run 的终态写入会因 CAS 失败而自然让位。
	var lastLeaseRenew atomic.Int64
	renewLease := func() {
		now := time.Now().UnixNano()
		last := lastLeaseRenew.Load()
		if now-last < int64(intentLeaseRenewInterval) || !lastLeaseRenew.CompareAndSwap(last, now) {
			return
		}
		if ok, err := t.Store.TouchIntentLease(iid, name, db.IntentLeaseTTL); err == nil && !ok {
			log.Printf("[worker %s] task %s 意图 #%d 租约已易主，续租未生效", name, t.ID, iid)
		}
	}
	emitRenew := func(r db.Activity) { renewLease(); emit(r) }
	taskEmit := func(a db.Activity) {
		nid := iid
		a.NodeID, a.Worker = &nid, name
		if a.SlotNo == nil && slotNo > 0 {
			n := slotNo
			a.SlotNo = &n
		}
		emitRenew(a)
	}
	label := fmt.Sprintf("%s · #%d", name, iid)
	workCtx = intercept.WithTaskContext(workCtx, t.ID, label, taskEmit)
	// nudges 有意建在 model_error 重跑循环之外:空转续跑的上限是「这条意图」的总量，
	// 重跑一轮不该把额度清零重来。
	hooks := steerHooks{
		inner:  t.Guard.Hooks(),
		drain:  func() (steerMsg, bool) { return e.drainSteer(iid) },
		nudges: &atomic.Int64{},
		limit:  e.emptyTurnNudgeLimit(),
		label:  label,
	}
	// UI 的槽位会话需要一条意图边界气泡；同一 intent 被暂停/续跑时只写一次。
	if slotNo > 0 {
		if started, _ := t.Store.HasIntentStartActivity(iid); !started {
			boundary := db.Activity{
				NodeID: &iid, Worker: name, Kind: "intent",
				Summary: intentSummaryOf(intent), Detail: string(intent.Payload),
			}
			if slotNo > 0 {
				n := slotNo
				boundary.SlotNo = &n
			}
			e.emitActivity(t, boundary)
		}
	}
	wTaskID, _ := strconv.ParseInt(t.ID, 10, 64)
	e.BeginLLMCall(t.ID)
	var reason harness.TerminalReason
	var wrote agent.WriteCounts
	var err error
	if hasChatMessage {
		reason, wrote, err = worker.ExecuteWithMessage(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emitRenew, e.m.enrich, t.NotifyFinding, requestID, message)
	} else {
		reason, wrote, err = worker.Execute(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emitRenew, e.m.enrich, t.NotifyFinding)
	}
	e.EndLLMCall(t.ID)
	// model_error 收场 → 额外重跑几次（退避后再试）。仅在意图仍属本 work、任务
	// 未暂停/未终止/未取消【且未进入收尾】时重试；否则让位给对应分支处理(收尾期不
	// 再重试,避免退避挤占其他 worker 的优雅收尾窗口)。
	maxRetries, retryBackoff := e.modelErrorRetryPolicy()
	for attempt := 1; attempt <= maxRetries &&
		retryableWorkerModelError(reason, err) &&
		workCtx.Err() == nil && ectx.Err() == nil && !e.IsPaused(t.ID) && !e.isSettling(t.ID); attempt++ {
		// 重试是**警告**不是错误：以前因「重试」二字被判成 error，而这条恰恰说明
		// 平台正在按预期自愈。
		slogFor("worker").Warn("model_error 收场，稍后重试",
			"worker", name, "task", t.ID, "intent", intent.ID,
			"backoff", retryBackoff.String(), "attempt", attempt, "max", maxRetries)
		if sleepCtx(workCtx, retryBackoff) {
			break // 退避期间被取消（终止/暂停）→ 交给下方分支处理
		}
		e.BeginLLMCall(t.ID)
		if hasChatMessage {
			reason, wrote, err = worker.ExecuteWithMessage(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emitRenew, e.m.enrich, t.NotifyFinding, requestID, message)
		} else {
			reason, wrote, err = worker.Execute(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emitRenew, e.m.enrich, t.NotifyFinding)
		}
		e.EndLLMCall(t.ID)
	}
	// Capture kill state before detachWork cancels workCtx. kill = this work's
	// ctx was cancelled (planner kill_work) while the TASK ctx kept running; a
	// pause cancels the task ctx (ectx) instead. Checking workCtx.Err() AFTER
	// unregister would always be true (unregister cancels it) → every completed
	// work would be wrongly marked stopped.
	workCause := context.Cause(workCtx)
	killed := workCtx.Err() != nil && ectx.Err() == nil
	action, completeWork := e.detachWork(intent.ID)
	// A caller may stop waiting and release its in-memory reservation before the
	// agent honors cancellation. The named context cause remains authoritative and
	// still settles the stopped run into a recoverable state.
	if action == "" {
		switch {
		case errors.Is(workCause, agent.AbortWorkPausedByUser):
			action = "pause"
		case errors.Is(workCause, agent.AbortWorkCancelledByUser):
			action = "cancel"
		case errors.Is(workCause, agent.AbortWorkLeaseExpired):
			action = "lease"
		}
	}
	var controlErr error
	defer func() { completeWork(controlErr) }()
	if action == "pause" {
		controlErr = transitionIntentState(t.Store, intent.ID, "running", "paused")
		if controlErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 暂停状态落库失败: %v", name, t.ID, intent.ID, controlErr)
			return true
		}
		log.Printf("[worker %s] task %s 意图 #%d 已暂停", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	if action == "cancel" {
		// Park the stopped run in paused before handing cleanup to the API. If the
		// request disconnects after cancellation, the intent remains recoverable and
		// a later cancel can finish cleanup instead of leaving a phantom running row.
		controlErr = transitionIntentState(t.Store, intent.ID, "running", "paused")
		if controlErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 取消栅栏落库失败: %v", name, t.ID, intent.ID, controlErr)
			return true
		}
		log.Printf("[worker %s] task %s 意图 #%d 已停止，等待取消清理", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	if action == "lease" {
		// 租约到期被看门狗回收：意图退回 open 重新领取（从 transcript 续跑）。看门狗通常
		// 已先一步把状态 CAS 到 open，此时这里转换失败属预期，只记一行日志。
		if err := transitionIntentState(t.Store, intent.ID, "running", "open"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 租约回收回退(预期:看门狗已改状态): %v", name, t.ID, intent.ID, err)
		} else {
			log.Printf("[worker %s] task %s 意图 #%d 因租约到期结束，已退回 open 等待重派", name, t.ID, intent.ID)
		}
		e.touch(t.ID)
		t.NotifyRequeued(intent.ID, "租约到期（心跳停止），意图退回 open 等待重派")
		return true
	}
	// if a pause cancelled this run mid-flight, return the intent to the frontier
	// so it is re-claimed on resume — the worker will resume the prior LLM
	// conversation from its transcript instead of restarting from scratch.
	if ectx.Err() != nil && taskExecutionPaused(context.Cause(ectx)) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "open"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 任务暂停回退失败: %v", name, t.ID, intent.ID, err)
		}
		return true
	}
	// 任务超时收尾的硬兜底 cancel(非 pause、非 kill)取消了本 run → 归为 exhausted(已收尾),
	// 不要误标 blocked。此时 worker 通常已在 settlement 阶段把结果写回。
	if ectx.Err() != nil && e.isSettling(t.ID) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "exhausted"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 超时收尾状态落库失败: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 意图 #%d 因任务超时收尾结束(exhausted)，写回 %s", name, t.ID, intent.ID, wrote)
		e.touch(t.ID)
		return true
	}
	// 任务已判完成(done via 常规路径)→ 上面 cancelExec 取消了本 run。意图结果已无意义,
	// 标 stopped(不是 blocked),别污染已完成任务的意图状态。
	if ectx.Err() != nil && isTerminalStatus(e.m.TaskStatus(t.ID)) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "stopped"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 终态停止落库失败: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 意图 #%d 因任务已完成而取消(stopped)", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	// 进程关停（SIGTERM/SIGINT、一键更新重启）取消了本 run → 退回 open，**不标 blocked**。
	// 关停不是"这条意图没探成"：agent.AbortShutdown 的说明就是"重启后残留的 running 意图会
	// 重置为 open 并重新执行"。此前没有这条分支，靠"终态写入恰好撞上关池子而失败"才达成
	// ——写入赶在关池子之前成功时，意图会被标成 blocked 这种假失败，而启动重置只认
	// running、不修它（planner 会把它当外部故障重派，但图上留了个假终态）。
	// 排在其它特判之后：超时收尾/任务终态/kill 都比"整个进程要关了"更具体。
	if ectx.Err() != nil && shutdownAborted(context.Cause(ectx)) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "open"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 关停回退失败: %v", name, t.ID, intent.ID, err)
		} else {
			log.Printf("[worker %s] task %s 意图 #%d 因进程关停结束，已退回 open 等待重启后重派", name, t.ID, intent.ID)
		}
		e.touch(t.ID)
		return true
	}
	// killed by the planner: mark stopped (don't write back results, don't auto-reclaim).
	if killed {
		if err := transitionIntentState(t.Store, intent.ID, "running", "stopped"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d planner 停止落库失败: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 意图 #%d 被终止(stopped)", name, t.ID, intent.ID)
		e.touch(t.ID)
		t.NotifyDone(intent.ID, "stopped")
		return true
	}
	if err != nil {
		log.Printf("[worker %s] intent %d: %v", name, intent.ID, err)
	}
	// ── P13 意图收尾门 ── 本次 run 正常收笔（err==nil）才走：托管长任务还在跑时
	// 不做终态判定（详见 execGate）。blocked（run 自身出错）不走此门——没测成不是
	// 结论，也不会被当成测干净。
	if err == nil && e.execGate(t, intent.ID, name) {
		e.touch(t.ID)
		return true
	}
	// terminal分流：撞步数上限 ≠ 完成。max_turns→exhausted（规划者据此知道这个方向
	// 试过但没真正做完、需换角度，而非当成已覆盖永久跳过）；出错→blocked；正常→done。
	outcome := ""
	if o, outcomeErr := t.Store.GetIntentSemanticOutcome(intent.ID); outcomeErr == nil {
		outcome = o
	}
	intentKind := intentKindOf(intent)
	// 历史产出：重试/续跑只看见本次 run 的写回。若这条意图在前几轮已经产出过，
	// 本次没有新产出不等于“从没产出”——否则续跑会被误判 no_output，规划者据此
	// 把一条已经探出东西的方向当成没做。只在确实需要判定时查一次库。
	var prior intentPriorOutput
	if wrote.Total() == 0 || (intentKind == "verify" && wrote.ValidationTotal() == 0) {
		if anyOut, validation, herr := t.Store.IntentPriorOutput(intent.ID); herr == nil {
			prior = intentPriorOutput{Any: anyOut, Validation: validation}
		} else {
			log.Printf("[worker %s] task %s 意图 #%d 历史产出查询失败（按本次口径结算）: %v", name, t.ID, intent.ID, herr)
		}
	}
	state := settleIntentState(err, reason, wrote, outcome, intentKind, prior)
	recordValidatedAssets(t, intent, intentKind, state, wrote)
	if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		log.Printf("[worker %s] intent %d 撞步数上限或运行超时(exhausted)，收尾后写回 %s", name, intent.ID, wrote)
	}
	if state == "no_output" {
		if intentKind == "verify" && wrote.Total() > 0 {
			log.Printf("[worker %s] intent %d 是验证意图但只登记了资产、没有验证结论 → no_output（planner 可重派）", name, intent.ID)
		} else {
			log.Printf("[worker %s] intent %d 正常结束但什么都没写回 → no_output（planner 可重派）", name, intent.ID)
		}
	}
	if state == "blocked" && isTaskLLMChainExhausted(err) {
		if rerr := t.Store.SetIntentBlockedReason(intent.ID, db.IntentBlockedLLMQuota); rerr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 记录阻塞原因失败: %v", name, t.ID, intent.ID, rerr)
		}
	} else {
		if stateErr := transitionIntentState(t.Store, intent.ID, "running", state); stateErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 终态 %s 落库失败: %v", name, t.ID, intent.ID, state, stateErr)
		}
	}
	log.Printf("[worker %s] task %s 意图 #%d 结束: %s (写回 %s)", name, t.ID, intent.ID, state, wrote)
	e.touch(t.ID)
	t.NotifyDone(intent.ID, state) // results changed the graph -> wake the planner with the real terminal state
	return true
}

// settleIntentState 决定一条意图本次 run 结束后落哪个终态。抽成纯函数是因为三条规则
// 互相压制，埋在 settler 里只能靠读代码猜：
//
//  1. 运行结果：出错 → blocked；撞步数上限 / 超时 → exhausted（"试过但没做完"，
//     planner 据此换角度，而不是当成已覆盖永久跳过）；
//  2. 语义终态：worker 经 conclude_intent 声明 negative / out-of-scope 时以语义为准；
//     blocked / exhausted 压过它——没测成不是结论；
//  3. **空跑**：正常结束、什么都没写回、也没有语义结论 → no_output。
//     此前这种 run 落 done：planner 当成"已覆盖"不再派、覆盖度当成"已测"，
//     整条方向就这样静默消失——而它可能连一次探针都没发出去。
//
// recordValidatedAssets 把 verify 意图真实验证过的资产落库，供覆盖矩阵按资产计。
//
// 一条 verify 意图可以绑多个资产、只测其中一个：不记录的话矩阵会把整排资产都算成
// 该风险类已测。这里只对终态（done/negative）且确有验证产出的意图记录；没有产出
// 的意图不写字段，矩阵沿用原口径（其状态本身已是 no_output/failed，不会被算已测）。
func recordValidatedAssets(t *Task, intent *db.Node, kind, state string, wrote agent.WriteCounts) {
	if t == nil || intent == nil || kind != "verify" {
		return
	}
	if state != "done" && state != "negative" {
		return
	}
	validated := wrote.ValidationAssetIDs()
	if len(validated) == 0 {
		return
	}
	if err := t.Store.SetIntentValidatedAssets(intent.ID, validated); err != nil {
		log.Printf("[worker] task %s 意图 #%d 验证资产记录失败: %v", t.ID, intent.ID, err)
		return
	}
	bound := intentAssetIDsOf(intent)
	if len(bound) <= 1 {
		return
	}
	covered := make(map[int64]bool, len(validated))
	for _, id := range validated {
		covered[id] = true
	}
	var missing []int64
	for _, id := range bound {
		if !covered[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return
	}
	// 只记日志与活动流，不改终态：意图确实做完了已验证的那部分，覆盖矩阵会如实
	// 显示其余资产仍未触及，planner 下一轮可据此补派。
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	log.Printf("[worker] task %s 意图 #%d 绑定了 %d 个资产，但只验证了 %d 个；未验证资产: %v",
		t.ID, intent.ID, len(bound), len(validated), missing)
}

// intentAssetIDsOf 读意图 payload 的 asset_ids（覆盖矩阵与收尾提示共用）。
func intentAssetIDsOf(n *db.Node) []int64 {
	if n == nil || len(n.Payload) == 0 {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

// intentKindOf 读意图的完成口径（payload.intent_kind）。空 = 旧意图，
// 沿用历史判定（任何写回都算产出），不能因为字段缺失改变既有任务的行为。
func intentKindOf(n *db.Node) string {
	if n == nil || len(n.Payload) == 0 {
		return ""
	}
	var p struct {
		Kind string `json:"intent_kind"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return strings.TrimSpace(p.Kind)
}

// intentPriorOutput 是这条意图在本次 run 之前已经留下的产出（见
// ExplorationStore.IntentPriorOutput）。Any 覆盖资产锚定与图产出；Validation
// 只覆盖验证级产出（事实/漏洞/战果/检测/遗留物），供 verify 口径使用。
type intentPriorOutput struct {
	Any        bool
	Validation bool
}

func settleIntentState(err error, reason harness.TerminalReason, wrote agent.WriteCounts, semanticOutcome, intentKind string, prior intentPriorOutput) string {
	state := "done"
	switch {
	case err != nil:
		state = "blocked"
	case reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout:
		state = "exhausted"
	}
	if state != "done" {
		return state
	}
	if semanticOutcome == "negative" || semanticOutcome == "out-of-scope" {
		return semanticOutcome
	}
	// 本次没写回，但这条意图前几轮已经产出过 → 仍是 done（续跑不能抹掉前段成果）。
	if wrote.Total() == 0 && !prior.Any {
		return "no_output"
	}
	// 验证意图的完成依据是「验证结果」，不是「登记了几个资产」：只 insert_assets
	// 的 verify 意图没有证明任何东西，落 no_output 让 planner 换打法重派，而不是
	// 当成已覆盖——覆盖矩阵与报告都按终态算，一处放宽就会把"没测"记成"测过"。
	// 历史验证产出同样算数：续跑重复登记旧资产不代表这条验证没做过。
	if intentKind == "verify" && wrote.ValidationTotal() == 0 && !prior.Validation {
		return "no_output"
	}
	return "done"
}

// execGate is the P13 finalization gate. A run that finishes normally while ITS OWN
// hosted long tasks (nuclei/ffuf/nmap scans, dictionary batches) are still running
// must not settle the intent: the output is not in yet, and settling now would be
// "宣布这方向探完了 while the scan is still going". The intent stays running and
// an execution watcher is registered — when the executions reach a terminal state
// the intent goes back to 'open' (the pool re-claims it, the worker resumes from its
// transcript) and the planner is woken. Returns true when the caller must not settle.
//
// Factored out of runIntent so the acceptance suite can drive the decision against a
// real ledger without a live LLM run.
func (e *Engine) execGate(t *Task, intentID int64, worker string) bool {
	if e.isSettling(t.ID) || isTerminalStatus(e.m.TaskStatus(t.ID)) {
		return false
	}
	execs := e.m.Executions()
	if execs == nil {
		return false
	}
	active := execs.ActiveForIntent(intentID)
	if len(active) == 0 {
		return false
	}
	parts := make([]string, 0, len(active))
	for _, a := range active {
		desc := a.Description
		if desc == "" {
			desc = preview(a.Command, 60)
		}
		parts = append(parts, fmt.Sprintf("#%d %s: %s", a.ID, a.Tool, desc))
	}
	log.Printf("[worker %s] task %s 意图 #%d 收笔但仍有 %d 个执行在跑，意图保持 running: %s",
		worker, t.ID, intentID, len(active), strings.Join(parts, " / "))
	nid := intentID
	e.emitActivity(t, db.Activity{NodeID: &nid, Worker: worker, Kind: "execution_pending",
		Summary: fmt.Sprintf("收笔待执行：%d 个长任务仍在跑，意图暂不判完", len(active)),
		Detail:  "在跑：" + strings.Join(parts, " / ") + "\n执行全部落终态后本意图会自动退回 open 续跑（已写入的产出保留）。"})
	e.watchExecutions(t, intentID)
	return true
}

// watchExecutions is the execution watcher behind the P13 finalization gate: it
// polls the ledger until the intent has no in-flight execution left, then returns
// the intent to 'open' (the pool re-claims it and the worker resumes from its
// transcript) and wakes the planner with a done-trigger. Registered at most once
// per intent — the intent stays running while the watcher lives, so only one run
// can have parked it. In-memory by design: a restart is covered by orphan
// reconciliation (those rows go terminal) plus the lease watchdog.
func (e *Engine) watchExecutions(t *Task, intentID int64) {
	if _, loaded := e.execWatch.LoadOrStore(intentID, true); loaded {
		return
	}
	goSafe("engine/execution-watch", nil, func() {
		defer e.execWatch.Delete(intentID)
		execs := e.m.Executions()
		if execs == nil {
			return
		}
		deadline := time.Now().Add(execWatchMax)
		for time.Now().Before(deadline) {
			if len(execs.ActiveForIntent(intentID)) == 0 {
				break
			}
			if sleepCtx(e.runtimeCtx(t.ID), execWatchPoll) {
				return // 任务已停止/删除（rt.cancel）——观察者随之退出
			}
		}
		rows, _ := execs.ListForIntent(intentID)
		// 执行落终态 → 把这条意图交回可领取状态。CAS 失败（意图已被别的路径收走/
		// 任务已停）只记一行日志：终态已有人负责，观察者静默退出。
		reopened, err := t.Store.CompareAndSetIntentState(intentID, "running", "open")
		if err != nil {
			log.Printf("[exec] task %s 意图 #%d 执行收束后回退失败: %v", t.ID, intentID, err)
			return
		}
		if !reopened {
			log.Printf("[exec] task %s 意图 #%d 执行收束，但意图已不在 running（无需回退）", t.ID, intentID)
			return
		}
		nid := intentID
		e.emitActivity(t, db.Activity{NodeID: &nid, Worker: "system", Kind: "execution_done",
			Summary: "长任务执行收束，意图退回待领续跑", Detail: executionDigest(rows)})
		log.Printf("[exec] task %s 意图 #%d 执行收束，已退回 open", t.ID, intentID)
		e.touch(t.ID)
		t.NotifyRequeued(intentID, "托管长任务执行收束")
	})
}

// executionDigest renders the intent's executions as one readable block (the
// activity detail; the planner reads it back through node_detail).
func executionDigest(rows []*db.ToolExecution) string {
	if len(rows) == 0 {
		return "（无执行记录）"
	}
	var b strings.Builder
	for i, r := range rows {
		if i >= 10 {
			b.WriteString(fmt.Sprintf("… 共 %d 条执行记录\n", len(rows)))
			break
		}
		exit := "-"
		if r.ExitCode != nil {
			exit = strconv.Itoa(*r.ExitCode)
		}
		b.WriteString(fmt.Sprintf("#%d [%s] %s exit=%s bytes=%d\n  命令: %s\n  输出文件: %s\n",
			r.ID, r.Status, r.Error, exit, r.OutputBytes, preview(r.Command, 160), r.OutputPath))
		// 头部与尾部都给：扫描类命令（nmap/ffuf/nuclei）的命中行往往在最前面，
		// 只给尾部的话续跑的 worker 会漏掉它们。输出文件本身也给了路径，
		// 需要更多时可以直接 grep。
		if head := strings.TrimSpace(readOutputHead(r.OutputPath, 1024)); head != "" {
			b.WriteString("  开头输出: " + preview(head, 1000) + "\n")
		}
		if tail := strings.TrimSpace(r.PartialTail); tail != "" {
			b.WriteString("  末尾输出: " + preview(tail, 400) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// runDetachedIntent runs one paused/interrupted intent OUTSIDE the worker pool in
// its own goroutine — the human-message path. It transitions the intent
// fromState->running itself (never through 'open'), so the pool, which only claims
// 'open', can never race it; the "at most one run per intent" invariant still holds
// because winning the CAS is the sole entry and work[intentID] was cleared when the
// previous run settled. fromState is paused, or a recoverable terminal state
// (blocked/exhausted/stopped/no_output) when a human revives an interrupted run.
// Because it does not compete for a frontier slot, a user message continues the
// worker immediately even when all pool slots are busy (mirroring how the
// main-agent chat handler starts its run directly). The spawned goroutine owns one
// task-operation admission for the whole run and roots its context at ctx (pass the
// server root, never the HTTP request, so a disconnect cannot strand the run while
// task pause/delete/shutdown still stops it). Returns an error if the run could not
// be started; the intent is left untouched in that case.
func (e *Engine) runDetachedIntent(ctx context.Context, t *Task, intentID int64, fromState, requestID, message, agentMessage string) error {
	node, err := t.Store.GetNode(intentID)
	if err != nil {
		return err
	}
	if node == nil || node.Kind != db.KindIntent {
		return fmt.Errorf("intent not found")
	}
	workers := e.TaskWorkers(t.ID)
	if workers <= 0 {
		workers = e.m.TaskWorkerSlots(t.ID)
	}
	if workers <= 0 {
		workers = 1
	}
	// Prefer the slot that last ran this intent so the slot transcript (and its
	// cache) is resumed. If that slot is busy, fall back to any idle slot instead
	// of failing: slot affinity is an optimization, not a correctness rule (the DB
	// claim path now applies the same rule — an owner slot with no running intent
	// no longer blocks other slots).
	slot := ""
	var lock *sync.Mutex
	preferred := slotNumber(node.Owner)
	if preferred > 0 && preferred <= workers {
		if l := e.slotLock(t.ID, slotName(preferred)); l != nil && l.TryLock() {
			slot, lock = slotName(preferred), l
		}
	}
	if lock == nil {
		for i := 1; i <= workers; i++ {
			if i == preferred {
				continue // 已尝试过首选槽位（被占）
			}
			l := e.slotLock(t.ID, slotName(i))
			if l != nil && l.TryLock() {
				slot, lock = slotName(i), l
				break
			}
		}
	}
	if lock == nil {
		return fmt.Errorf("%w: 所有 Worker 槽位都在运行，请稍后再继续", errSlotBusy)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			lock.Unlock()
		}
	}()
	if !e.beginTaskOperation(t.ID) {
		return fmt.Errorf("task is being deleted")
	}
	releaseAdmission := true
	defer func() {
		if releaseAdmission {
			e.decInflight(t.ID)
		}
	}()
	_, worker := e.snapshotFor(t)
	if worker == nil {
		return fmt.Errorf("worker 尚未就绪")
	}
	changed, err := t.Store.ClaimPausedIntent(intentID, fromState, slot, db.IntentLeaseTTL)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: 意图不再是 %s 状态", db.ErrIntentStateConflict, fromState)
	}
	node.State, node.Owner = "running", slot
	// Record the human turn as a visible activity BEFORE the run starts, so it is
	// ordered ahead of any worker step and never appears without the run happening.
	// Keep the UI copy concise; ExecuteWithMessage writes the server-resolved
	// reference snapshot into the intent transcript as the LLM input.
	uid := intentID
	sn := slotNumber(slot)
	e.emitActivity(t, db.Activity{NodeID: &uid, Worker: "user", Kind: "user", Summary: message, Detail: message, SlotNo: &sn})
	releaseAdmission = false // ownership of the admission passes to the goroutine
	lockHeld = false         // ownership of the slot lock passes to the goroutine
	goSafe("engine/run-intent-slot", nil, func() {
		defer lock.Unlock()
		defer e.decInflight(t.ID)
		e.runIntent(ctx, t, slot, worker, node, requestID, agentMessage)
	})
	return nil
}

// shutdownAborted reports whether this run was cancelled by process shutdown
// (SIGTERM/SIGINT 或一键更新重启). 与 taskExecutionPaused 同款：按登记过的 code 判，
// 别去比字符串以外的什么东西。
func shutdownAborted(cause error) bool {
	var abort *agent.AbortCause
	if !errors.As(cause, &abort) {
		return false
	}
	return abort.Code == "shutdown"
}

func taskExecutionPaused(cause error) bool {
	var abort *agent.AbortCause
	if !errors.As(cause, &abort) {
		return false
	}
	switch abort.Code {
	case "paused_by_user", "paused_by_orchestrator", "paused_on_reload", "paused_race_guard",
		"queued_for_admission", "llm_unavailable_queued", "task_deleted",
		// 这三类不是"意图没探成"，而是平台主动踩了刹车：预算耗尽、停机开关、离开
		// 作业时间窗；以及 planner 判定策略内无方向。被它们取消的意图必须退回 open，
		// 否则恢复后会以 blocked 终态留在图上，planner 会当"没测成"重派，白烧预算。
		"budget_exhausted", "kill_switch", "scope_window_closed", "strategy_exhausted":
		return true
	default:
		return false
	}
}

// plannerRoundSignals 是一轮规划结束后的纯判定：是否需要因空闲槽位补派一次，
// 以及是否已连续空转到需要暂停。抽成纯函数是为了让「不计代价派活」与「空转停机」
// 这两条纪律能被单测钉住（实测任务 14：31 轮里 17 轮 0 意图，4 槽长期只跑 1~2 条）。
//
//	refill：本轮 0 意图、至少 2 个空闲槽位、没有可领意图、目标未达成，且补派次数未用尽；
//	stall ：连续 3 轮「0 意图 + 没有 running/可领意图」——planner 已无产出，任务不能再空转。
func plannerRoundSignals(made, running, claimable, idle int, hasOpenGoal bool, streak, refills int) (newStreak, newRefills int, refill, stall bool) {
	newRefills = refills
	if made > 0 {
		newStreak, newRefills = 0, 0
	} else if running == 0 && claimable == 0 && hasOpenGoal {
		newStreak = streak + 1
	} else {
		newStreak = 0
	}
	refill = made == 0 && idle >= 2 && claimable == 0 && hasOpenGoal && refills < 2
	if refill {
		newRefills++
	}
	stall = newStreak >= 3
	return
}

func sleepCtx(ctx context.Context, d time.Duration) (done bool) {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func (e *Engine) claimNextForSlot(t *Task, name string) *db.Node {
	pool := slotPool(e.TaskWorkers(t.ID))
	fr, _ := t.Store.FrontierForSlot(name, pool, 20)
	for _, in := range fr {
		if ok, _ := t.Store.ClaimIntentForSlot(in.ID, name, db.IntentLeaseTTL, pool); ok {
			return in
		}
	}
	return nil
}
