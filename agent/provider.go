// Package agent wires real LLM-driven planner and work agents (on top of the
// agent-core SDK) to the dual SQLite graph.
// §4.3 (planner) and §4.4 (work agent).
//
// Provider configuration is read from the environment so the system runs with
// any Anthropic- or OpenAI-format endpoint. If no key is configured, FromEnv
// returns ok=false and the exploration engine stays idle (an LLM is required).
package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/compaction"
	"github.com/lin9527ya/Norma/llm"
	acperm "github.com/lin9527ya/Norma/permission"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/llmpool"
	"github.com/lin9527ya/blade/llmrec"
)

// Config describes the LLM backend resolved from the environment.
type Config struct {
	Format  llm.Format
	BaseURL string
	APIKey  string
	Model   string
	// Proxy routes all LLM requests through the given proxy URL (http/https/socks5,
	// optionally with user:pass@ credentials). Empty means direct — it does NOT
	// fall back to the standard *_PROXY environment variables.
	Proxy string
	// RatePerSecond / RatePerMinute cap the shared request rate across ALL agents
	// using the provider (0 = that window unlimited).
	RatePerSecond float64
	RatePerMinute float64
	// ContextWindowK is the model's context window in K tokens (user-configured),
	// used to size compaction thresholds. 0 = default; see CompactionWindow.
	ContextWindowK int
	// ThinkingType 独立控制思考「开关」字段(thinking.type):
	//   "" = 不发送(默认,兼容不支持该字段的模型); "disabled" = 显式关闭;
	//   "enabled" = 开启; "adaptive" = 自适应(anthropic Claude 4.6+).
	ThinkingType string
	// ThinkingBudgetTokens 可选设置 Anthropic enabled 模式下的思考预算
	// ("thinking.budget_tokens")。0 = 不发送; 仅对 format=anthropic 且
	// ThinkingType="enabled" 生效。
	ThinkingBudgetTokens int
	// ReasoningEffort 独立控制思考「强度」字段:
	//   "" = 不发送(默认); "none"/"minimal"/"low"/"medium"/"high"/"xhigh"/"max" = 对应强度.
	//   OpenAI 映射为顶层 reasoning_effort; Anthropic 为 output_config.effort; Responses 为 reasoning.effort.
	ReasoningEffort string
	// Stream 控制该 profile 是否使用流式(SSE)接口。true(默认)= 流式;false = 真·
	// 非流式(发 stream:false,一次性拿完整 JSON,走 Provider.Complete)。非流式可绕开
	// 某些网关糟糕的 SSE 实现(空帧、思考字段丢帧),代价是失去运行中的实时进度/实时
	// token 计数。映射为 agentcore.Options.NonStreaming = !Stream。
	Stream bool
	// MaxTokens 是单次回复的输出上限(token)。0 = 不发送该字段,由服务端默认值决定
	// (历史行为)。与 ContextWindowK 不同:后者是模型总容量,只在本地用来算压缩阈值,
	// 不出现在请求里;本值随每次请求发出。映射为 agentcore.Options.MaxTokens。
	MaxTokens int
	// MaxTokensField 选择 MaxTokens 用哪个请求字段名,仅对 format=openai 生效:
	//   "" = max_tokens(默认); "max_completion_tokens" = 新字段。
	// OpenAI 推理模型(o 系列/GPT-5)只认后者,收到 max_tokens 会直接报
	// unsupported_parameter;而多数兼容网关只认前者,故不做自动推断,交由用户按端点选。
	MaxTokensField string
	// SessionHeaderKey,非空时,让每次 LLM 请求带上一个自定义 HTTP 头,头名为该值、
	// 头值为【当前会话的 session id】(chat 会话=conv-<id>,worker=exp<x>-worker-i<intent>
	// 等,见 WorkerSessionID)。用于某些按 session-id 头做提示缓存/粘性路由的网关。
	// 空 = 不发送。值由 transcript.WithSessionID 挂在请求 context 上,由 RoundTripper
	// 读取填入,因此同一共享 provider 也能按会话发出不同的头值。
	SessionHeaderKey string
	// CustomHeaders are fixed headers attached to every request (e.g. User-Agent).
	// Unlike SessionHeaderKey their values do not depend on the conversation. The
	// session header wins if both set the same name.
	CustomHeaders map[string]string
	// InputTokensIncludesCache tells the Anthropic adapter that raw
	// usage.input_tokens from this endpoint already includes cached tokens, so it
	// must not add cache_read + cache_creation again.
	InputTokensIncludesCache bool
	// PreserveThinkingBlocks keeps historical thinking blocks on Anthropic
	// requests when ThinkingType is unset (for gateways that enable thinking on by default).
	PreserveThinkingBlocks bool
	// CacheBreakpoints controls extra Anthropic cache_control breakpoints
	// ("" = system prefix only; "rolling" = last tool + rolling history messages).
	CacheBreakpoints string
	// CacheTTL sets optional Anthropic prompt cache TTL (""/"5m" or "1h") on the
	// static tools + system prefix; dynamic rolling breakpoints remain default 5m.
	CacheTTL string
	// PromptCacheRetention sets OpenAI prompt_cache_retention (""/"in_memory"/"24h")
	// on endpoints where supportsPromptCacheKey allows sending prompt cache body knobs.
	PromptCacheRetention string
	// Retry 是该配置解析后的重试参数(profile 覆盖 → 全局策略 → 内置默认,由
	// server 侧解析)。三层的含义见 RetryConfig;零值 = 完全沿用内置默认。
	Retry RetryConfig
}

