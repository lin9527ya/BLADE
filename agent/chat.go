package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/guard"
	"github.com/lin9527ya/blade/intercept"
)

// ChatAgent is the generic, task-independent conversational runner behind the chat
// page. It generalizes MainAgent.Chat: any agent (built-in OR a custom one, by
// key) can be chatted with, multi-turn history resumed from the transcript. It is
// a PURE ASSISTANT — base tools are the SDK DefaultTools (Bash/Read/Write/Edit/
// LS/Glob/Grep) plus whatever skills/MCP the key is made visible; NO pentest
// graph/task context is injected (that stays exclusive to MainAgent).
// MemberRuntimeOptions captures the per-profile options resolved dynamically
// from an active or failover-selected pool member for a given session.
type MemberRuntimeOptions struct {
	WindowTokens         int
	MaxTokens            int
	NonStreaming         bool
	CacheTTL             string
	PromptCacheRetention string
	PreserveThinking     bool
}

type ChatAgent struct {
	prov               llm.Provider
	model              string
	workDir            string
	tx                 *transcript.Store
	window             int
	windowFn           func() int // dynamic resolver (e.g. active llmpool member)
	proxyAddr          string
	proxyCACert        string
	proxyFn            func() (string, string) // dynamic resolver (proxy-pool failover); nil = static
	webSearch          WebSearchOpts
	webSearchFn        func() WebSearchOpts // dynamic resolver so settings updates apply without cache invalidation
	guard              *guard.Guard         // optional; nil disables intercept hooks for chat
	nonStreamingFn     func() bool          // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn       func() bool          // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn        func() int           // resolver: per-reply output cap (nil/0 = send no cap)
	cacheOpts          cacheOptionResolvers // prompt-cache TTL/retention & thinking-preservation options
	memberOptsFn       func(sessionID string) (MemberRuntimeOptions, bool)
	customWindow       bool
	customNonStreaming bool
	customMaxTokens    bool
	customCache        bool
}

func NewChatAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window int) *ChatAgent {
	return &ChatAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window}
}

// SetMemberOptionsResolver wires a per-session pool member options resolver so
// sticky failover members drive runtime options without overriding explicit test setters.
func (c *ChatAgent) SetMemberOptionsResolver(fn func(sessionID string) (MemberRuntimeOptions, bool)) {
	c.memberOptsFn = fn
}

// SetCompactionWindowResolver wires a dynamic context window resolver (in tokens).
func (c *ChatAgent) SetCompactionWindowResolver(fn func() int) {
	c.windowFn = fn
	c.customWindow = true
}

func (c *ChatAgent) compactionWindow() int {
	if c.windowFn != nil {
		if win := c.windowFn(); win > 0 {
			return win
		}
	}
	return c.window
}

// SetCacheOptions wires per-run resolvers for CacheTTL, PromptCacheRetention, and PreserveThinking.
func (c *ChatAgent) SetCacheOptions(ttlFn, retentionFn func() string, preserveThinkingFn func() bool) {
	c.cacheOpts = cacheOptionResolvers{cacheTTLFn: ttlFn, retentionFn: retentionFn, preserveThinkingFn: preserveThinkingFn}
	c.customCache = true
}

// SetNonStreaming wires a resolver deciding whether chat runs use the
// non-streaming model path (true = non-streaming). nil/unset = streaming.
func (c *ChatAgent) SetNonStreaming(fn func() bool) {
	c.nonStreamingFn = fn
	c.customNonStreaming = true
}

