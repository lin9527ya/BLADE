// LLM 配置：读写、探测、按 agent/配置解析。
// 从 server.go 拆出：纯搬运，零行为变化。
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/llmpool"
	"github.com/lin9527ya/blade/llmrec"
)

// agentMaxTurns returns the configured max_turns for an agent key (0 = unlimited,
// also the fallback when no DB or no row).
func (s *Server) agentMaxTurns(key string) int {
	if s.m.pg == nil {
		return 0
	}
	a, err := s.agentRow(key)
	if err != nil || a == nil {
		return 0
	}
	return a.MaxTurns
}

// agentRunSeconds returns the configured wall-clock run budget (seconds) for an
// agent key (0 = unlimited; 1200 fallback when no DB or no row, matching schema).
func (s *Server) agentRunSeconds(key string) int {
	if s.m.pg == nil {
		return 1200
	}
	a, err := s.agentRow(key)
	if err != nil || a == nil {
		return 1200
	}
	return a.RunSecs
}

func (s *Server) profileToAgentConfig(p *db.LLMProfile) agent.Config {
	cfg := agent.ConfigFrom(p.Format, p.Model, p.BaseURL, p.APIKey, p.Proxy)
	cfg.RatePerSecond, cfg.RatePerMinute = p.RatePerSecond, p.RatePerMinute
	cfg.ContextWindowK = p.ContextWindowK
	cfg.ThinkingType = p.ThinkingType
	cfg.ThinkingBudgetTokens = p.ThinkingBudgetTokens
	cfg.ReasoningEffort = p.ReasoningEffort
	cfg.Stream = p.Streaming
	cfg.MaxTokens, cfg.MaxTokensField = p.MaxTokens, p.MaxTokensField
	cfg.SessionHeaderKey = p.SessionHeaderKey
	cfg.CustomHeaders = p.CustomHeaders
	cfg.InputTokensIncludesCache = p.InputTokensIncludesCache
	cfg.PreserveThinkingBlocks = p.PreserveThinkingBlocks
	if cfg.Format == llm.FormatAnthropic {
		cfg.CacheBreakpoints = agent.ResolveAnthropicCacheBreakpoints(p.CacheBreakpoints)
		cfg.CacheTTL = p.CacheTTL
	} else {
		cfg.CacheBreakpoints = p.CacheBreakpoints
		cfg.PromptCacheRetention = p.PromptCacheRetention
	}
	s.applyProfileRetry(&cfg, p)
	return cfg
}

// loadLLMConfig reads the active LLM profile from PG (llm_profiles).
func (s *Server) loadLLMConfig() (agent.Config, bool) {
	p, err := s.m.pg.ActiveProfile()
	if err != nil || p == nil {
		return agent.Config{}, false
	}
	cfg := s.profileToAgentConfig(p)
	if cfg.APIKey == "" {
		return cfg, false
	}
	s.llm.mu.Lock()
	s.llm.profile = p.Name
	s.llm.mu.Unlock()
	return cfg, true
}