// ResolveAnthropicCacheBreakpoints normalizes persisted/configured breakpoint modes
// for Anthropic profiles: unset/blank defaults to "rolling", while explicit "system"
// opts out back to the single system-prefix breakpoint ("").
func ResolveAnthropicCacheBreakpoints(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "system":
		return ""
	case "", "rolling":
		return "rolling"
	default:
		return "rolling"
	}
}

// PreservesThinking reports whether the configured profile expects historical
// thinking blocks / reasoning content to stay attached. Always false when
// ThinkingType is "disabled" or on OpenAI Responses (stateless replay drops them).
func (c Config) PreservesThinking() bool {
	switch strings.ToLower(strings.TrimSpace(c.ThinkingType)) {
	case "disabled":
		return false
	case "enabled", "adaptive":
		return c.Format == "" || c.Format == llm.FormatAnthropic || c.Format == llm.FormatOpenAI
	default:
		if c.Format == llm.FormatOpenAI {
			return strings.TrimSpace(c.ReasoningEffort) != ""
		}
		if c.Format == "" || c.Format == llm.FormatAnthropic {
			return c.PreserveThinkingBlocks
		}
		return false
	}
}

func providerPreservesThinking(p llm.Provider) bool {
	if p == nil {
		return false
	}
	if pt, ok := p.(interface{ PreservesThinking() bool }); ok {
		return pt.PreservesThinking()
	}
	return false
}

// cacheOptionResolvers lazily resolves endpoint-specific prompt-cache options
// (CacheTTL, PromptCacheRetention, PreserveThinking) per run and applies them to
// agentcore.Options so hot profile reloads take effect without rebuilding agents.
type cacheOptionResolvers struct {
	cacheTTLFn         func() string
	retentionFn        func() string
	preserveThinkingFn func() bool
}

func (r cacheOptionResolvers) apply(opts *agentcore.Options) {
	if opts == nil {
		return
	}
	if r.cacheTTLFn != nil {
		opts.CacheTTL = r.cacheTTLFn()
	}
	if r.retentionFn != nil {
		opts.PromptCacheRetention = r.retentionFn()
	}
	if r.preserveThinkingFn != nil {
		opts.PreserveThinking = r.preserveThinkingFn()
	} else if providerPreservesThinking(opts.Provider) {
		opts.PreserveThinking = true
	}
}