func (c *ChatAgent) nonStreaming() bool { return c.nonStreamingFn != nil && c.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether chat runs use the experimental
// noa context-compression mechanism. nil/unset = off (built-in compaction). Read
// per run so the settings toggle takes effect without rebuilding the agent.
func (c *ChatAgent) SetNoaEnabled(fn func() bool) { c.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (c *ChatAgent) SetMaxTokens(fn func() int) {
	c.maxTokensFn = fn
	c.customMaxTokens = true
}

func (c *ChatAgent) maxTokens() int {
	if c.maxTokensFn == nil {
		return 0
	}
	return c.maxTokensFn()
}

// SetProxy points the chat agent's WebFetch/Bash at the recording proxy plus the
// CA cert it trusts (empty addr = direct). Kept for parity with the other agents.
func (c *ChatAgent) SetProxy(addr, caCert string) {
	c.proxyAddr, c.proxyCACert, c.proxyFn = addr, caCert, nil
}

// SetProxyFunc resolves egress per run (proxy-pool failover without a rebuild).
func (c *ChatAgent) SetProxyFunc(fn func() (string, string)) { c.proxyFn = fn }

func (c *ChatAgent) proxySnapshot() (string, string) {
	if c.proxyFn != nil {
		return c.proxyFn()
	}
	return c.proxyAddr, c.proxyCACert
}

// SetWebSearch selects the web_search backend for the chat agent (off by default).
func (c *ChatAgent) SetWebSearch(o WebSearchOpts) { c.webSearch = o }

// SetWebSearchFunc wires a per-turn dynamic resolver so settings/agent switches
// take effect immediately on the next chat turn without cache invalidation.
func (c *ChatAgent) SetWebSearchFunc(fn func() WebSearchOpts) { c.webSearchFn = fn }

// SetGuard attaches a guard (with user-configured intercept rules) to this chat
// agent. Must be called before Chat; safe to call multiple times.
func (c *ChatAgent) SetGuard(g *guard.Guard) { c.guard = g }

// chatWorkDirSpec returns a working-directory notice appended to every chat
// agent's system prompt. Mirrors artifactSpec but without pentest-specific
// wording ("payload", "抓响应体") that would be odd in a general assistant.
func chatWorkDirSpec(workDir string) string {
	return "\n\n**文件输出规约**：需要写文件时，一律写到工作目录 " + workDir + "（这是默认 CWD，相对路径即落在这里，也可用该绝对路径）——不要写 /tmp 或其他绝对路径。"
}

// chatSystemParts splits the chat prompt into the cross-session static body
// (before DynamicBoundary) and the per-session working-directory spec (after
// DynamicBoundary) so sessions sharing an agent key reuse the same system cache.
func chatSystemParts(agentKey, dataDir, workDir string) (staticPart, dynamicPart string) {
	staticPart = renderSystem(agentKey, DefaultAssistantPrompt, chatVars{DataDir: dataDir, Now: nowStr()})
	dynamicPart = strings.TrimSpace(chatWorkDirSpec(workDir))
	return staticPart, dynamicPart
}

// chatSystem renders the DB-managed prompt body for agentKey. Custom agents have
// no per-key in-code default, so DefaultAssistantPrompt is the render fallback.
func chatSystem(agentKey, dataDir, workDir string) string {
	staticPart, _ := chatSystemParts(agentKey, dataDir, workDir)
	return staticPart + chatWorkDirSpec(workDir)
}

// chatVars carries the runtime variables a custom agent's prompt may reference.
// DataDir (server data root) + Now (server wall-clock, refreshed each turn) are the
// universal ones; any other {{.X}} fails to render and falls back to
// DefaultAssistantPrompt.
type chatVars struct{ DataDir, Now string }

// Chat runs ONE turn of a conversation with the agent identified by agentKey,
// resuming prior history keyed by sessionID. maxTurns is the per-turn agent step
// budget (0 = unlimited). maxDuration is the wall-clock run budget per turn
// (0 = unlimited); the timer resets each time Chat is called, so a new user
// message always starts a fresh countdown. webSearch gates network search for
// THIS agent (the global backend/key still come from the chat agent's config,
// but each agent decides on/off). emit receives each execution step (thinking /
// tool_use / tool_result / text / result), tagged with the agent key as the
// worker lane.
func (c *ChatAgent) Chat(ctx context.Context, agentKey, sessionID, message string, maxTurns int, maxDuration time.Duration, webSearch bool, emit func(db.Activity)) (string, error) {
	_, proxyCA := c.proxySnapshot()
	// gate the global web-search opts by this agent's own flag.
	ws := resolveWebSearchOpts(c.webSearch, c.webSearchFn)
	if !webSearch {
		ws.Enabled = false
	}

	// Per-session working directory: <workDir>/sessions/<sessionID>/
	// Isolates file writes across conversations, mirroring how workers use i<intentID>/.
	sessionWorkDir := filepath.Join(c.workDir, "sessions", sessionID)
	_ = os.MkdirAll(sessionWorkDir, 0o755)
	ctx = intercept.WithReviewWorkingDirectory(ctx, sessionWorkDir)

	// Pure assistant: DefaultTools as the base; AugmentTools layers in the key's
	// visible skills/MCP and lets the DB tools table filter/override. DefaultTools
	// have no tools-table rows, so they always pass through.
	base := actool.DefaultTools()
	// Preserve a task/exploration attribution attached by the caller (triggered
	// reporter/retester runs); only fill the session id.
	ri := RunInfoFrom(ctx)
	ri.SessionID = sessionID
	ctx = WithRunInfo(ctx, ri)
	tools, def, cleanup := AugmentTools(ctx, agentKey, base)
	defer cleanup()
	tools = append(tools, newBLADEWebFetch(c.proxySnapshot, proxyCA, ri.TaskID))
	if ws.Enabled {
		tools = append(tools, NewWebSearchToolWithResolver(func() WebSearchOpts {
			resolved := resolveWebSearchOpts(c.webSearch, c.webSearchFn)
			if !webSearch {
				resolved.Enabled = false
			}
			return resolved
		}))
	}

	staticSys, dynamicSys := chatSystemParts(agentKey, c.workDir, sessionWorkDir)
	system, boundary := deferredSystemWithDynamic(staticSys, def, dynamicSys)
	opts := agentcore.Options{
		Provider:                c.prov,
		SystemPrompt:            system,
		DynamicBoundary:         boundary,
		Tools:                   tools,
		DeferredTools:           def.Deferred,
		UnlockSet:               def.Unlock,
		PermissionMode:          permission.ModeBypass,
		EnableWebFetch:          false, // BLADE-owned WebFetch（带拟真 UA + 记录代理 + CA 校验）挂在 Tools 里
		OmitCurrentDateReminder: true,
		PromptCacheKey:          sessionID,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:       false, // BLADE-owned web_search（含 Brave HTML 回退）挂在 Tools 里
		WebSearchBackend:      ws.Backend,
		BraveSearchAPIKey:     ws.BraveKey,
		TavilySearchAPIKey:    ws.TavilyKey,
		DeepSeekSearchBaseURL: ws.DeepSeekBaseURL, DeepSeekSearchHeaders: ws.Headers, CommandCodeSearchBaseURL: ws.CommandCodeBaseURL, CommandCodeSearchAPIKey: ws.CommandCodeAPIKey,
		DeepSeekSearchAPIKey: ws.DeepSeekAPIKey,
		DeepSeekSearchModel:  ws.DeepSeekModel,
		WebSearchProxy:       ws.Proxy,
		// Bash 子命令默认走代理+信任 CA。每次工具调用现取（出口可能在 run 中途
		// 恢复/切换），不把启动瞬间的快照冻结整轮。
		BashEnvFunc: func() []string {
			addr, ca := c.proxySnapshot()
			return append(proxyEnv(addr, ca), TaskSubprocessEnv(sessionWorkDir, ri.TaskID)...)
		},
		WorkingDir:  sessionWorkDir,
		MaxTurns:    maxTurns,
		MaxDuration: maxDuration,
		Todos:       actool.NewTodoStore(),
		// large tool output spills to cmd-output/ under the session dir.
		// 截断上限用 SDK 默认(tool.Capture 的 30000 字符)。
		ToolOutputDir: filepath.Join(sessionWorkDir, "cmd-output"),
		TaskOutputDir: taskOutDir(sessionWorkDir),
		// 命中预算(步数)→ SDK 跑收尾:输出一句总结。Prompt 与收尾轮数按本 agent key 后台可编辑
		// (自定义 agent 各自一份;留空/0 用通用默认:10 轮)。
		Settlement:   wrapupSettlement(agentKey, nil),
		NonStreaming: c.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    c.maxTokens(),    // 0 = 不发上限,由服务端默认值决定
	}
	var mo MemberRuntimeOptions
	var hasMO bool
	if c.memberOptsFn != nil {
		mo, hasMO = c.memberOptsFn(sessionID)
	}
	if !c.customNonStreaming && hasMO {
		opts.NonStreaming = mo.NonStreaming
	}
	if !c.customMaxTokens && hasMO {
		opts.MaxTokens = mo.MaxTokens
	}
	if !c.customCache && hasMO {
		opts.CacheTTL = mo.CacheTTL
		opts.PromptCacheRetention = mo.PromptCacheRetention
		opts.PreserveThinking = mo.PreserveThinking
	} else {
		c.cacheOpts.apply(&opts)
	}
	if c.guard != nil {
		opts.Hooks = c.guard.Hooks()
	}
	if c.tx != nil { // persist raw human↔AI conversation; one accumulating file per thread
		opts.Transcript = c.tx
		opts.SessionID = sessionID
	}
	// 实验功能:开启后由 noa 接管上下文压缩(归档集中在 <workDir>/noa/<SessionID> 下,持久)。
	noaSession := "chat-" + sessionID
	win := c.compactionWindow()
	if !c.customWindow && hasMO && mo.WindowTokens > 0 {
		win = mo.WindowTokens
	}
	noaSt := enableNoa(&opts, c.noaEnabledFn, c.workDir, noaSession, win, noaWarn(noaSession))
	// P14 领域上下文：独立会话没有探索图（index 传 nil），收益在摘要侧——领域模板强制
	// 逐字保留"所有用户消息"，原文落盘可回读。
	InstallContextManager(&opts, win, opts.MaxTokens, sessionWorkDir, nil)
	attachNoaState(opts.Compactor, noaSt)
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload prior conversation so the agent has context across turns (each Chat is
	// a fresh session). First turn: no file yet → Resume loads nothing and proceeds.
	if c.tx != nil {
		_ = s.Resume(sessionID)
	}
	// re-unlock skill-gated MCPs from prior Skill() calls in the reloaded history so
	// revealed tools stay callable across the fresh session.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, _, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit != nil {
			r.Worker = agentKey
			emit(r)
		}
	})
	return text, err
}