// saveLLMConfig persists the LLM config as the active "default" profile in PG.
func (s *Server) saveLLMConfig(cfg agent.Config) error {
	// cfg.Provider() already returns one of the three valid format strings
	// (anthropic / openai / openai-responses), matching the DB CHECK constraint.
	format := cfg.Provider()
	var id int64
	// 这个 legacy 端点的请求体不含轮询/收发/输出上限参数,故把库里已存的值原样带回 —
	// 否则每次保存都会把 profile 的 priority、pool_exclude、streaming 以及输出上限
	// (max_tokens / max_tokens_field)悄悄重置成零值。
	var priority int
	var poolExclude bool
	streaming := true // 旧库/新建默认流式
	var maxTokens int
	var maxTokensField string
	var sessionHeaderKey string
	var customHeaders map[string]string
	var inputTokensIncludesCache, preserveThinkingBlocks bool
	var cacheBreakpoints, cacheTTL, promptCacheRetention string
	var retry db.RetryOverride
	if profs, _ := s.m.pg.ListProfiles(); profs != nil {
		for _, p := range profs {
			if p.Name == "default" {
				id, priority, poolExclude, streaming = p.ID, p.Priority, p.PoolExclude, p.Streaming
				maxTokens, maxTokensField = p.MaxTokens, p.MaxTokensField
				sessionHeaderKey = p.SessionHeaderKey
				customHeaders = p.CustomHeaders
				inputTokensIncludesCache = p.InputTokensIncludesCache
				preserveThinkingBlocks = p.PreserveThinkingBlocks
				cacheBreakpoints = p.CacheBreakpoints
				cacheTTL = p.CacheTTL
				promptCacheRetention = p.PromptCacheRetention
				retry = p.Retry
				break
			}
		}
	}
	if format == "anthropic" && cacheBreakpoints == "" {
		cacheBreakpoints = "rolling"
	}
	newID, err := s.m.pg.SaveProfile(&db.LLMProfile{
		ID: id, Name: "default", Format: format, Model: cfg.Model, BaseURL: cfg.BaseURL, Proxy: cfg.Proxy,
		APIKey: cfg.APIKey, RatePerSecond: cfg.RatePerSecond, RatePerMinute: cfg.RatePerMinute,
		ContextWindowK: cfg.ContextWindowK, ThinkingType: cfg.ThinkingType, ReasoningEffort: cfg.ReasoningEffort, IsDefault: true,
		Priority: priority, PoolExclude: poolExclude, Streaming: streaming,
		MaxTokens: maxTokens, MaxTokensField: maxTokensField, SessionHeaderKey: sessionHeaderKey,
		CustomHeaders: customHeaders, InputTokensIncludesCache: inputTokensIncludesCache,
		PreserveThinkingBlocks: preserveThinkingBlocks, CacheBreakpoints: cacheBreakpoints,
		CacheTTL: cacheTTL, PromptCacheRetention: promptCacheRetention, Retry: retry,
	})
	if err != nil {
		return err
	}
	return s.m.pg.SetActiveProfile(newID)
}

// reapplyActiveProfile hot-reloads the engine from the active DB profile so that
// saving or activating a profile takes effect without a restart. Best-effort:
// logs on failure and leaves the running engine untouched.
func (s *Server) reapplyActiveProfile() {
	cfg, ok := s.loadLLMConfig()
	if !ok {
		return
	}
	if err := s.applyLLM(cfg); err != nil {
		log.Printf("[engine] reapply active profile failed: %v", err)
		return
	}
	log.Printf("[engine] LLM reapplied from active profile: %s / %s", cfg.Provider(), cfg.Model)
}

// webSearchFor gates the global web-search opts (backend/key) by an agent's own
// web_search flag: the backend/key come from the global config, each agent decides on/off.
func (s *Server) webSearchFor(key string) agent.WebSearchOpts {
	o := s.m.WebSearchOpts()
	if a, err := s.agentRow(key); err != nil || a == nil || !a.WebSearch {
		o.Enabled = false
	}
	return o
}

// nonStreamingResolver returns a resolver capturing a profile's streaming choice.
// The agents read it per run; a profile change rebuilds the agents (applyLLM),
// so the captured value is always the one in effect for this build.
func nonStreamingResolver(cfg agent.Config) func() bool {
	nonStreaming := !cfg.Stream
	return func() bool { return nonStreaming }
}

// maxTokensResolver mirrors nonStreamingResolver for the per-reply output cap.
func maxTokensResolver(cfg agent.Config) func() int {
	maxTokens := cfg.MaxTokens
	return func() int { return maxTokens }
}