// RetryConfig 是随一个 LLM 配置走的重试参数。每层的「次数」统一语义:
// 0 = 用内置默认次数;负数 = 关闭该层重试;>0 = 用该值。每层的「间隔」:
// 0 = 用该层原本的指数退避;>0 = 改用这个固定间隔。
type RetryConfig struct {
	// ConnectAttempts/ConnectInterval:SDK 建连重试(连接重置/超时/429/5xx,流开始前),
	// 直接映射为 llm.Config.MaxRetries / RetryInterval。默认 3 次、0.5s 起指数(封顶 8s)。
	ConnectAttempts int
	ConnectInterval time.Duration
	// EmptyAttempts/EmptyInterval:SDK 空响应重试(完成但无 content block,仅 openai
	// 格式),映射为 llm.Config.EmptyResponseRetries / EmptyResponseInterval。
	// 默认 2 次、同一条指数梯度。
	EmptyAttempts int
	EmptyInterval time.Duration
	// StreamAttempts/StreamInterval:同 provider 安全窗口重试——本项目在 SDK 之上补的
	// 一层,只在「还没向调用方交付任何输出」时重放断流/过载/流内 429。SDK 看不到它,
	// 由 server/task_llm.go 消费。默认 2 次、0.5s 起指数(封顶 4s)。
	StreamAttempts int
	StreamInterval time.Duration
}

// compaction window resolution bounds (in K tokens). Below the floor the
// threshold math (window − summary reserve − buffer) would go non-positive and
// compaction would fire every turn; above the cap it would never fire.
const (
	defaultWindowK = 200  // unset → assume a 200K window (Claude default)
	minWindowK     = 32   // floor so effectiveWindow stays comfortably positive
	maxWindowK     = 1000 // cap at 1M tokens (user request)
)

// CompactionWindow returns the model context window in TOKENS for compaction
// thresholds, resolved from the user-configured size (ContextWindowK). 0/unset →
// a 200K default; otherwise clamped to [32K, 1M] so compaction stays effective.
func (c Config) CompactionWindow() int {
	k := c.ContextWindowK
	if k <= 0 {
		k = defaultWindowK
	}
	if k < minWindowK {
		k = minWindowK
	}
	if k > maxWindowK {
		k = maxWindowK
	}
	return k * 1000
}

// compactionConfig builds the agent-core compaction config for a context window
// in tokens. maxTokens 是 profile 的实际输出上限：SDK 会把它作为「摘要预留」
// （min(maxTokens, 20k)）。此前恒传 0 → 预留 20k；对 64K 窗口意味着 auto 阈值只有
// ~31K、micro ~26K，长会话会被过早压缩，每次压缩都重建前缀并多一次大摘要调用。
// 传真实输出上限（如 8K）可把窗口还给上下文。
func compactionConfig(windowTokens, maxTokens int) *compaction.Config {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	return &compaction.Config{
		ContextWindow:        windowTokens,
		MaxOutputTokens:      maxTokens,
		MicroCompactMinClear: 8,
	}
}

