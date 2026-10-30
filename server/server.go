package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/llmpool"
)

// BuildVersion is the backend application version, injected from cmd/blade at
// startup (which in turn gets it from -ldflags "-X main.version=<tag>").
// Defaults to "dev" for local builds. Exposed to the frontend via GET /api/health.
var BuildVersion = "dev"

// Server exposes the BLADE backend over a JSON HTTP API for the shadcn/ui
// frontend.
type Server struct {
	m        *Manager
	engine   *Engine
	ctx      context.Context
	sched    *Scheduler
	skillDir string // root directory for skill subdirectories
	side     *sideQuestionState

	// concMu serializes concurrency-cap decisions (admission + reconcile) so a
	// scheduler tick and an HTTP settings change / task creation can't both count
	// free slots off the same snapshot and over-promote past the limit.
	concMu sync.Mutex

	// 下面按域聚成子结构（P2 第二步）。放在子结构里而不是平铺在 Server 上，
	// 是为了让每个域的状态与它自己的锁待在一起——此前 10 个 mutex 混在 37 个字段里，
	// 很难看出哪把锁保护哪些数据。
	cors      corsState
	auth      authState
	llm       llmState
	providers providerState
	chatRuns  chatState
	triggers  *triggerQueue
	agents    agentState
	archive   archiveState
	boot      restoreState

	queryCache queryCacheState
}

// restoreState 记录启动恢复是否成功。恢复失败时 readiness 必须如实失败：
// 任务表读不出来却报健康，会让升级/编排层以为实例可用。
type restoreState struct {
	mu  sync.Mutex
	err error
}

func (b *restoreState) set(err error) {
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
}

func (b *restoreState) get() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// corsState 是 CORS 白名单缓存（P42）：中间件在每个 API 请求上跑，设置不能每请求读一次。
type corsState struct {
	mu    sync.Mutex
	cache []string
	at    time.Time
}

// authState 是 JWT 签名密钥与登录节流。
type authState struct {
	// signKey 是 HS256 签名密钥（见 auth.go 的 signingKey）：改密码会**轮换**它以立刻
	// 吊销既有 token，而 requireAuth 每请求都要读，所以经方法访问、不直读字段。
	// jwtKeyPath 是本地镜像的落盘位置（baseDir/jwt.key）。权威副本在库里
	// （见 auth.go 的 jwtKeySetting），轮换时两边一起写；这个文件只服务于
	// 「回滚到只认文件的旧版本」和「库暂时读不到」。
	signKey    signingKey
	jwtKeyPath string
	login      loginGuard // 登录失败节流（见 auth.go）
}

// llmState 是 LLM 配置与全局 provider 的运行时状态。
type llmState struct {
	mu        sync.Mutex
	chatAgent *agent.ChatAgent // conversational runner for the chat page; nil w/o LLM
	// prov is the fully-decorated global provider (recorder + failover chain)
	// installed by applyLLM. Task routers use it after an explicit chain is cleared.
	prov    llm.Provider
	direct  llm.Provider // concrete global provider, before any failover pool
	cfg     agent.Config // current LLM config (key not exposed)
	on      bool
	profile string // active LLM profile name (for llmrec tagging)
}

// providerState 缓存按配置构建的 provider / ChatAgent，以及进程级的熔断状态。
type providerState struct {
	// mu 保护 chatAgents（chat 页按 profile 缓存的 ChatAgent，编辑 profile 后失效）。
	mu         sync.Mutex
	chatAgents map[int64]*agent.ChatAgent

	// cacheMu 保护 byProfile / cacheGen：每个 profile 只建一个 provider，让绑定或
	// pin 到同一 profile 的 agent 共享同一个实例——因而共享同一个限流器。
	cacheMu   sync.Mutex
	byProfile map[int64]*provEntry
	cacheGen  uint64

	// health 是 LLM 轮询的进程级熔断状态。刻意放在 provider 缓存**之外**：重建链
	// （保存无关 profile、改个开关）不该抹掉"哪个后端没额度/被限流"这件事。
	health *llmpool.Registry
}

// chatState 串行化每个任务的主 agent 运行：同一任务同时只跑一轮。
type chatState struct {
	mu   sync.Mutex
	busy map[string]bool
	// cancel 持有每个在跑会话的 cancel（按 convBusyKey），手动停止只中止那一个会话。
	cancel map[string]context.CancelCauseFunc
}