func (s *Server) newConfiguredChatAgent(prov llm.Provider, cfg agent.Config) *agent.ChatAgent {
	tx := transcript.NewStore(filepath.Join(s.m.dir, "transcripts"))
	ca := agent.NewChatAgent(prov, cfg.Model, s.m.dir, tx, cfg.CompactionWindow())
	ca.SetProxyFunc(s.m.ProxySnapshot)
	ca.SetWebSearch(s.m.WebSearchOpts())
	ca.SetWebSearchFunc(s.m.WebSearchOpts)
	ca.SetGuard(s.chatGuard())
	if pool, ok := prov.(*llmpool.Pool); ok && pool != nil {
		ca.SetCompactionWindowResolver(func() int {
			if m := pool.CurrentMember(""); m != nil && m.WindowTokens > 0 {
				return m.WindowTokens
			}
			return cfg.CompactionWindow()
		})
		ca.SetNonStreaming(func() bool {
			if m := pool.CurrentMember(""); m != nil {
				return m.NonStreaming
			}
			return !cfg.Stream
		})
		ca.SetMaxTokens(func() int {
			if m := pool.CurrentMember(""); m != nil {
				return m.MaxTokens
			}
			return cfg.MaxTokens
		})
		ca.SetCacheOptions(
			func() string {
				if m := pool.CurrentMember(""); m != nil && m.ProfileConfigured {
					return m.CacheTTL
				}
				return cfg.CacheTTL
			},
			func() string {
				if m := pool.CurrentMember(""); m != nil && m.ProfileConfigured {
					return m.PromptCacheRetention
				}
				return cfg.PromptCacheRetention
			},
			pool.PreservesThinking,
		)
	} else {
		ca.SetNonStreaming(nonStreamingResolver(cfg))
		ca.SetMaxTokens(maxTokensResolver(cfg))
		ca.SetCacheOptions(func() string { return cfg.CacheTTL }, func() string { return cfg.PromptCacheRetention }, cfg.PreservesThinking)
	}
	ca.SetNoaEnabled(s.m.NoaCompactionEnabled) // 实验功能:noa 上下文压缩(每 run 读)
	return ca
}

// applyLLM (re)builds the planner/worker/main-agent from cfg and installs them on
// the running engine as the GLOBAL active pair. Safe to call at runtime (UI configures LLM).
func (s *Server) applyLLM(cfg agent.Config) error {
	var prov llm.Provider
	// A persisted active profile must use the same cached provider as task chains
	// and Agent bindings, otherwise each path owns a separate rate limiter.
	if active, _ := s.m.pg.ActiveProfile(); active != nil {
		if activeCfg, ok := s.loadProfileConfig(active.ID); ok && reflect.DeepEqual(activeCfg, cfg) {
			prov, _, _ = s.providerForProfile(active.ID)
		}
	}
	if prov == nil {
		var err error
		prov, err = cfg.NewProviderWithLimiter(s.requestLimiterForProfile(0))
		if err != nil {
			return err
		}
		// Non-persisted/env configs do not have a profile cache key.
		s.llm.mu.Lock()
		profName := s.llm.profile
		s.llm.mu.Unlock()
		prov = llmrec.Wrap(prov, s.m.PG(), cfg.Model, profName, cfg.ThinkingType, cfg.ReasoningEffort, s.m.LLMRecordEnabled, BuildVersion)
		prov = bindSideProvider(prov, cfg, 0, profName)
	}
	s.llm.mu.Lock()
	s.llm.direct = prov
	s.llm.mu.Unlock()
	// LLM 轮询(默认关):把激活配置包进故障转移链,当前配置不可用时自动切下一个。
	// 只影响「走全局激活配置」的这条路径——agent 绑定 / 任务 pin 的走 providerForProfile,
	// 默认仍然独占该配置(见 poolForBinding)。关闭或无备选时返回原 provider,行为不变。
	if act, err := s.m.pg.ActiveProfile(); err == nil && act != nil {
		prov = s.poolForActive(act.ID, prov, cfg)
	}
	// No global planner/worker pair: every task runs on its own task-routed pair
	// (agentsForTask), resolved through the engine's authoritative resolver. Global
	// readiness (Ready()/llm_configured) is reported from s.llm.on, set below.

	win := cfg.CompactionWindow()
	_ = win
	// chat stays the GLOBAL fallback since one ChatAgent serves many agent keys — its
	// per-agent binding is resolved at Chat time (runConversationSync →
	// chatAgentForProfile). The main-agent has no global instance: each task builds its
	// own via agentsForTask (task-routed), so nothing is constructed for it here.
	ca := s.newConfiguredChatAgent(prov, cfg)
	s.llm.mu.Lock()
	// chat agent serves MANY custom agents by key → it holds the GLOBAL opts
	// (backend/key) and gates Enabled per-conversation-agent at Chat time. 对话始终用激活配置。
	s.llm.chatAgent = ca // chat page runner
	s.llm.prov = prov
	s.llm.cfg = cfg
	s.llm.on = true
	s.llm.mu.Unlock()
	s.invalidateTaskAgents()

	// wake the active task so a task created while idle starts exploring.
	if t := s.m.ActiveTask(); t != nil {
		t.Notify()
	}
	return nil
}