// FromEnv resolves the LLM provider config:
//
//	BLADE_LLM_PROVIDER = anthropic|openai|openai-responses (default: inferred from keys)
//	BLADE_LLM_MODEL    = model id        (default: per provider)
//	BLADE_LLM_BASE_URL = endpoint        (optional)
//	BLADE_LLM_PROXY    = proxy URL       (optional; http/https/socks5)
//	BLADE_LLM_STREAM   = true|false      (default: true)
//	BLADE_LLM_THINKING_TYPE          = disabled|enabled|adaptive (optional)
//	BLADE_LLM_THINKING_BUDGET_TOKENS = int >= 0 (anthropic enabled only)
//	BLADE_LLM_REASONING_EFFORT       = none|minimal|low|medium|high|xhigh|max
//	BLADE_LLM_MAX_TOKENS             = int >= 0 (optional)
//	BLADE_LLM_MAX_TOKENS_FIELD       = max_completion_tokens (openai only)
//	BLADE_LLM_CONTEXT_WINDOW_K       = 0..1000 (optional)
//	BLADE_LLM_RATE_PER_SECOND / BLADE_LLM_RATE_PER_MINUTE = float >= 0
//	BLADE_LLM_SESSION_HEADER_KEY     = custom HTTP session header name
//	BLADE_LLM_CACHE_BREAKPOINTS / BLADE_LLM_CACHE_TTL / BLADE_LLM_PROMPT_CACHE_RETENTION
//	BLADE_LLM_INPUT_TOKENS_INCLUDES_CACHE / BLADE_LLM_PRESERVE_THINKING_BLOCKS = bool
//	ANTHROPIC_API_KEY / OPENAI_API_KEY = credentials
func FromEnv() (Config, bool) {
	prov := os.Getenv("BLADE_LLM_PROVIDER")
	anthKey := os.Getenv("ANTHROPIC_API_KEY")
	oaiKey := os.Getenv("OPENAI_API_KEY")

	if prov == "" {
		switch {
		case anthKey != "":
			prov = "anthropic"
		case oaiKey != "":
			prov = "openai"
		default:
			return Config{}, false
		}
	}

	c := Config{
		BaseURL:          strings.TrimSpace(os.Getenv("BLADE_LLM_BASE_URL")),
		Model:            strings.TrimSpace(os.Getenv("BLADE_LLM_MODEL")),
		Proxy:            strings.TrimSpace(os.Getenv("BLADE_LLM_PROXY")),
		ThinkingType:     strings.TrimSpace(os.Getenv("BLADE_LLM_THINKING_TYPE")),
		ReasoningEffort:  strings.TrimSpace(os.Getenv("BLADE_LLM_REASONING_EFFORT")),
		SessionHeaderKey: strings.TrimSpace(os.Getenv("BLADE_LLM_SESSION_HEADER_KEY")),
		// 默认流式;BLADE_LLM_STREAM=false/0/off 显式关闭走非流式。
		Stream: !isFalsy(os.Getenv("BLADE_LLM_STREAM")),
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("BLADE_LLM_MAX_TOKENS"))); err == nil && n > 0 {
		c.MaxTokens = n
	}
	if cw, err := strconv.Atoi(strings.TrimSpace(os.Getenv("BLADE_LLM_CONTEXT_WINDOW_K"))); err == nil && cw > 0 {
		if cw > 1000 {
			cw = 1000
		}
		c.ContextWindowK = cw
	}
	if rps, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("BLADE_LLM_RATE_PER_SECOND")), 64); err == nil && rps > 0 {
		c.RatePerSecond = rps
	}
	if rpm, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("BLADE_LLM_RATE_PER_MINUTE")), 64); err == nil && rpm > 0 {
		c.RatePerMinute = rpm
	}
	switch prov {
	case "openai":
		c.Format = llm.FormatOpenAI
		c.APIKey = oaiKey
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
		if f := strings.TrimSpace(os.Getenv("BLADE_LLM_MAX_TOKENS_FIELD")); f == llm.MaxTokensFieldCompletion {
			c.MaxTokensField = f
		}
		if r := strings.TrimSpace(os.Getenv("BLADE_LLM_PROMPT_CACHE_RETENTION")); r == "in_memory" || r == "24h" {
			c.PromptCacheRetention = r
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		c.APIKey = oaiKey
		if c.Model == "" {
			c.Model = "gpt-5"
		}
		if r := strings.TrimSpace(os.Getenv("BLADE_LLM_PROMPT_CACHE_RETENTION")); r == "in_memory" || r == "24h" {
			c.PromptCacheRetention = r
		}
	default:
		c.Format = llm.FormatAnthropic
		c.APIKey = anthKey
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
		if b, err := strconv.Atoi(strings.TrimSpace(os.Getenv("BLADE_LLM_THINKING_BUDGET_TOKENS"))); err == nil && b > 0 {
			c.ThinkingBudgetTokens = b
		}
		c.InputTokensIncludesCache = isTruthy(os.Getenv("BLADE_LLM_INPUT_TOKENS_INCLUDES_CACHE"))
		c.PreserveThinkingBlocks = isTruthy(os.Getenv("BLADE_LLM_PRESERVE_THINKING_BLOCKS"))
		c.CacheBreakpoints = ResolveAnthropicCacheBreakpoints(os.Getenv("BLADE_LLM_CACHE_BREAKPOINTS"))
		if ttl := strings.TrimSpace(os.Getenv("BLADE_LLM_CACHE_TTL")); ttl == "5m" || ttl == "1h" {
			c.CacheTTL = ttl
		}
	}
	if c.APIKey == "" {
		return Config{}, false
	}
	return c, true
}

