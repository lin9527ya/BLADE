// LLM 配置：读写、探测、按 agent/配置解析。
// 从 server.go 拆出：纯搬运，零行为变化。
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"reflect"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/llmrec"
	"github.com/lin9527ya/blade/policy"
)

// agentMaxTurns returns the configured max_turns for an agent key (0 = unlimited,
// also the fallback when no DB or no row).
func (s *Server) agentMaxTurns(key string) int {
	if s.m.pg == nil {
		return 0
	}
	a, err := s.m.pg.GetAgentByKey(key)
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
	a, err := s.m.pg.GetAgentByKey(key)
	if err != nil || a == nil {
		return 1200
	}
	return a.RunSecs
}

// loadLLMConfig reads the active LLM profile from PG (llm_profiles).
func (s *Server) loadLLMConfig() (agent.Config, bool) {
	p, err := s.m.pg.ActiveProfile()
	if err != nil || p == nil {
		return agent.Config{}, false
	}
	cfg := agent.ConfigFrom(p.Format, p.Model, p.BaseURL, p.APIKey, p.Proxy)
	cfg.RatePerSecond, cfg.RatePerMinute = p.RatePerSecond, p.RatePerMinute
	cfg.ContextWindowK = p.ContextWindowK
	cfg.ThinkingType = p.ThinkingType
	cfg.ReasoningEffort = p.ReasoningEffort
	cfg.Stream = p.Streaming
	cfg.MaxTokens, cfg.MaxTokensField = p.MaxTokens, p.MaxTokensField
	cfg.SessionHeaderKey = p.SessionHeaderKey
	cfg.CustomHeaders = p.CustomHeaders
	s.applyProfileRetry(&cfg, p)
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
	var retry db.RetryOverride
	if profs, _ := s.m.pg.ListProfiles(); profs != nil {
		for _, p := range profs {
			if p.Name == "default" {
				id, priority, poolExclude, streaming = p.ID, p.Priority, p.PoolExclude, p.Streaming
				maxTokens, maxTokensField = p.MaxTokens, p.MaxTokensField
				sessionHeaderKey = p.SessionHeaderKey
				customHeaders = p.CustomHeaders
				retry = p.Retry
				break
			}
		}
	}
	newID, err := s.m.pg.SaveProfile(&db.LLMProfile{
		ID: id, Name: "default", Format: format, Model: cfg.Model, BaseURL: cfg.BaseURL, Proxy: cfg.Proxy,
		APIKey: cfg.APIKey, RatePerSecond: cfg.RatePerSecond, RatePerMinute: cfg.RatePerMinute,
		ContextWindowK: cfg.ContextWindowK, ThinkingType: cfg.ThinkingType, ReasoningEffort: cfg.ReasoningEffort, IsDefault: true,
		Priority: priority, PoolExclude: poolExclude, Streaming: streaming,
		MaxTokens: maxTokens, MaxTokensField: maxTokensField, SessionHeaderKey: sessionHeaderKey,
		CustomHeaders: customHeaders, Retry: retry,
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
	if a, err := s.m.pg.GetAgentByKey(key); err != nil || a == nil || !a.WebSearch {
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
		prov, err = cfg.NewProvider()
		if err != nil {
			return err
		}
		// Non-persisted/env configs do not have a profile cache key.
		s.llm.mu.Lock()
		profName := s.llm.profile
		s.llm.mu.Unlock()
		prov = llmrec.Wrap(prov, s.m.PG(), cfg.Model, profName, cfg.ThinkingType, cfg.ReasoningEffort, s.m.LLMRecordEnabled)
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

	tx := transcript.NewStore(filepath.Join(s.m.dir, "transcripts"))
	win := cfg.CompactionWindow()
	// chat stays the GLOBAL fallback since one ChatAgent serves many agent keys — its
	// per-agent binding is resolved at Chat time (runConversationSync →
	// chatAgentForProfile). The main-agent has no global instance: each task builds its
	// own via agentsForTask (task-routed), so nothing is constructed for it here.
	s.llm.mu.Lock()
	// chat agent serves MANY custom agents by key → it holds the GLOBAL opts
	// (backend/key) and gates Enabled per-conversation-agent at Chat time. 对话始终用激活配置。
	s.llm.chatAgent = agent.NewChatAgent(prov, cfg.Model, s.m.dir, tx, win) // chat page runner
	s.llm.chatAgent.SetProxyFunc(func() (string, string) { return s.m.ProxyAddr(), s.m.ProxyCACert() })
	s.llm.chatAgent.SetWebSearch(s.m.WebSearchOpts())
	s.llm.chatAgent.SetGuard(s.chatGuard())
	s.llm.chatAgent.SetNonStreaming(nonStreamingResolver(cfg))
	s.llm.chatAgent.SetMaxTokens(maxTokensResolver(cfg))
	s.llm.chatAgent.SetNoaEnabled(s.m.NoaCompactionEnabled) // 实验功能:noa 上下文压缩(每 run 读)
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
	cfg := agent.ConfigFrom(p.Format, p.Model, p.BaseURL, p.APIKey, p.Proxy)
	cfg.RatePerSecond, cfg.RatePerMinute = p.RatePerSecond, p.RatePerMinute
	cfg.ContextWindowK = p.ContextWindowK
	cfg.ThinkingType = p.ThinkingType
	cfg.ReasoningEffort = p.ReasoningEffort
	cfg.Stream = p.Streaming
	cfg.MaxTokens, cfg.MaxTokensField = p.MaxTokens, p.MaxTokensField
	cfg.SessionHeaderKey = p.SessionHeaderKey
	cfg.CustomHeaders = p.CustomHeaders
	s.applyProfileRetry(&cfg, p)
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
		if a, _ := s.m.pg.GetAgentByKey(agentKey); a != nil && a.LLMProfileID != nil {
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
	prov, err := cfg.NewProvider()
	if err != nil {
		log.Printf("[engine] build provider for LLM profile %d failed: %v", id, err)
		return nil, agent.Config{}, false
	}
	// Wrap with recorder, tagged with this profile's name.
	if p, _ := s.m.pg.ProfileByID(id); p != nil {
		prov = llmrec.Wrap(prov, s.m.PG(), cfg.Model, p.Name, cfg.ThinkingType, cfg.ReasoningEffort, s.m.LLMRecordEnabled)
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
	tx := transcript.NewStore(filepath.Join(s.m.dir, "transcripts"))
	ca := agent.NewChatAgent(s.poolForBinding(id, prov, cfg), cfg.Model, s.m.dir, tx, cfg.CompactionWindow())
	ca.SetProxyFunc(func() (string, string) { return s.m.ProxyAddr(), s.m.ProxyCACert() })
	ca.SetWebSearch(s.m.WebSearchOpts())
	ca.SetGuard(s.chatGuard())
	ca.SetNonStreaming(nonStreamingResolver(cfg))
	ca.SetMaxTokens(maxTokensResolver(cfg))
	ca.SetNoaEnabled(s.m.NoaCompactionEnabled) // 实验功能:noa 上下文压缩(每 run 读)
	s.providers.mu.Lock()
	if ex := s.providers.chatAgents[id]; ex != nil { // lost the race → keep the winner
		ca = ex
	} else {
		s.providers.chatAgents[id] = ca
	}
	s.providers.mu.Unlock()
	return ca
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

func (s *Server) chatAgentRef() *agent.ChatAgent {
	s.llm.mu.Lock()
	defer s.llm.mu.Unlock()
	return s.llm.chatAgent
}

// globalLLMProvider returns the configured global provider for one-shot utility
// calls (e.g. CVE report extraction). It never builds a new provider: if no LLM
// is configured it returns false so callers can report "先配置 LLM".
func (s *Server) globalLLMProvider() (llm.Provider, bool) {
	s.llm.mu.Lock()
	if s.llm.direct != nil {
		p := s.llm.direct
		s.llm.mu.Unlock()
		return p, true
	}
	s.llm.mu.Unlock()
	// 启动早期 direct 可能尚未赋值；退回当前激活 profile 的缓存 provider。
	active, err := s.m.pg.ActiveProfile()
	if err != nil || active == nil {
		return nil, false
	}
	prov, _, ok := s.providerForProfile(active.ID)
	return prov, ok
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
		Provider         string            `json:"provider"`
		Model            string            `json:"model"`
		BaseURL          string            `json:"base_url"`
		Proxy            string            `json:"proxy"`
		APIKey           string            `json:"api_key"`
		ThinkingType     string            `json:"thinking_type"`
		ReasoningEffort  string            `json:"reasoning_effort"`
		ProfileID        *int64            `json:"profile_id"`         // 测已存 profile 时传入：api_key 为空则用它存的 key
		Streaming        *bool             `json:"streaming"`          // 省略=流式，与保存 profile 时同一套默认
		SessionHeaderKey string            `json:"session_header_key"` // 非空=测试时也带该自定义会话头，值为一次性随机 session id
		CustomHeaders    map[string]string `json:"custom_headers"`     // 固定请求头（User-Agent 等），与保存后实际运行一致
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cfg := agent.ConfigFrom(req.Provider, req.Model, req.BaseURL, req.APIKey, req.Proxy)
	// mirror production: send the SAME thinking params so a provider that rejects the
	// reasoning_effort/thinking field fails the test too (no false "test ok, run 400").
	cfg.ThinkingType = req.ThinkingType
	cfg.ReasoningEffort = req.ReasoningEffort
	// 同理，收发模式也照该 profile 的选择来：只支持其中一种通道的端点必须在这里就
	// 暴露，而不是等会话里才发现"测试通过的配置根本跑不动"。
	if req.Streaming != nil {
		cfg.Stream = *req.Streaming
	}
	// 自定义会话头名照该配置来：非空则测试请求也发这个头(值为一次性随机 session id，
	// 见 TestConnection)。opencode zen 等强制要求 x-opencode-session 的端点，缺了它
	// 直接 400，必须在测试路径上也带上，否则"对话通、测试 400"。表单没填时按端点补
	// 默认头名(opencode.ai ⇒ x-opencode-session)，保证"没填也能测通"。
	cfg.SessionHeaderKey = db.DefaultSessionHeaderKey(req.BaseURL, req.SessionHeaderKey)
	headers, err := db.NormalizeCustomHeaders(req.CustomHeaders)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	cfg.CustomHeaders = headers
	// API Key 解析优先级：表单输入 > 指定 profile 存的 key > 全局配置的 key。
	// 已存 profile 的 key 不回传浏览器，所以测试已存配置时表单为空，需从 DB 取。
	// 会话头名同理：表单未带时用已存 profile 的值兜底。
	if req.ProfileID != nil && (cfg.APIKey == "" || cfg.SessionHeaderKey == "" || req.CustomHeaders == nil) {
		if p, err := s.m.pg.ProfileByID(*req.ProfileID); err == nil && p != nil {
			if cfg.APIKey == "" {
				cfg.APIKey = p.APIKey
			}
			if cfg.SessionHeaderKey == "" {
				cfg.SessionHeaderKey = p.SessionHeaderKey
			}
			if req.CustomHeaders == nil {
				cfg.CustomHeaders = p.CustomHeaders
			}
		}
	}
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
	CoverageEnabled      *bool    `json:"coverage_enabled,omitempty"`  // 资产覆盖度功能;省略/null=默认开(true)。false=关闭覆盖度计算/展示/自动累积范围+隐藏 add_task_scope/list_untested_assets。company 关联不受影响。
	// InterceptRules 任务级资产拦截/允许规则(创建时录入,存 task_intercept_rules,不进全局表)。
	InterceptRules []taskInterceptRuleReq `json:"intercept_rules,omitempty"`
	// ExplorationPolicy(P20) 任务级探索策略覆盖层;省略/null=完全继承当时的全局默认。
	ExplorationPolicy *policy.Override `json:"exploration_policy,omitempty"`
}