// loadProfileConfig builds an agent.Config from a specific profile id (with its key).
// ok=false when the profile is missing or has no api key.
func (s *Server) loadProfileConfig(id int64) (agent.Config, bool) {
	p, err := s.m.pg.ProfileByID(id)
	if err != nil || p == nil {
		return agent.Config{}, false
	}
	cfg := s.profileToAgentConfig(p)
	if cfg.APIKey == "" {
		return cfg, false
	}
	return cfg, true
}

// effectiveProfileForAgent resolves the LLM profile id an agent should run on, by
// precedence: agent binding (agents.llm_profile_id) → pin (task/conversation) → nil
// (caller falls back to the global active profile). A binding to a deleted profile
// can't happen (FK ON DELETE SET NULL); an otherwise-invalid one is dropped downstream
// by loadProfileConfig, letting the caller fall back.
func (s *Server) effectiveProfileForAgent(agentKey string, pinID *int64) *int64 {
	if s.m.pg != nil && agentKey != "" {
		// 走缓存：这条路径在 worker 循环里每 0.8s 被调用一次（见 agent_cache.go）。
		if a, _ := s.agentRow(agentKey); a != nil && a.LLMProfileID != nil {
			return a.LLMProfileID
		}
	}
	return pinID
}

// resolveChatAgent picks the ChatAgent for one conversation: the agent's own
// binding or this conversation's chosen profile first, the global active config
// only as a fallback. Both the send precheck and the background runner MUST use
// this — resolving differently in the two paths is how a conversation that had
// picked a valid profile still got rejected with "LLM 未配置" when no global
// config was active.
func (s *Server) resolveChatAgent(c *db.Conversation) *agent.ChatAgent {
	ca := s.chatAgentRef()
	if eff := s.effectiveProfileForAgent(c.AgentKey, c.LLMProfileID); eff != nil {
		if pa := s.chatAgentForProfile(*eff); pa != nil {
			ca = pa
		}
	}
	return ca
}

// chatUnavailableReason explains why no ChatAgent could be resolved, so the user
// knows whether to add a config, activate one, or pick one for this conversation
// — rather than a flat "not configured" that hides which of those it is.
func (s *Server) chatUnavailableReason() string {
	if s.m.pg != nil {
		if profiles, err := s.m.pg.ListProfiles(); err == nil && len(profiles) == 0 {
			return "尚未配置 LLM：请到 系统 → LLM 配置 添加一个配置"
		}
		if active, err := s.m.pg.ActiveProfile(); err == nil && active == nil {
			return "没有已激活的 LLM 配置：请到 系统 → LLM 配置 激活一个，或在本对话为该会话指定一个配置"
		}
	}
	return "LLM 未就绪，无法对话：请检查 系统 → LLM 配置是否有可用且已激活的配置"
}

// providerForProfile returns a cached provider+cfg for a profile id, so every agent
// bound/pinned to the same profile shares one provider instance (one rate limiter).
// ok=false when the profile is missing/invalid → caller falls back to the global pair.
func (s *Server) providerForProfile(id int64) (llm.Provider, agent.Config, bool) {
	s.providers.cacheMu.Lock()
	if e := s.providers.byProfile[id]; e != nil {
		s.providers.cacheMu.Unlock()
		return e.prov, e.cfg, true
	}
	generation := s.providers.cacheGen
	s.providers.cacheMu.Unlock()
	cfg, ok := s.loadProfileConfig(id)
	if !ok {
		return nil, agent.Config{}, false
	}
	prov, err := cfg.NewProviderWithLimiter(s.requestLimiterForProfile(id))
	if err != nil {
		log.Printf("[engine] build provider for LLM profile %d failed: %v", id, err)
		return nil, agent.Config{}, false
	}
	// Wrap with recorder, tagged with this profile's name.
	if p, _ := s.m.pg.ProfileByID(id); p != nil {
		prov = llmrec.Wrap(prov, s.m.PG(), cfg.Model, p.Name, cfg.ThinkingType, cfg.ReasoningEffort, s.m.LLMRecordEnabled, BuildVersion)
		prov = bindSideProvider(prov, cfg, id, p.Name)
	}
	s.providers.cacheMu.Lock()
	if generation != s.providers.cacheGen {
		s.providers.cacheMu.Unlock()
		return s.providerForProfile(id)
	}
	if e := s.providers.byProfile[id]; e != nil { // lost the race → keep the winner
		prov, cfg = e.prov, e.cfg
	} else {
		s.providers.byProfile[id] = &provEntry{prov: prov, cfg: cfg}
		log.Printf("[engine] built provider for LLM profile %d (%s / %s)", id, cfg.Provider(), cfg.Model)
	}
	s.providers.cacheMu.Unlock()
	return prov, cfg, true
}