// ConfigFrom builds a Config from UI-provided strings (provider defaults to
// anthropic; model defaults per provider). Inputs are trimmed and the base URL
// is normalized to the API base the provider expects (the provider appends the
// endpoint path itself), so a full endpoint URL is tolerated.
func ConfigFrom(provider, model, baseURL, apiKey, proxy string) Config {
	c := Config{
		Model:   strings.TrimSpace(model),
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  strings.TrimSpace(apiKey),
		Proxy:   strings.TrimSpace(proxy),
		Stream:  true, // 默认流式;调用方按 profile 覆盖
	}
	switch strings.TrimSpace(provider) {
	case "openai":
		c.Format = llm.FormatOpenAI
		// provider appends "/chat/completions"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/chat/completions"), "/")
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		// provider appends "/responses"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/responses"), "/")
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.Format = llm.FormatAnthropic
		// provider appends "/v1/messages".
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/v1/messages"), "/")
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
	}
	return c
}

// isFalsy reports whether an env-var string explicitly requests "off". Empty or
// unrecognized → false (so an unset var keeps the streaming default).
func isFalsy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// Provider returns the short provider name ("anthropic"/"openai").
func (c Config) Provider() string {
	switch c.Format {
	case llm.FormatOpenAI:
		return "openai"
	case llm.FormatOpenAIResponses:
		return "openai-responses"
	}
	return "anthropic"
}

// NewProvider builds an llm.Provider from the config. When a rate is set, the
// limiter lives on the single provider instance — so planner + all workers +
// main agent (which share this provider) are bounded by one shared rate limit.
func (c Config) NewProvider() (llm.Provider, error) {
	return c.NewProviderWithLimiter(&RequestLimiter{})
}

// NewProviderWithLimiter builds a provider whose HTTP layer counts every attempt
// against limiter. Callers that cache providers per LLM profile MUST pass the same
// limiter across rebuilds, or a profile edit silently resets the quota.
func (c Config) NewProviderWithLimiter(limiter *RequestLimiter) (llm.Provider, error) {
	if limiter == nil {
		limiter = &RequestLimiter{}
	}
	if c.RatePerSecond > 0 || c.RatePerMinute > 0 {
		if err := limiter.Configure(c.RatePerSecond, c.RatePerMinute); err != nil {
			return nil, err
		}
	} else {
		// 未配置限流：清空窗口，避免沿用上一次的额度（profile 从有限流改成不限流）。
		if err := limiter.Configure(0, 0); err != nil {
			return nil, err
		}
	}
	client, err := quotaAwareHTTPClient(c.Proxy, c.SessionHeaderKey, c.CustomHeaders, limiter)
	if err != nil {
		return nil, err
	}
	lc := llm.Config{
		Format:     c.Format,
		BaseURL:    c.BaseURL,
		APIKey:     c.APIKey,
		Model:      c.Model,
		HTTPClient: client,
	}
	// 思考开关与强度两个字段各自透传(空 = 该字段不发送)。二者解耦:
	// 可只发 thinking.type、只发 effort、都发、或都不发。
	lc.ThinkingType = c.ThinkingType
	lc.ThinkingBudgetTokens = c.ThinkingBudgetTokens
	lc.ReasoningEffort = c.ReasoningEffort
	// 输出上限的字段名选择(空 = 用 max_tokens)。上限的「值」不在这里:它每轮随
	// agentcore.Options.MaxTokens 走,provider 只决定把它塞进哪个键。
	lc.MaxTokensField = c.MaxTokensField
	// KV cache 兼容选项与滚动断点：由 profile/endpoint 配置透传给 SDK，默认零值不变。
	lc.InputTokensIncludesCache = c.InputTokensIncludesCache
	lc.PreserveThinkingBlocks = c.PreserveThinkingBlocks
	lc.CacheBreakpoints = c.CacheBreakpoints
	lc.CacheTTL = c.CacheTTL
	lc.PromptCacheRetention = c.PromptCacheRetention
	// Explicit prompt_cache_retention on custom OpenAI endpoints implies the target
	// gateway accepts OpenAI prompt cache body parameters.
	lc.DisablePromptCacheKey = !(supportsPromptCacheKey(c.Format, c.BaseURL) ||
		(c.Format == llm.FormatOpenAI && strings.TrimSpace(c.PromptCacheRetention) != ""))
	// 重试参数与 SDK 同语义(次数 0=默认/负=关闭,间隔 0=指数退避/>0=固定),原样透传。
	lc.MaxRetries = c.Retry.ConnectAttempts
	lc.RetryInterval = c.Retry.ConnectInterval
	lc.EmptyResponseRetries = c.Retry.EmptyAttempts
	lc.EmptyResponseInterval = c.Retry.EmptyInterval
	// 限流只在 HTTP 层做（见 RequestLimiter）。SDK 自带的 token bucket 会与本层
	// 叠加成双重限流，且它不统计 SDK 内部重试，留着一个「看起来生效」的假护栏
	// 比没有更糟——所以这里不再设置 lc.RateLimit。
	return llm.NewProvider(lc)
}