// agentState 是显式任务链使用的任务级 agent 组合。
type agentState struct {
	mu      sync.Mutex
	bundles map[string]*taskAgentBundle
}

// archiveState 是冷任务归档的后台工作者。
type archiveState struct {
	wake chan struct{}
	wg   sync.WaitGroup
}

// provEntry is a cached provider + its config for one LLM profile id.
type provEntry struct {
	prov llm.Provider
	cfg  agent.Config
}

// triggeredRun is one queued P3 trigger fire awaiting its turn for an agent.
// taskID + mergeable let the drainer coalesce several event triggers (finding/goal)
// from the SAME task into one conversation before it starts (interval fires don't merge).
type triggeredRun struct {
	agentKey  string
	title     string
	message   string // 事件正文(触发语 + 工具/入参/返回等);不含任务描述/目标头
	taskID    int64  // source task for finding/goal triggers; 0 for interval/none
	taskDesc  string // 任务描述(任务级,同任务相同);合并时只渲染一次
	taskGoal  string // 任务目标(任务级,同任务相同);合并时只渲染一次
	mergeable bool   // true for finding/goal event triggers (merge by taskID)
}

func New(ctx context.Context, m *Manager, skillDir string, dataDir string, keyDir string) *Server {
	key, err := loadOrCreateJWTKey(m.pg, keyDir, dataDir)
	if err != nil {
		log.Fatalf("[auth] JWT key: %v", err)
	}
	s := &Server{
		m: m, engine: NewEngine(m), ctx: ctx, skillDir: skillDir,
		chatRuns: chatState{
			busy:   map[string]bool{},
			cancel: map[string]context.CancelCauseFunc{},
		},
		triggers: newTriggerQueue(),
		providers: providerState{
			chatAgents: map[int64]*agent.ChatAgent{},
			byProfile:  map[int64]*provEntry{},
			health:     newLLMHealthRegistry(m.pg),
		},
		agents:  agentState{bundles: map[string]*taskAgentBundle{}},
		archive: archiveState{wake: make(chan struct{}, 1)},
	}
	// 预算耗尽 → 暂停任务（落库、取消主 Agent、并发对账），而不是终止任务出终局报告。
	// 用户继续后同一档预算不再打断；具体见 server/budget.go 的 pauseForBudget。
	s.engine.SetBudgetPauseHandler(func(t *Task) error {
		_, err := s.applyTaskControlWithCause(t, "pause", agent.AbortBudgetExhausted)
		return err
	})
	// 规划层的「无方向」出口（mark_strategy_exhausted 与停滞保护）走同一条完整暂停路径：
	// 先落一条 system 活动留痕（含原因），再暂停任务、取消在跑 Agent。不终止任务、不出报告。
	s.engine.SetPauseWithReasonHandler(func(t *Task, reason string) error {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			reason = "规划者判定当前已无可行方向"
		}
		s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
			Summary: "任务已暂停：" + clipRunes(reason, 120),
			Detail: "由规划层（mark_strategy_exhausted / 停滞保护）触发：平台已停止派发新意图并取消在跑的 Planner/Worker，" +
				"运行中的意图退回 open。这不是任务结束，也不会生成终局报告；人工确认后可在任务页继续。\n原因：" + reason})
		cause := agent.Causef("strategy_exhausted", "规划者判定无可行方向，任务已暂停",
			"规划层判定任务在当前授权范围与操作约束内已无可行方向：%s\n平台已停止派发新意图并暂停任务（运行中的意图退回 open）。这不是任务结束，也不会生成终局报告；人工确认后可在任务页继续。", reason)
		_, err := s.applyTaskControlWithCause(t, "pause", cause)
		return err
	})
	s.auth.signKey.set(key)
	s.auth.jwtKeyPath = filepath.Join(keyDir, jwtKeyFilename)
	s.initSideQuestions()
	// 熔断阈值/冷却是失败路径上的热参数，启动时把全局重试策略推给 Registry 一次；
	// 之后每次保存策略再推一次（saveLLMRetryPolicy）。
	s.applyRetryPolicy()
	// Every task uses a stable task router. An empty explicit chain is resolved by
	// that router through Agent bindings and then the global provider, so adding a
	// first chain to a running task takes effect on its very next LLM call.
	s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
		if !s.taskRuntimeAvailable(t, "planner", "worker") {
			return nil, nil
		}
		b := s.agentsForTask(t)
		return b.pl, b.wk
	})
	// Global readiness (Ready()/llm_configured): a global active LLM provider is
	// installed. Task-level runnability is separate (ReadyFor → the resolver above).
	s.engine.SetReadiness(func() bool {
		s.llm.mu.Lock()
		defer s.llm.mu.Unlock()
		return s.llm.on
	})
	if m.pg != nil {
		// 注入 agent 依赖的外部能力（prompt 模板 / 产物语言 / 收尾词覆盖）。
		// 工具类的三项由下面的 wireAgentAugment / wireTools 用「读-改-写」接上。
		agent.SetOverrides(agent.Overrides{
			// Wire DB-stored prompt templates into the agents (新版方案 §3.3 / §5a). With no
			// override row, agents keep their built-in defaults — behavior is unchanged.
			PromptOverride: func(key string) (string, bool) {
				a, err := m.pg.GetAgentByKey(key)
				if err != nil || a == nil {
					return "", false
				}
				t, err := m.pg.CurrentPrompt(a.ID)
				if err != nil || t == "" {
					return "", false
				}
				return t, true
			},
			// 追加段：渲染在正文之后、代码尾段之前。外部场景（benchmark）只写这一段，
			// 内置提示词改了它会自动跟随，不会漂移成旧分支。
			PromptAppend: func(key string) (string, bool) {
				a, err := m.pg.GetAgentByKey(key)
				if err != nil || a == nil {
					return "", false
				}
				t, err := m.pg.CurrentPromptAppend(a.ID)
				if err != nil || t == "" {
					return "", false
				}
				return t, true
			},
			// planner_templates 的默认模板（四级链第 3 级；任务快照与 agent_prompts 优先）。
			PlannerTemplateDefault: func() (string, bool) {
				body, err := m.pg.DefaultPlannerTemplateBody()
				if err != nil || strings.TrimSpace(body) == "" {
					return "", false
				}
				return body, true
			},
			// P14：领域化压缩模板（settings.compaction_prompt）。空/缺失 → 内置 7 段模板。
			CompactionPromptOverride: func() (string, bool) {
				v, ok, err := m.pg.GetSetting(settingCompactionPrompt)
				if err != nil || !ok || strings.TrimSpace(v) == "" {
					return "", false
				}
				return v, true
			},
			// P41 出站产物语言：agent 侧每 run 现读 → 改完即时生效，不需要重建 agent。
			ArtifactLanguage: func() string {
				v, ok, err := m.pg.GetSetting(settingArtifactLanguage)
				if err != nil || !ok {
					return agent.ArtifactLangZh
				}
				return strings.TrimSpace(v)
			},
			// Wire DB-stored wrap-up (settlement) prompts. Empty column → built-in default.
			WrapupOverride: func(key string) (string, bool) {
				a, err := m.pg.GetAgentByKey(key)
				if err != nil || a == nil || a.WrapupPrompt == "" {
					return "", false
				}
				return a.WrapupPrompt, true
			},
			// Wire DB-stored wrap-up turn budgets. 0 / missing → built-in per-agent default.
			WrapupMaxTurnsOverride: func(key string) (int, bool) {
				a, err := m.pg.GetAgentByKey(key)
				if err != nil || a == nil || a.WrapupMaxTurns <= 0 {
					return 0, false
				}
				return a.WrapupMaxTurns, true
			},
			// Wire DB-stored TASK-TIMEOUT wrap-up prompt/turns (worker/planner). Empty/0 → default.
			WrapupTaskTimeoutOverride: func(key string) (string, bool) {
				a, err := m.pg.GetAgentByKey(key)
				if err != nil || a == nil || a.TaskTimeoutWrapupPrompt == "" {
					return "", false
				}
				return a.TaskTimeoutWrapupPrompt, true
			},
			WrapupTaskTimeoutTurnsOverride: func(key string) (int, bool) {
				a, err := m.pg.GetAgentByKey(key)
				if err != nil || a == nil || a.TaskTimeoutWrapupMaxTurns <= 0 {
					return 0, false
				}
				return a.TaskTimeoutWrapupMaxTurns, true
			},
		})
		wireAgentAugment(m.pg, s.skillDir, s.hostTools) // 可见 skills/MCP + 流量/编排 host 工具装配进 agent 工具集
		domainReg := buildDomainReg(m.Assets())
		wireTools(m.pg, domainReg) // 内置工具表：按 agent 过滤 + 覆盖描述/schema + 注入默认值
		seedPrompts(m.pg)          // 内置 agent 默认提示词正文播种进 agent_prompts(仅空时)
		s.seedOrchestrationTools() // P2 跨任务编排工具 seed 进 tools 表(可按 agent 绑定)
		if err := s.seedFindingRetester(); err != nil {
			log.Printf("[retester] seed: %v", err)
		}
		// 启动期 goroutine 一律**捕获当时的 ctx**，不在 goroutine 里再读 s.ctx。
		// s.ctx 是可变字段（测试夹具在构造后用可用 ctx 覆盖它，见 finding_traffic_test），
		// 边读边写就是数据竞争——-race 会在 CI 上抓出来。启动期捕获一次，语义不变。
		svcCtx := s.ctx
		// 后台循环（证据 GC / 保留清理 / 触发器调度 / 推送投递）**不在测试里跑**。
		//
		// 它们连的是同一个测试库，且一直跑到测试进程退出：一个用 context.Background()
		// 建 Server 的用例（它自己早就结束了）就足以让它的 notifier 在**后面的**用例
		// 运行时继续按 3 秒 tick 遍历库里所有启用渠道、领取别人的投递行。这不是假想：
		// TestNotifyDigestSegmentsAndDefersRemainder 的偶发红就是这么来的——用例自己的
		// stepDigest 因为 SKIP LOCKED 领不到另一个 dispatcher 正锁着的行，于是「这一轮
		// 没有任何进展」，或者只领到 1 条、消息里没有「其余」。
		//
		// 用例都直接调用引擎的单步入口（stepDigest / stepRealtime / tickOnce /
		// runRetentionSweep），不依赖后台循环。sched 仍然要装配：停机一 tick 的行为
		// 就是通过 s.sched.tickOnce 断言的。
		s.seedPythonInterpreter() // 自定义脚本工具:开机检测 python 解释器入库(仅空时)
		s.installMCPGuard()       // P24 外部 MCP 的并发上限 + 熔断（每调用现读设置）
		// P35：把 Scheduler 存下来，让"停机时这一 tick 做了什么"可被断言（tickOnce）。
		s.sched = newScheduler(s)
		if !testing.Testing() {
			goSafe("server/evidence-gc", nil, func() { s.evidenceStore().RunGC(svcCtx) })
			// 数据库侧的保留清理（llm_usage / activity / server_logs / llm_records）。
			// 默认全 0 = 不清理，所以这里起 goroutine 不会删任何东西。
			s.startRetentionSweeper(svcCtx)
			goSafe("server/scheduler", nil, func() { s.sched.Run(svcCtx) }) // P3 触发器调度(定时/finding/目标事件),仅自定义 agent
			// 漏洞 IM 推送投递引擎。与 Scheduler 并列但独立：推送的实时性要求(3s)
			// 与触发器的业务节奏不同，且两者失败互不牵连——推送卡住不该影响 agent 触发。
			goSafe("server/notifier", nil, func() { newNotifier(s).Run(svcCtx) })
		}
		// Fill the tool cache for any enabled MCP that has none yet (notably the
		// seeded browser MCP on first run). Async so it never blocks startup.
		goSafe("server/discover-mcps", nil, func() { s.discoverEmptyMCPsOnStartup(svcCtx) })
		logSink.SetDB(ctx, m.pg) // restore last 100 log rows and enable async persistence
	}
	// precedence: persisted DB config > env.
	if cfg, ok := s.loadLLMConfig(); ok {
		if err := s.applyLLM(cfg); err != nil {
			log.Printf("[engine] saved LLM config init failed — engine idle: %v", err)
		} else {
			log.Printf("[engine] LLM configured from DB: %s / %s", cfg.Provider(), cfg.Model)
		}
	} else if cfg, ok := agent.FromEnv(); ok {
		if err := s.applyLLM(cfg); err != nil {
			log.Printf("[engine] env provider init failed — engine idle: %v", err)
		} else {
			log.Printf("[engine] LLM configured from env: %s / %s", cfg.Provider(), cfg.Model)
		}
	} else {
		log.Printf("[engine] no LLM provider configured — engine idle until set via /api/llm or env")
	}
	// 注意：任务恢复(RestoreTaskRuntimes)不在这里——构造器只做装配、不启动后台任务。
	// 生产路径由 main 显式调用（重启后恢复任务）；测试构造服务器时因此不会被库里
	// 遗留的未终态任务"劫持"：那些任务会真跑起来、把文件写进测试的临时工作目录，
	// 与 t.TempDir() 的清理竞态（偶发 "directory not empty"）。
	goSafe("server/reconcile-concurrency", nil, func() { s.reconcileConcurrency() })
	s.startTaskArchiveWorker()
	s.wireInterceptReviewer() // LLM 兜底审批:未命中拦截规则的命令交给模型判定
	return s
}