// chatAgentForProfile returns a ChatAgent built from a specific LLM profile, cached
// per profile id. Returns nil if the profile is missing or has no API key.
func (s *Server) chatAgentForProfile(id int64) *agent.ChatAgent {
	s.providers.mu.Lock()
	cached := s.providers.chatAgents[id]
	s.providers.mu.Unlock()
	if cached != nil {
		return cached
	}
	prov, cfg, ok := s.providerForProfile(id)
	if !ok {
		return nil
	}
	ca := s.newConfiguredChatAgent(s.poolForBinding(id, prov, cfg), cfg)
	s.providers.mu.Lock()
	if ex := s.providers.chatAgents[id]; ex != nil { // lost the race → keep the winner
		ca = ex
	} else {
		s.providers.chatAgents[id] = ca
	}
	s.providers.mu.Unlock()
	return ca
}

// requestLimiterForProfile returns the stable limiter for one LLM profile (0 =
// no-profile/global config). Providers are rebuilt often (profile save, activation,
// agent rebinding); the limiter must survive that, otherwise the quota restarts
// from zero on every edit and rate limiting is decorative.
func (s *Server) requestLimiterForProfile(id int64) *agent.RequestLimiter {
	s.providers.cacheMu.Lock()
	defer s.providers.cacheMu.Unlock()
	if l := s.providers.limiters[id]; l != nil {
		return l
	}
	l := &agent.RequestLimiter{}
	if s.providers.limiters == nil { // 测试里手工构造的 Server 可能没初始化这张表
		s.providers.limiters = map[int64]*agent.RequestLimiter{}
	}
	s.providers.limiters[id] = l
	return l
}

// invalidateProfileAgents drops the per-profile ChatAgent + provider caches so a profile
// save/activate/delete — or an agent's binding change — rebuilds pinned tasks' agents
// (and re-resolves each agent's bound model) on their next round.
func (s *Server) invalidateProfileAgents() {
	s.providers.mu.Lock()
	s.providers.chatAgents = map[int64]*agent.ChatAgent{}
	s.providers.mu.Unlock()
	s.providers.cacheMu.Lock()
	s.providers.cacheGen++
	s.providers.byProfile = map[int64]*provEntry{}
	s.providers.cacheMu.Unlock()
	s.invalidateTaskAgents()
}

// invalidateProfileChatAgents clears per-profile ChatAgent wrappers without
// discarding cached LLM providers / request rate limiters.
func (s *Server) invalidateProfileChatAgents() {
	s.providers.mu.Lock()
	s.providers.chatAgents = map[int64]*agent.ChatAgent{}
	s.providers.mu.Unlock()
}

func (s *Server) chatAgentRef() *agent.ChatAgent {
	s.llm.mu.Lock()
	defer s.llm.mu.Unlock()
	return s.llm.chatAgent
}

// globalLLMConfig returns both the global provider and its effective agent.Config.
func (s *Server) globalLLMConfig() (llm.Provider, agent.Config, bool) {
	s.llm.mu.Lock()
	if s.llm.direct != nil {
		p, cfg := s.llm.direct, s.llm.cfg
		s.llm.mu.Unlock()
		return p, cfg, true
	}
	s.llm.mu.Unlock()
	// 启动早期 direct 可能尚未赋值；退回当前激活 profile 的缓存 provider。
	active, err := s.m.pg.ActiveProfile()
	if err != nil || active == nil {
		return nil, agent.Config{}, false
	}
	return s.providerForProfile(active.ID)
}