// supportsPromptCacheKey reports whether sending OpenAI's "prompt_cache_key"
// body parameter is known-safe for the target endpoint (Responses API, official
// OpenAI, OpenRouter, or custom gateways that tolerate it without rejecting
// unknown top-level parameters). Strict OpenAI-compat relays like Gemini/DeepSeek
// can reject unknown body fields with HTTP 400, so leave them opted out.
func supportsPromptCacheKey(format llm.Format, baseURL string) bool {
	if format == llm.FormatOpenAIResponses {
		return true
	}
	if format != llm.FormatOpenAI {
		return false
	}
	u := strings.ToLower(strings.TrimSpace(baseURL))
	return u == "" || strings.Contains(u, "api.openai.com") || strings.Contains(u, "openrouter.ai")
}

// IsQuotaExhaustedMessage deliberately recognizes only explicit balance,
// billing, credit, or quota-exhaustion signals. Generic 429/rate-limit text,
// authentication failures, network errors, and server failures are excluded.
func IsQuotaExhaustedMessage(message string) bool {
	return llmpool.IsQuotaExhaustedMessage(message)
}

// quotaAwareTransport preserves Norma's normal retry behavior except for a 429
// whose body explicitly says the account quota/balance is exhausted. Norma's
// retry loop treats every 429 as transient; normalizing only that response to
// 402 lets a task router fail over immediately while retaining the original
// response body for provider-specific classification and audit logs.
type quotaAwareTransport struct {
	base http.RoundTripper
	// sessionHeaderKey, when non-empty, is the HTTP header name each request
	// carries; its value is the session id read from the request context. Empty
	// disables it. See Config.SessionHeaderKey.
	sessionHeaderKey string
	// customHeaders are fixed request headers from the profile (User-Agent etc).
	customHeaders map[string]string
}