// RestoreTaskRuntimes rebuilds in-memory task handles from PG at boot and restarts
// the loops of every task that had been admitted (reload-resume), resetting stale
// running intents on the way. It is a BOOT action, deliberately not part of New:
// a constructor that starts background work makes any test that builds a server
// inherit whatever tasks happen to sit in the shared database.
// Restored deadline and worker loops must inherit the same context as new tasks,
// including the side-question checkpoint publisher installed during startup.
func (s *Server) RestoreTaskRuntimes() {
	m := s.m
	// P13：先把上一进程遗留的活跃执行对账成 orphaned（不是 failed——没有任何观察），
	// 再拉起周期巡检；必须在任务循环启动前完成，否则收尾门会把这些僵尸行当成在跑。
	if x := m.Executions(); x != nil {
		x.StartOrphanReconciler(s.ctx)
	}
	// 上次进程若在普通删除中途退出，先把暂存目录按 journal 收尾（回滚或完成删除），
	// 再重建任务句柄——否则残留文件既回不去也没人清。
	m.RecoverDeleteStaging()
	// reload tasks persisted on disk so the task list survives a restart, and
	// restore persisted paused state (so a task paused before restart stays paused).
	existing, err := m.LoadExisting()
	if err != nil {
		// 恢复失败不做半套：不启动任何任务循环，health 报 503，日志留可读原因。
		s.boot.set(err)
		log.Printf("[engine] 启动恢复失败，任务循环未启动：%v", err)
		return
	}
	for _, t := range existing {
		lifecycle := t.lifecycleSnapshot()
		// clear stale 'running' intents from a prior crash/restart (no live worker
		// owns them) so they re-claim instead of spinning forever in the UI.
		if n, _ := t.Store.ResetRunningIntents(); n > 0 {
			log.Printf("[engine] task %s 重置 %d 个残留 running 意图为 open", t.ID, n)
		}
		if lifecycle.Paused {
			s.engine.Pause(t.ID, agent.AbortPausedOnReload)
		}
		// 任务级协调器:为每个未终态任务起 deadline 协调器(超时到点收尾 + token 预算 +
		// 意图租约看门狗),独立于 planner/worker loop——非活跃任务重启后也能在到点后被
		// 收尾(deadline 已过则立即走收尾时序),僵死的 running 意图也会被看门狗回收。
		if !isTerminalStatus(lifecycle.Status) {
			s.engine.startDeadlineCoordinator(s.ctx, t)
		}
	}
	// Restore every task that had already been admitted before shutdown. Starting
	// only the active UI task left other non-queued tasks counted as concurrency
	// occupants without live loops, which could permanently block the persistent
	// FIFO. Paused loops remain idle; queued tasks are admitted below as slots allow.
	for _, t := range m.List() {
		lifecycle := t.lifecycleSnapshot()
		if !lifecycle.Queued && !isTerminalStatus(lifecycle.Status) {
			s.engine.Run(s.ctx, t)
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// /api/* goes through CORS + JWT; everything else is served by the embedded
	// frontend (public — auth is enforced client-side and on the API). With the
	// no-embed build the webui handler just 404s (run `next dev` separately).
	api := withRequestID(cors(s.requireAuth(mux), s.corsAllowedOrigins))
	root := http.NewServeMux()
	root.Handle("/api/", api)
	root.Handle("/", s.webuiHandler())
	return securityHeaders(root)
}

// securityHeaders 给整站（页面与 API）加一组保守的安全响应头。
//
// 页面是静态导出：壳 HTML 公开、鉴权在 API 侧，所以登录页原本可以被任意站点 iframe
// （点击劫持）。X-Frame-Options + frame-ancestors 把这条路封死。
//
// CSP 主要给 XSS 兜底：token 存在 localStorage 里，一旦有脚本注入就等于丢 7 天凭证。
// 这里只允许同源脚本/样式/连接；`script-src` 的 'unsafe-inline' 是 Next 静态导出必须的
// （页面里有内联 bootstrap 脚本），要换成 nonce 得改成服务端渲染。
// `img-src` 不含外部 https：markdown 里的外链图片会被挡掉——这是有意的（防信标/外泄），
// 确有需要时再放开。
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// routeRegistrar 是路由注册面的最小接口：Handler() 传 *http.ServeMux，测试传
// 记录型实现，把**真实注册**的路由清点出来（契约测试与路由快照都用它）。
//
// 为什么必须是运行时清点、而不是扫源码字面量：registerSideRoutes 用循环生成
// 9 条路由、registerFindingTraffic 用字符串拼接生成 7 条——AST 只认 BasicLit，
// 这 16 条它一条也看不见（此前契约测试正是这样漏掉整个 traffic 改动面的）。
type routeRegistrar interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// registerRoutes 是**唯一**的路由注册入口：所有 /api/ 路由都在这里挂上。
// 以后拆成多个 registerXxx 也必须从这里串起来，否则契约测试与路由快照都看不到。
func (s *Server) registerRoutes(mux routeRegistrar) {
	s.registerSideRoutes(mux)

	// 按域拆分，顺序与原单块一致（Go 1.22+ 的 ServeMux 按特异性匹配，
	// 与注册顺序无关；但保持顺序能让 diff 更好读）。
	s.registerSystemRoutes(mux)
	s.registerTaskRoutes(mux)
	s.registerWorkspaceRoutes(mux)
	s.registerAssetRoutes(mux)
	s.registerExplorationRoutes(mux)
	s.registerMetricsRoutes(mux)
	s.registerChatRoutes(mux)
	s.registerAgentRoutes(mux)
	s.registerPlaybookRoutes(mux)
	s.registerCVERoutes(mux)
}

// --- handlers ---

// health 是**唯一免认证**的信息端点，所以它必须**不自报家门**（P42）。
//
// 它此前返回 {"ok":true,"service":"blade","version":"v0.4.x"}——那是个明牌指纹：公网上任何人
// 请求一次就知道这是什么、什么版本（测绘引擎连指纹比对都省了）。现在只回一个 ok；版本与构建
// 信息移到需要登录的 /api/system/info。
//
// 保留它免认证是必须的：反向代理/隧道/监控的健康检查都靠它，而它们通常没有 token。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.boot.get(); err != nil {
		// 不把原始错误（可能含数据库地址）回给免认证端点；细节在服务端日志。
		writeJSON(w, 503, map[string]any{"ok": false, "error": "启动恢复未完成：任务状态不可用，请查看服务端日志"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// systemInfo 是需要登录的版本/构建信息（P42 从 /api/health 搬过来）。
func (s *Server) systemInfo(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"ok": true, "service": "blade", "version": BuildVersion}
	if e := s.m.Enrich(); e != nil {
		out["enrich"] = e.Stats()
	}
	writeJSON(w, 200, out)
}

// cors 只对**显式配置**的来源发 CORS 头（P42）。此前无条件发
// `Access-Control-Allow-Origin: *`——单机自用平台的前端与 API 同源，根本不需要它；而"无脑全开放"
// 既是可指纹特征，也把 API 暴露给任意网页的跨源读取。
//
// 需要跨源时把来源填进设置 `cors_allowed_origins`（逗号分隔；填 "*" 才是真的全开放，得自己写出来）。
func cors(next http.Handler, allowed func() []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
			for _, a := range allowed() {
				if a == "*" || strings.EqualFold(a, origin) {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
					break
				}
			}
		}
		// 预检照旧快速回 204：不在白名单时它不带任何 CORS 头，浏览器自然会拒。
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// corsAllowedOrigins 读设置 `cors_allowed_origins`（逗号分隔），带 60 秒缓存——
// 这个中间件在每个 API 请求上跑，每请求读一次库不值当（拦截层按"每次工具调用"读设置，
// 那是另一个量级）。
func (s *Server) corsAllowedOrigins() []string {
	s.cors.mu.Lock()
	defer s.cors.mu.Unlock()
	if time.Since(s.cors.at) < time.Minute {
		return s.cors.cache
	}
	var out []string
	if v, ok, err := s.m.pg.GetSetting(settingCORSAllowedOrigins); err == nil && ok {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	s.cors.cache, s.cors.at = out, time.Now()
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func atoiDefault(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return d
}