// getLLM returns the current LLM config (key never exposed).
func (s *Server) getLLM(w http.ResponseWriter, r *http.Request) {
	s.llm.mu.Lock()
	defer s.llm.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"configured":       s.llm.on,
		"provider":         s.llm.cfg.Provider(),
		"model":            s.llm.cfg.Model,
		"base_url":         s.llm.cfg.BaseURL,
		"proxy":            s.llm.cfg.Proxy,
		"key_set":          s.llm.cfg.APIKey != "",
		"rate_per_second":  s.llm.cfg.RatePerSecond,
		"rate_per_minute":  s.llm.cfg.RatePerMinute,
		"context_window_k": s.llm.cfg.ContextWindowK,
		"thinking_type":    s.llm.cfg.ThinkingType,
		"reasoning_effort": s.llm.cfg.ReasoningEffort,
	})
}

// setLLM configures the LLM at runtime. A blank api_key keeps the existing key.
func (s *Server) setLLM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider        string  `json:"provider"`
		Model           string  `json:"model"`
		BaseURL         string  `json:"base_url"`
		Proxy           string  `json:"proxy"`
		APIKey          string  `json:"api_key"`
		RatePerSecond   float64 `json:"rate_per_second"`
		RatePerMinute   float64 `json:"rate_per_minute"`
		ContextWindowK  int     `json:"context_window_k"`
		ThinkingType    string  `json:"thinking_type"`
		ReasoningEffort string  `json:"reasoning_effort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cfg := agent.ConfigFrom(req.Provider, req.Model, req.BaseURL, req.APIKey, req.Proxy)
	cfg.RatePerSecond, cfg.RatePerMinute = req.RatePerSecond, req.RatePerMinute
	cfg.ThinkingType = req.ThinkingType
	cfg.ReasoningEffort = req.ReasoningEffort
	if k := req.ContextWindowK; k > 0 { // 0 = keep default (200K); cap at 1M
		if k > 1000 {
			k = 1000
		}
		cfg.ContextWindowK = k
	}
	if cfg.APIKey == "" {
		s.llm.mu.Lock()
		cfg.APIKey = s.llm.cfg.APIKey // keep existing key if not re-entered
		s.llm.mu.Unlock()
	}
	if cfg.APIKey == "" {
		writeErr(w, 400, "api_key required")
		return
	}
	// Validate provider construction before persisting it as the active profile.
	if _, err := cfg.NewProvider(); err != nil {
		writeErr(w, 400, "provider init failed: "+err.Error())
		return
	}
	if err := s.saveLLMConfig(cfg); err != nil {
		writeErr(w, 500, "persist provider failed: "+err.Error())
		return
	}
	s.invalidateProfileAgents()
	if _, ok := s.loadLLMConfig(); !ok {
		writeErr(w, 500, "saved provider is unavailable")
		return
	}
	if err := s.applyLLM(cfg); err != nil {
		writeErr(w, 400, "provider init failed: "+err.Error())
		return
	}
	log.Printf("[engine] LLM configured via UI: %s / %s", cfg.Provider(), cfg.Model)
	s.getLLM(w, r)
}

// testLLM makes a real minimal completion to verify the config works.
func (s *Server) testLLM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider               string            `json:"provider"`
		Model                  string            `json:"model"`
		BaseURL                string            `json:"base_url"`
		Proxy                  *string           `json:"proxy"`
		APIKey                 string            `json:"api_key"`
		ThinkingType           string            `json:"thinking_type"`
		ThinkingBudgetTokens   *int              `json:"thinking_budget_tokens"`
		ReasoningEffort        string            `json:"reasoning_effort"`
		ProfileID              *int64            `json:"profile_id"` // 测已存 profile 时传入：api_key 为空则用它存的 key
		Streaming              *bool             `json:"streaming"`  // 省略=流式，与保存 profile 时同一套默认
		MaxTokens              *int              `json:"max_tokens"`
		MaxTokensField         *string           `json:"max_tokens_field"`
		SessionHeaderKey       string            `json:"session_header_key"` // 非空=测试时也带该自定义会话头，值为一次性随机 session id
		CustomHeaders          map[string]string `json:"custom_headers"`     // 固定请求头（User-Agent 等），与保存后实际运行一致
		CacheBreakpoints       *string           `json:"cache_breakpoints"`
		CacheTTL               *string           `json:"cache_ttl"`
		PromptCacheRetention   *string           `json:"prompt_cache_retention"`
		PreserveThinkingBlocks *bool             `json:"preserve_thinking_blocks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var saved *db.LLMProfile
	if req.ProfileID != nil {
		if p, err := s.m.pg.ProfileByID(*req.ProfileID); err == nil && p != nil {
			saved = p
		}
	}
	var proxyStr string
	if req.Proxy != nil {
		proxyStr = *req.Proxy
	} else if saved != nil {
		proxyStr = saved.Proxy
	}
	cfg := agent.ConfigFrom(req.Provider, req.Model, req.BaseURL, req.APIKey, proxyStr)
	// Build a temporary profile representation so validation/normalization
	// mirrors pgSaveProfile identically.
	tmp := db.LLMProfile{
		Format:                 cfg.Provider(),
		Model:                  cfg.Model,
		BaseURL:                cfg.BaseURL,
		Proxy:                  cfg.Proxy,
		ThinkingType:           req.ThinkingType,
		ReasoningEffort:        req.ReasoningEffort,
		Streaming:              req.Streaming == nil || *req.Streaming,
		SessionHeaderKey:       req.SessionHeaderKey,
		CustomHeaders:          req.CustomHeaders,
		MaxTokensField:         "",
		CacheBreakpoints:       "",
		CacheTTL:               "",
		PromptCacheRetention:   "",
		PreserveThinkingBlocks: false,
	}
	if req.ThinkingBudgetTokens != nil {
		tmp.ThinkingBudgetTokens = *req.ThinkingBudgetTokens
	} else if saved != nil {
		tmp.ThinkingBudgetTokens = saved.ThinkingBudgetTokens
	}
	if req.MaxTokens != nil {
		tmp.MaxTokens = *req.MaxTokens
	} else if saved != nil {
		tmp.MaxTokens = saved.MaxTokens
	}
	if req.MaxTokensField != nil {
		tmp.MaxTokensField = *req.MaxTokensField
	} else if saved != nil {
		tmp.MaxTokensField = saved.MaxTokensField
	}
	if req.CacheBreakpoints != nil {
		tmp.CacheBreakpoints = *req.CacheBreakpoints
	} else if saved != nil {
		tmp.CacheBreakpoints = saved.CacheBreakpoints
	}
	if req.CacheTTL != nil {
		tmp.CacheTTL = *req.CacheTTL
	} else if saved != nil {
		tmp.CacheTTL = saved.CacheTTL
	}
	if req.PromptCacheRetention != nil {
		tmp.PromptCacheRetention = *req.PromptCacheRetention
	} else if saved != nil {
		tmp.PromptCacheRetention = saved.PromptCacheRetention
	}
	if req.PreserveThinkingBlocks != nil {
		tmp.PreserveThinkingBlocks = *req.PreserveThinkingBlocks
	} else if saved != nil {
		tmp.PreserveThinkingBlocks = saved.PreserveThinkingBlocks
	}
	if saved != nil && req.Streaming == nil {
		tmp.Streaming = saved.Streaming
	}
	if saved != nil {
		if cfg.APIKey == "" {
			cfg.APIKey = saved.APIKey
		}
		if strings.TrimSpace(tmp.SessionHeaderKey) == "" {
			tmp.SessionHeaderKey = saved.SessionHeaderKey
		}
		if tmp.CustomHeaders == nil {
			tmp.CustomHeaders = saved.CustomHeaders
		}
	}
	tmp.Name = "connection-test" // satisfy shared profile validator
	if _, err := validateAndNormalizeProfile(&tmp); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	cfg.ThinkingType = tmp.ThinkingType
	cfg.ThinkingBudgetTokens = tmp.ThinkingBudgetTokens
	cfg.ReasoningEffort = tmp.ReasoningEffort
	cfg.Stream = tmp.Streaming
	cfg.MaxTokens = tmp.MaxTokens
	cfg.MaxTokensField = tmp.MaxTokensField
	cfg.SessionHeaderKey = db.DefaultSessionHeaderKey(cfg.BaseURL, tmp.SessionHeaderKey)
	cfg.PreserveThinkingBlocks = tmp.PreserveThinkingBlocks
	if cfg.Format == llm.FormatAnthropic {
		cfg.CacheBreakpoints = agent.ResolveAnthropicCacheBreakpoints(tmp.CacheBreakpoints)
		cfg.CacheTTL = tmp.CacheTTL
	} else {
		cfg.PromptCacheRetention = tmp.PromptCacheRetention
	}
	headers, err := db.NormalizeCustomHeaders(tmp.CustomHeaders)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	cfg.CustomHeaders = headers
	if cfg.APIKey == "" {
		s.llm.mu.Lock()
		cfg.APIKey = s.llm.cfg.APIKey
		s.llm.mu.Unlock()
	}
	if cfg.APIKey == "" {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "未提供 API Key"})
		return
	}
	// 重试参数【不】带进连接测试:测试有 30s 硬超时,把配置的重试次数/长间隔叠上去
	// 只会让一个本来能用的端点测成"超时失败"。测试看的是"这个端点通不通",重试节奏
	// 是跑起来之后的事。
	lat, reply, err := agent.TestConnection(r.Context(), cfg)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// 回传模型实际回复，让"测试通过"有据可查：看得见它确实说了话，而不只是 HTTP 200。
	writeJSON(w, 200, map[string]any{
		"ok": true, "latency_ms": lat.Milliseconds(), "model": cfg.Model, "reply": truncateReply(reply),
	})
}