func (t quotaAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Fixed per-profile headers first, so the session header below keeps precedence
	// if both configure the same name.
	for k, v := range t.customHeaders {
		req.Header.Set(k, v)
	}
	// Custom session-id header: name is user-configured, value is THIS run's
	// session id (norma stashes it on the context via transcript.WithSessionID).
	// Stable across a session's turns and distinct across sessions — exactly what
	// a session-keyed prompt cache wants. Skipped when no session id is present.
	if t.sessionHeaderKey != "" {
		if sid := transcript.SessionIDFrom(req.Context()); sid != "" {
			req.Header.Set(t.sessionHeaderKey, sid)
		}
	}
	// When LLM recording is on, the Recorder puts a Capture on the context so the
	// raw wire bodies can be persisted. This is the only layer that still sees
	// them: norma builds the request body internally and decodes the SSE response
	// before either reaches the recorder.
	//
	// 录制关闭时不要复制请求正文：一次模型请求的正文可能有数百 KB，热路径上
	// 只为写进一个 nil Capture 而整份读一遍是纯浪费。capt 为 nil 即未录制。
	capt := llmrec.CaptureFrom(req.Context())
	surface := llmrec.SurfaceFrom(req.Context())
	if capt != nil || surface != nil {
		body := requestBodySnapshot(req)
		capt.SetRequest(body)
		if surface != nil {
			surface.Set(observedRequestSurface(req, body))
		}
	}

	traceCache := traceModelRequest(req)
	hint := RetryHintFrom(req.Context())
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		hint.RecordTransportFailure()
		updateCacheDiagnostic(traceCache, func(row *CacheDiagnostic) { row.Complete = true })
		return resp, err
	}
	if resp.StatusCode == http.StatusOK {
		hint.RecordTransportSuccess()
	} else if resp.StatusCode >= http.StatusBadRequest {
		hint.RecordTransportFailure()
	}
	if traceCache != 0 {
		wrapCacheTraceResponse(req, resp, traceCache)
	}
	// Tee rather than read: a 200 is an SSE stream that must keep streaming. The
	// 429 branch below reads through this wrapper, so its body lands in the
	// capture before being replaced.
	resp.Body = capt.TeeResponse(resp.StatusCode, resp.Body)

	if resp.StatusCode == http.StatusServiceUnavailable {
		if ra := ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now()); ra > 0 {
			RetryHintFrom(req.Context()).Record(ra)
		}
		return resp, nil
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		return resp, nil
	}
	ra := ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	if readErr != nil {
		if ra > 0 {
			RetryHintFrom(req.Context()).Record(ra)
		}
		return resp, nil
	}
	if IsQuotaExhaustedMessage(string(body)) {
		resp.StatusCode = http.StatusPaymentRequired
		resp.Status = "402 Payment Required"
		return resp, nil
	}
	if ra > 0 {
		RetryHintFrom(req.Context()).Record(ra)
	}
	return resp, nil
}

// requestBodySnapshot copies an outgoing request body without consuming it.
// norma builds every model request from a *bytes.Reader, so net/http populates
// GetBody and the copy has no effect on what gets sent.
func requestBodySnapshot(req *http.Request) string {
	if req.GetBody == nil {
		return ""
	}
	rc, err := req.GetBody()
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	return string(b)
}

func quotaAwareHTTPClient(proxy, sessionHeaderKey string, customHeaders map[string]string, limiter *RequestLimiter) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Disable automatic gzip: an SSE response compressed here can be buffered by
	// an intermediary (Clash and similar) and never flushed, leaving the client
	// blocked inside flate. The SDK's default client does the same.
	transport.DisableCompression = true
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		transport.Proxy = nil // 留空=直连,不回退 HTTP_PROXY/HTTPS_PROXY 环境变量
	} else {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("llm: invalid proxy %q: %w", proxy, err)
		}
		switch proxyURL.Scheme {
		case "http", "https", "socks5":
		case "":
			return nil, fmt.Errorf("llm: proxy %q missing scheme (use http://, https:// or socks5://)", proxy)
		default:
			return nil, fmt.Errorf("llm: unsupported proxy scheme %q (use http, https or socks5)", proxyURL.Scheme)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	var rt http.RoundTripper = quotaAwareTransport{base: transport, sessionHeaderKey: strings.TrimSpace(sessionHeaderKey), customHeaders: customHeaders}
	if limiter != nil {
		// 限流包在最外层：SDK 的连接重试、空响应重试都会真实发出 HTTP 请求，
		// 只有在 RoundTrip 上计数才能把它们算进额度。
		rt = rateLimitedTransport{base: rt, limiter: limiter}
	}
	return &http.Client{Transport: rt}, nil
}

// logTestConnection prints the raw HTTP status code(s) and response body of a
// connection test to the server log, so "点击测试" leaves a diagnosable trail of
// exactly what the gateway returned — 401 bodies, quota text, empty frames — not
// just the collapsed ok/err the UI shows. Bodies are clipped to keep a chatty
// SSE stream from flooding the log.
func logTestConnection(c Config, capt *llmrec.Capture) {
	attempts := capt.Attempts()
	if len(attempts) == 0 {
		log.Printf("[llm-test] %s / %s @ %s — 未发出任何 HTTP 请求(配置解析或建连即失败)",
			c.Provider(), c.Model, c.BaseURL)
		return
	}
	for i, a := range attempts {
		log.Printf("[llm-test] %s / %s @ %s — 尝试 %d/%d HTTP %d\n响应体: %s",
			c.Provider(), c.Model, c.BaseURL, i+1, len(attempts), a.Status, clipBody(a.Body))
	}
}