// truncateReply clips a connection-test reply for display. A model told to answer
// "OK" can still ramble (or think out loud); the UI only needs enough to show it
// really said something. Rune-based so multibyte text never splits mid-character.
func truncateReply(s string) string {
	const max = 200
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

type createTaskReq struct {
	Name                 string   `json:"name,omitempty"` // 可选任务名称;省略/空=未命名
	CategoryID           *int64   `json:"category_id,omitempty"`
	Description          string   `json:"description"`
	Goal                 string   `json:"goal"`
	LLMProfileID         *int64   `json:"llm_profile_id,omitempty"`    // 指定运行本任务的 LLM 配置;省略/null=用激活配置
	LLMProfileIDs        []int64  `json:"llm_profile_ids,omitempty"`   // 有序任务级配置链;第一项初始生效
	SourceTaskIDs        []string `json:"source_task_ids,omitempty"`   // 仅直接、只读继承的来源任务
	CompanyIDs           []int64  `json:"company_ids,omitempty"`       // 关联企业范围并快照关联当前企业资产;不复制资产或强制生成意图
	TimeoutSeconds       int      `json:"timeout_seconds"`             // 任务级超时(秒);0/省略=不限时
	PlanHeartbeatSeconds int      `json:"plan_heartbeat_seconds"`      // planner 心跳触发间隔(秒);0/省略=默认600(10min);下限=默认=600,低于自动抬到600
	SeedFirstIntent      *bool    `json:"seed_first_intent,omitempty"` // 创建时直接下发一条种子意图(内容=描述+目标),让 worker 免等首轮 planner 直接开跑;省略/null=默认关闭,走标准先规划再执行。显式传 true 才开(CTF 常一 work 解决时可省掉开跑前的 planner 轮)。
	CoverageEnabled      *bool    `json:"coverage_enabled,omitempty"`
	AssetScopePolicy     string   `json:"asset_scope_policy,omitempty"` // exact | target；空=新任务默认 target  // 资产覆盖度功能;省略/null=默认开(true)。false=关闭覆盖度计算/展示/自动累积范围+隐藏 add_task_scope/list_untested_assets。company 关联不受影响。
	LootExecEnabled      bool     `json:"loot_exec_enabled,omitempty"`
	// CredentialAttemptBudget 任务级凭据尝试预算/30分钟；nil=继承全局，0=不限。
	CredentialAttemptBudget *int `json:"credential_attempt_budget,omitempty"`
	// InterceptRules 任务级资产拦截/允许规则(创建时录入,存 task_intercept_rules,不进全局表)。
	InterceptRules []taskInterceptRuleReq `json:"intercept_rules,omitempty"`
	// WorkerCount 是每任务并发覆盖（0 = 跟随全局 workers，上限 50）。
	// 只在任务首次启动时固化，之后改它不影响已在跑的槽位。
	WorkerCount int `json:"worker_count,omitempty"`
	// Remark 是任务备注（≤2000 字），给操作者记上下文用。
	Remark string `json:"remark,omitempty"`
}