// clipBody trims a wire body for logging. 4K is plenty to show an error JSON or
// the head of an SSE stream while bounding a runaway response.
func clipBody(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(空)"
	}
	const max = 4096
	if len(s) > max {
		return s[:max] + fmt.Sprintf("…(截断,共 %d 字节)", len(s))
	}
	return s
}

// TestConnection makes a minimal real completion to verify the provider/model/
// endpoint/key actually work. Returns the round-trip latency and the model's
// reply text.
func TestConnection(ctx context.Context, c Config) (time.Duration, string, error) {
	prov, err := c.NewProvider()
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// 抓取原始 wire 报文:连接测试最需要看到的就是网关到底回了什么(状态码+响应体),
	// 而 norma 把响应解码成 StreamEvent 后这些就没了。quotaAwareTransport 会在
	// context 里找到这个 Capture 并填入每次 HTTP 尝试的状态码与 body。
	ctx, capt := llmrec.NewCapture(ctx)
	defer logTestConnection(c, capt)
	// 连接测试是一条单发路径,不经过 agentcore 的会话循环,因此没人往 context 上挂
	// session id。对配了 SessionHeaderKey 的端点(如 opencode zen 强制要求
	// x-opencode-session 头,缺了直接 400 MissingSessionID),这会导致"对话正常、
	// 点击测试却 400"的落差。这里补挂一个一次性随机 session id,让测试与真实对话走同
	// 一套发头逻辑;未配 SessionHeaderKey 的端点不读它,无副作用。
	ctx = transcript.WithSessionID(ctx, "conntest-"+transcript.NewSessionID())
	start := time.Now()
	// MaxTokens 要给足：推理模型(如 deepseek-v4-pro)在给出答案前会先产出一大段
	// 思考(实测对一句 "ping" 也能烧 ~2900 token)。若只给 32,模型会一直卡在"思考阶段"
	// 就撞到输出上限(finish=length)、被截断,连接测试虽仍算通(err=nil)但显示成
	// "已中断/length/resume" 一团糟。给足预算让它把 OK 干净吐完(finish=stop)。
	// EscalateMaxTokens 保持 false:不因截断而抬额重试,避免 resume 循环空烧。
	maxTok := 8192
	if c.MaxTokens > 0 {
		maxTok = c.MaxTokens
	} else if c.Format == llm.FormatAnthropic && c.ThinkingType == "enabled" && c.ThinkingBudgetTokens >= maxTok {
		maxTok = c.ThinkingBudgetTokens + 4096
	}
	reply, err := agentcore.Run(ctx, agentcore.Options{
		Provider:             prov,
		SystemPrompt:         []string{"你是连接测试。直接输出两个字符 OK 即可，不要思考、不要解释、不要别的。"},
		PermissionMode:       acperm.ModeBypass,
		MaxTurns:             1,
		MaxTokens:            maxTok,
		NonStreaming:         !c.Stream, // 用该 profile 的真实收发模式做连接测试
		CacheTTL:             c.CacheTTL,
		PromptCacheRetention: c.PromptCacheRetention,
		PreserveThinking:     c.PreservesThinking(),
	}, "ping")
	lat := time.Since(start)
	if err != nil {
		return lat, "", err
	}
	// err==nil 还不够：请求通了但模型一个字都不吐的情况真实存在(思考把预算烧光、
	// 正文被安全策略吞掉、兼容层把 content 丢了)。这种配置在会话里就是"不回话",
	// 测试却报成功——正是本项要消除的落差。没有可见正文一律判失败。
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return lat, "", fmt.Errorf("模型无回复内容（请求已通，但未返回任何文本）")
	}
	return lat, reply, nil
}
