// Package intercept implements the user-configurable tool-call interception layer.
// Rules are loaded from the database, cached in memory, and evaluated in priority
// order (highest first) on every PreToolUse event. Three actions are supported:
//
//   - allow: immediately permits the call, skipping lower-priority rules.
//   - deny:  blocks the call and returns a message to the model.
//   - ask:   blocks the call, creates an intercept_pending record, writes an
//     activity to the active conversation, then waits for the user to approve or
//     deny via the /api/intercept/pending/{id}/decide endpoint.
//
// The timeout behaviour is configurable at runtime via SetTimeoutConfig.
package intercept

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

// ctxKey is the unexported context key type to avoid collisions.
type ctxKey int

// ConvIDKey stores the active conversation ID in a context.Context so the
// interceptor can associate "ask" pending records with the right conversation.
const ConvIDKey ctxKey = 0

// WithConvID returns a child context carrying convID.
func WithConvID(ctx context.Context, convID int64) context.Context {
	return context.WithValue(ctx, ConvIDKey, convID)
}

// ConvIDFromContext extracts the conversation ID (0 if absent).
func ConvIDFromContext(ctx context.Context) int64 {
	v, _ := ctx.Value(ConvIDKey).(int64)
	return v
}

// taskCtxKey is a separate unexported key type for task context values.
type taskCtxKey int

const (
	taskInfoCtxKey taskCtxKey = 1
	taskEmitCtxKey taskCtxKey = 2
)

type taskCtxInfo struct{ taskID, agentName string }

// WithTaskContext injects task metadata and an emit function into ctx so that
// HandleAsk can tag pending records and write intercept_request activities to
// the task's exploration stream (making them appear inline in session transcripts).
func WithTaskContext(ctx context.Context, taskID, agentName string, emit func(db.Activity)) context.Context {
	ctx = context.WithValue(ctx, taskInfoCtxKey, taskCtxInfo{taskID, agentName})
	if emit != nil {
		ctx = context.WithValue(ctx, taskEmitCtxKey, emit)
	}
	return ctx
}

func taskInfoFromCtx(ctx context.Context) (taskID, agentName string) {
	if v, ok := ctx.Value(taskInfoCtxKey).(taskCtxInfo); ok {
		return v.taskID, v.agentName
	}
	return "", ""
}

func taskEmitFromCtx(ctx context.Context) func(db.Activity) {
	f, _ := ctx.Value(taskEmitCtxKey).(func(db.Activity))
	return f
}

// compiledRule is an InterceptRule with the regex pre-compiled (nil for string rules).
type compiledRule struct {
	db.InterceptRule
	re *regexp.Regexp
}

// pendingManager tracks in-flight "ask" requests via per-request channels.
// AskOutcome 是一次人工审批的结论：允许与否，以及（可选）人改过的参数。
// Edited 非空表示"改参放行"——调用方必须先用 Narrow 复核再执行。
type AskOutcome struct {
	Allowed bool
	Edited  json.RawMessage
	Diff    []string
}

type pendingManager struct {
	mu sync.Mutex
	ch map[int64]chan AskOutcome
}

func newPendingManager() *pendingManager { return &pendingManager{ch: map[int64]chan AskOutcome{}} }

func (p *pendingManager) add(id int64) chan AskOutcome {
	ch := make(chan AskOutcome, 1)
	p.mu.Lock()
	p.ch[id] = ch
	p.mu.Unlock()
	return ch
}

func (p *pendingManager) resolve(id int64, out AskOutcome) {
	p.mu.Lock()
	ch, ok := p.ch[id]
	delete(p.ch, id)
	p.mu.Unlock()
	if ok {
		ch <- out
	}
}

func (p *pendingManager) remove(id int64) {
	p.mu.Lock()
	delete(p.ch, id)
	p.mu.Unlock()
}

// Reviewer runs the LLM fallback judge for one tool call and returns its verdict
// as a Decision (Action ∈ allow|ask|deny; empty Action means the reply could not
// be parsed). It is injected by the server layer so the intercept package stays
// free of any llm dependency. profileID == 0 means "use the active/default profile".
type Reviewer func(ctx context.Context, profileID int64, prompt string, input ReviewInput) (Decision, error)

// Interceptor loads intercept rules from the database and evaluates them on
// tool calls. It is safe for concurrent use.
type Interceptor struct {
	db           *db.DB
	mu           sync.RWMutex
	cached       []compiledRule  // sorted by priority DESC; nil means not loaded yet
	enabledTools map[string]bool // nil means not loaded yet
	pending      *pendingManager
	reviewer     Reviewer // nil = LLM fallback judge not wired

	// RoE scope gate (see scope.go): per-task scope rows with a small TTL cache.
	scopeMu    sync.Mutex
	scopeCache map[int64]scopeCacheEntry
}

// SetReviewer installs the LLM fallback judge callback. Passing nil disables it.
func (i *Interceptor) SetReviewer(r Reviewer) {
	i.mu.Lock()
	i.reviewer = r
	i.mu.Unlock()
}

// defaultEnabledTools is the hard-coded set of tools that enter the intercept
// rule system when no intercept_enabled_tools setting has been saved.
var defaultEnabledTools = []string{
	"Bash", "WebFetch", "web_search",
	"shell_open", "shell_send",
	"Write", "Edit", "MultiEdit",
}

// New creates an Interceptor backed by d. The rule cache is lazy-loaded on
// first use.
func New(d *db.DB) *Interceptor {
	return &Interceptor{db: d, pending: newPendingManager()}
}

// Invalidate clears the in-memory rule cache and the enabled-tools cache.
// The next call to Match or IsToolEnabled will reload from the database.
// Call this after any CRUD operation on rules or tool config.
func (i *Interceptor) Invalidate() {
	i.mu.Lock()
	i.cached = nil
	i.enabledTools = nil
	i.mu.Unlock()
}

func (i *Interceptor) loadLocked() error {
	rules, err := i.db.ListInterceptRules()
	if err != nil {
		return err
	}
	var out []compiledRule
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		cr := compiledRule{InterceptRule: r}
		if r.MatchType == "regex" {
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				continue // skip rules with bad regex rather than crashing
			}
			cr.re = re
		}
		out = append(out, cr)
	}
	i.cached = out

	// Load enabled-tools set from settings, falling back to hard-coded defaults.
	val, ok, _ := i.db.GetSetting("intercept_enabled_tools")
	if !ok {
		m := make(map[string]bool, len(defaultEnabledTools))
		for _, n := range defaultEnabledTools {
			m[n] = true
		}
		i.enabledTools = m
	} else {
		var names []string
		if json.Unmarshal([]byte(val), &names) != nil {
			i.enabledTools = map[string]bool{}
		} else {
			m := make(map[string]bool, len(names))
			for _, n := range names {
				m[n] = true
			}
			i.enabledTools = m
		}
	}
	return nil
}

func (i *Interceptor) rules() ([]compiledRule, error) {
	i.mu.RLock()
	if i.cached != nil {
		out := i.cached
		i.mu.RUnlock()
		return out, nil
	}
	i.mu.RUnlock()

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cached != nil {
		return i.cached, nil
	}
	if err := i.loadLocked(); err != nil {
		return nil, err
	}
	return i.cached, nil
}

// IsToolEnabled returns true if the named tool is in the intercept-enabled set
// (i.e. it should enter the rule-matching path). Uses the same double-check lock
// pattern as rules().
func (i *Interceptor) IsToolEnabled(name string) bool {
	i.mu.RLock()
	if i.enabledTools != nil {
		v := i.enabledTools[name]
		i.mu.RUnlock()
		return v
	}
	i.mu.RUnlock()

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.enabledTools == nil {
		if err := i.loadLocked(); err != nil {
			// 失效安全（P12）：设置读不出来时退回硬编码默认集——配了拦截面的工具
			// 不能因为一次 DB 抖动就整体退出拦截面（宁可多拦，不可漏拦）。
			for _, n := range defaultEnabledTools {
				if n == name {
					return true
				}
			}
			return false
		}
	}
	return i.enabledTools[name]
}

// GetEnabledTools returns the ordered list of tool names that are currently
// configured to enter the intercept rule system. When the setting has never been
// saved the hard-coded default list is returned.
func (i *Interceptor) GetEnabledTools() ([]string, error) {
	val, ok, err := i.db.GetSetting("intercept_enabled_tools")
	if err != nil {
		return nil, err
	}
	if !ok {
		out := make([]string, len(defaultEnabledTools))
		copy(out, defaultEnabledTools)
		return out, nil
	}
	var names []string
	if err := json.Unmarshal([]byte(val), &names); err != nil {
		return []string{}, nil
	}
	return names, nil
}

// SetEnabledTools persists the list of tool names that should enter the intercept
// rule system, then invalidates the cache so the next call picks up the new list.
func (i *Interceptor) SetEnabledTools(tools []string) error {
	b, err := json.Marshal(tools)
	if err != nil {
		return err
	}
	if err := i.db.SetSetting("intercept_enabled_tools", string(b)); err != nil {
		return err
	}
	i.Invalidate()
	return nil
}

// Decision is the outcome of a successful rule match.
type Decision struct {
	ModelInput       json.RawMessage
	ModelInputDigest string
	ModelFallback    bool
	RuleName         string
	ConfigDigest     string
	ProfileID        int64
	Action           string // "allow" | "deny" | "ask"
	Message          string
	RuleID           int64
	TimeoutEnabled   bool
	TimeoutSeconds   int
	TimeoutAction    string // "deny" | "allow"
	// MatchedSurface / MatchedText 记录规则命中的具体表面（raw | nested_string |
	// nested_key | percent_decoded:N | tool_name）与命中文本片段（截断），写入
	// intercept audit —— P12 遮挡面：让「为什么命中/为什么没命中」可复盘。
	MatchedSurface string
	MatchedText    string
	// RawReply 是裁判模型的原始回复（P15）：结构化模式下由 Judge 解析成
	// StructuredVerdict 再按代码阈值定动作。不进 audit JSON（audit 存 ModelVerdict）。
	RawReply string
	// ModelVerdict 是结构化裁决压缩成的一行 JSON，写进 audit —— 复盘"模型当时
	// 各轴打了多少分、建议什么、置信多少"。
	ModelVerdict string
}

// --- LLM fallback judge ---

// settingLoadFailAction 规则快照加载失败（DB 读不到）时的裁决：allow|ask|deny。
// 默认 ask（失效安全）——此前实现是静默按「未命中」放行；对配了 deny 规则/授权
// 边界的用户，静默放行意味着规则看起来还在、实际已经失效。
const (
	settingLoadFailAction        = "intercept_load_fail_action"
	defaultLoadFailAction        = "ask"
	settingJudgeEnabled          = "llm_judge_enabled"
	settingJudgeProfileID        = "llm_judge_profile_id"
	settingJudgePrompt           = "llm_judge_prompt"
	settingJudgeTimeoutSecs      = "llm_judge_timeout_seconds"
	settingJudgeFailAction       = "llm_judge_fail_action"
	settingJudgeAskTimeoutSecs   = "llm_judge_ask_timeout_seconds"
	settingJudgeAskTimeoutAction = "llm_judge_ask_timeout_action"
	settingJudgeStructured       = "llm_judge_structured"
)

// Judge default values.
const (
	defaultJudgeTimeoutSecs = 15
	// defaultJudgeFailAction 是裁判【失败】时的动作（模型不可用/输出无法解析）。
	// P15 起为 ask：放宽方向的默认值让"配了裁判"在故障时退化成静默放行——与 P12
	// 把规则加载失败改成 ask 同一条纪律（失效安全）。想放行可在设置页显式改成 allow。
	defaultJudgeFailAction       = "ask"
	defaultJudgeAskTimeoutSecs   = 300
	defaultJudgeAskTimeoutAction = "deny"
	// defaultJudgeStructured 默认开启结构化裁决（P15）：模型给各轴把握与建议，
	// 最终动作由 intercept.DecideStructured 按代码里的阈值算出。
	defaultJudgeStructured = true
)

// JudgeConfig is the resolved LLM-fallback-judge configuration. Prompt is always
// non-empty (falls back to DefaultJudgePrompt).
type JudgeConfig struct {
	Enabled           bool   `json:"enabled"`
	ProfileID         int64  `json:"profile_id"` // 0 = follow active/default
	Prompt            string `json:"prompt"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	FailAction        string `json:"fail_action"` // allow|ask|deny
	AskTimeoutSeconds int    `json:"ask_timeout_seconds"`
	AskTimeoutAction  string `json:"ask_timeout_action"` // allow|deny
	// Structured：结构化裁决（P15）。开启时裁判按"各轴把握 + 建议 choice + 置信度"
	// 输出，最终 allow/ask/deny 由代码阈值决定（intercept.DecideStructured）。
	Structured bool `json:"structured"`
}

// judgeConfig reads the judge configuration from settings, applying defaults for
// missing/invalid keys. Read fresh on each fallback judgement — the LLM call that
// follows dwarfs a few KV reads, and freshness avoids a cache-invalidation path.
func (i *Interceptor) judgeConfig() JudgeConfig {
	c := JudgeConfig{
		Enabled:           i.db.GetBool(settingJudgeEnabled, false),
		ProfileID:         int64(i.getSettingInt(settingJudgeProfileID, 0)),
		TimeoutSeconds:    i.getSettingInt(settingJudgeTimeoutSecs, defaultJudgeTimeoutSecs),
		FailAction:        i.getSettingChoice(settingJudgeFailAction, defaultJudgeFailAction, "allow", "ask", "deny"),
		AskTimeoutSeconds: i.getSettingInt(settingJudgeAskTimeoutSecs, defaultJudgeAskTimeoutSecs),
		AskTimeoutAction:  i.getSettingChoice(settingJudgeAskTimeoutAction, defaultJudgeAskTimeoutAction, "allow", "deny"),
		Structured:        i.db.GetBool(settingJudgeStructured, defaultJudgeStructured),
	}
	// Prompt: stored value if non-empty, else the built-in template.
	if v, ok, _ := i.db.GetSetting(settingJudgePrompt); ok && strings.TrimSpace(v) != "" {
		c.Prompt = v
	} else {
		c.Prompt = DefaultJudgePrompt
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = defaultJudgeTimeoutSecs
	}
	if c.AskTimeoutSeconds <= 0 {
		c.AskTimeoutSeconds = defaultJudgeAskTimeoutSecs
	}
	return c
}

func (i *Interceptor) getSettingInt(key string, def int) int {
	v, ok, err := i.db.GetSetting(key)
	if err != nil || !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

func (i *Interceptor) getSettingChoice(key, def string, allowed ...string) string {
	v, ok, err := i.db.GetSetting(key)
	if err != nil || !ok {
		return def
	}
	v = strings.TrimSpace(v)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return def
}

// GetJudgeConfig returns the resolved judge configuration for the API/UI. Prompt
// is the effective prompt (built-in template when unset), so the UI can prefill.
func (i *Interceptor) GetJudgeConfig() JudgeConfig { return i.judgeConfig() }

// SetJudgeConfig persists the judge configuration. An empty Prompt clears the
// override (the built-in template is used again).
func (i *Interceptor) SetJudgeConfig(c JudgeConfig) error {
	if err := i.db.SetBool(settingJudgeEnabled, c.Enabled); err != nil {
		return err
	}
	if err := i.db.SetSetting(settingJudgeProfileID, strconv.FormatInt(c.ProfileID, 10)); err != nil {
		return err
	}
	// Store the prompt only when it differs from the built-in template, so version
	// updates to DefaultJudgePrompt flow through for users who never customized it.
	promptToStore := ""
	if strings.TrimSpace(c.Prompt) != "" && strings.TrimSpace(c.Prompt) != strings.TrimSpace(DefaultJudgePrompt) {
		promptToStore = c.Prompt
	}
	if err := i.db.SetSetting(settingJudgePrompt, promptToStore); err != nil {
		return err
	}
	if err := i.db.SetSetting(settingJudgeTimeoutSecs, strconv.Itoa(c.TimeoutSeconds)); err != nil {
		return err
	}
	if err := i.db.SetSetting(settingJudgeFailAction, c.FailAction); err != nil {
		return err
	}
	if err := i.db.SetSetting(settingJudgeAskTimeoutSecs, strconv.Itoa(c.AskTimeoutSeconds)); err != nil {
		return err
	}
	if err := i.db.SetSetting(settingJudgeAskTimeoutAction, c.AskTimeoutAction); err != nil {
		return err
	}
	if err := i.db.SetBool(settingJudgeStructured, c.Structured); err != nil {
		return err
	}
	return nil
}

// Judge runs the LLM fallback judge for a tool call that matched no rule. It
// returns (Decision, true) when the judge produced a terminal verdict, or
// (Decision{}, false) when the fallback is disabled or not wired (caller then
// keeps the current behavior: allow). On model error or an unparseable reply it
// falls back to the configured FailAction. Ask verdicts carry the human-approval
// timeout so the existing HandleAsk consumes them unchanged.
func (i *Interceptor) Judge(ctx context.Context, tool string, arguments json.RawMessage) (Decision, bool) {
	cfg := i.judgeConfig()
	i.mu.RLock()
	rv := i.reviewer
	i.mu.RUnlock()
	if !cfg.Enabled || rv == nil {
		return Decision{}, false
	}

	cctx := ctx
	if cfg.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	input, contextErr := BuildReviewInput(cctx, tool, arguments)
	cfg.Prompt = EffectiveJudgePromptFor(cfg.Prompt, cfg.Structured)
	var out Decision
	var err error
	var modelInput []byte
	if contextErr != nil {
		// Invalid current arguments cannot be reviewed faithfully, regardless of
		// the configured model-failure strategy. A human must resolve the input.
		out = Decision{Action: "ask", ModelFallback: true, Message: "审查上下文不完整，需要人工确认：" + contextErr.Error()}
	} else {
		modelInput, _ = json.Marshal(input)
		out, err = rv(cctx, cfg.ProfileID, cfg.Prompt, input)
	}
	if err == nil && cfg.Structured && out.Action == "" {
		// 结构化模式：Reviewer 只回原始文本，动作由这里按代码阈值算。
		// 解析失败不在这里定动作——下面 switch 会走失败策略（默认 ask）。
		if sv, perr := ParseStructuredVerdict(out.RawReply); perr == nil {
			action, reason := DecideStructured(sv)
			out.Action = action
			out.Message = reason
			out.ModelVerdict = StructuredVerdictJSON(sv)
		} else {
			out.Message = "结构化裁决解析失败：" + perr.Error()
		}
	}
	if err != nil {
		out = Decision{ProfileID: out.ProfileID, ModelFallback: true, Action: cfg.FailAction, Message: "模型审批失败,按失败策略处理: " + err.Error()}
	}
	switch out.Action {
	case "allow", "ask", "deny":
		// valid verdict
	default:
		msg := "模型输出无法解析,按失败策略处理"
		if cfg.Structured {
			msg = "模型输出无法解析为结构化裁决,按失败策略处理"
		}
		if out.Message != "" && strings.HasPrefix(out.Message, "结构化裁决解析失败：") {
			msg += "：" + strings.TrimPrefix(out.Message, "结构化裁决解析失败：")
		}
		out = Decision{ProfileID: out.ProfileID, ModelFallback: true, Action: cfg.FailAction, Message: msg}
	}
	// A model verdict never carries a rule; keep RuleID 0 (→ NULL) for history.
	out.RuleID = 0
	if len(modelInput) > 0 {
		out.ModelInput = modelInput
		out.ModelInputDigest = digestInput(modelInput)
	}
	if out.ProfileID != 0 {
		cfg.ProfileID = out.ProfileID
	}
	out.ProfileID = cfg.ProfileID
	configJSON, _ := json.Marshal(cfg)
	out.ConfigDigest = digestInput(configJSON)
	if out.Message == "" {
		out.Message = "[模型] " + judgeActionLabel(out.Action)
	} else if !strings.HasPrefix(out.Message, "[模型]") {
		out.Message = "[模型] " + out.Message
	}
	if out.Action == "ask" {
		out.TimeoutEnabled = true
		out.TimeoutSeconds = cfg.AskTimeoutSeconds
		out.TimeoutAction = cfg.AskTimeoutAction
	}
	return out, true
}

func judgeActionLabel(action string) string {
	switch action {
	case "allow":
		return "放行"
	case "deny":
		return "拦截"
	case "ask":
		return "转人工审批"
	default:
		return action
	}
}

// Match evaluates the rule list (priority DESC) against a tool call.
// Returns (Decision, true) for the first matching enabled rule, or
// (Decision{}, false) if no rule matches.
func (i *Interceptor) Match(toolName string, input []byte) (Decision, bool) {
	rules, err := i.rules()
	if err != nil {
		// 失效安全（P12）：规则读不出来不再静默放行。默认 ask（转人工），
		// 可经 setting 配成 allow（旧行为）/deny。
		action := i.getSettingChoice(settingLoadFailAction, defaultLoadFailAction, "allow", "ask", "deny")
		if action == "allow" {
			return Decision{}, false
		}
		return Decision{
			RuleName: "[拦截规则加载失败]",
			Action:   action,
			Message:  "拦截规则未能加载（" + err.Error() + "），按失效安全策略处理",
		}, true
	}
	if len(rules) == 0 {
		return Decision{}, false
	}
	for _, r := range rules {
		text, surface, ok := matchRule(r, toolName, input)
		if !ok {
			continue
		}
		configJSON, _ := json.Marshal(r.InterceptRule)
		return Decision{
			RuleName: r.Name, ConfigDigest: digestInput(configJSON),
			Action:         r.Action,
			Message:        renderRuleMessage(r, text, toolName),
			RuleID:         r.ID,
			TimeoutEnabled: r.TimeoutEnabled,
			TimeoutSeconds: r.TimeoutSeconds,
			TimeoutAction:  r.TimeoutAction,
			MatchedText:    clipRunes(text, 200),
			MatchedSurface: surface,
		}, true
	}
	return Decision{}, false
}

// matchRule 在规则的一个匹配面上找第一处命中，返回命中文本与其表面
// （tool_name 规则仍只匹配工具名；tool_input 规则遍历 candidateTexts 的全部候选面）。
func matchRule(r compiledRule, toolName string, input []byte) (text, surface string, ok bool) {
	switch r.MatchTarget {
	case "tool_name":
		if ruleMatchesText(r, toolName) {
			return toolName, "tool_name", true
		}
		return "", "", false
	case "tool_input":
		for _, c := range candidateTexts(input) {
			if ruleMatchesText(r, c.Text) {
				return c.Text, c.Surface, true
			}
		}
		return "", "", false
	}
	return "", "", false
}

func ruleMatchesText(r compiledRule, subject string) bool {
	if r.MatchType == "regex" {
		return r.re != nil && r.re.MatchString(subject)
	}
	return strings.Contains(subject, r.Pattern)
}

// renderRuleMessage 渲染规则文案：支持 {match}/{tool}/{rule} 占位；命中部分截断
// 后再代入，且用单次 strings.NewReplacer——命中文本里写着的 "{tool}" 之类不会被
// 再替换成别的变量（防命中内容注入模板）。
func renderRuleMessage(r compiledRule, matched, toolName string) string {
	msg := r.Message
	if msg == "" {
		return defaultMessage(r.Action, r.Name)
	}
	if !strings.Contains(msg, "{") {
		return msg
	}
	return strings.NewReplacer(
		"{match}", clipRunes(matched, 500),
		"{tool}", toolName,
		"{rule}", r.Name,
	).Replace(msg)
}

// CandidatePreview 是 dry-run 展示用的一条匹配面（文本截断 + 表面标注）。
type CandidatePreview struct {
	Text    string `json:"text"`
	Surface string `json:"surface"`
}

// CandidateSurfaces 把一次工具调用的输入展开成规则实际会逐条判定的候选面，
// 供「规则测试」展示（limit<=0 时默认 50）。只读、不接触数据库。
func CandidateSurfaces(input []byte, limit int) []CandidatePreview {
	if limit <= 0 {
		limit = 50
	}
	cands := candidateTexts(input)
	out := make([]CandidatePreview, 0, len(cands))
	for _, c := range cands {
		out = append(out, CandidatePreview{Text: clipRunes(c.Text, 300), Surface: c.Surface})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// PreviewRule dry-runs 一条规则定义（未落库）against 一次样例调用：与真实匹配
// 路径共用同一套候选面展开与文案渲染；返回首个命中的文本/表面与渲染后的文案。
// 非法正则返回 error；不写任何数据库记录。
func PreviewRule(toolName string, input []byte, matchTarget, matchType, pattern, action, ruleName, message string) (matched bool, text, surface, rendered string, err error) {
	cr := compiledRule{InterceptRule: db.InterceptRule{
		Name: ruleName, MatchTarget: matchTarget, MatchType: matchType,
		Pattern: pattern, Action: action, Message: message,
	}}
	if matchType == "regex" {
		re, cerr := regexp.Compile(pattern)
		if cerr != nil {
			return false, "", "", "", cerr
		}
		cr.re = re
	}
	t, s, ok := matchRule(cr, toolName, input)
	if !ok {
		return false, "", "", "", nil
	}
	return true, clipRunes(t, 500), s, renderRuleMessage(cr, t, toolName), nil
}

// clipRunes 截断到 max 个字符（超出加省略号），用于审计与文案。
func clipRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}

func defaultMessage(action, name string) string {
	switch action {
	case "deny":
		return "拦截规则 [" + name + "] 禁止执行此工具"
	case "ask":
		return "拦截规则 [" + name + "] 要求用户审批，请等待"
	default:
		return ""
	}
}

// Log records an allow/deny rule or model decision into intercept_pending as an ALREADY-decided
// row (status = "allowed" | "denied"), for observability. Unlike HandleAsk it does NOT
// block and needs no user action — it makes explicit review decisions visible on the history
// page (GET /api/intercept/history) and the task's intercept list. Best-effort: a DB
// error is swallowed so logging never changes the tool call's outcome. The pending list
// (status='pending') is unaffected, so it still shows only asks awaiting a decision.
func (i *Interceptor) Log(ctx context.Context, convID int64, dec Decision, toolName string, input []byte, status string) {
	taskID, agentName := taskInfoFromCtx(ctx)
	audit := auditFor(ctx, dec, input, status)
	id, err := i.db.CreateDecidedIntercept(dec.RuleID, convID, taskID, agentName, toolName, input, status, dec.Message, audit)
	if err == nil {
		i.bindResult(ctx, id, audit)
	}
}

// HandleAsk creates a pending approval record and blocks until the user decides
// (via /api/intercept/pending/{id}/decide) or the per-rule timeout elapses.
// Returns true if the user approved.
//
// convID == 0 means no active conversation (background pentest task). The
// pending record is still created (conversation_id = NULL) so the approvals
// page shows it and the sidebar badge lights up. The worker thread blocks just
// like in a chat session — the user must visit the approvals page to unblock it.
func (i *Interceptor) HandleAsk(ctx context.Context, convID int64, dec Decision, toolName string, input []byte) (AskOutcome, bool) {
	ruleID := dec.RuleID
	taskID, agentName := taskInfoFromCtx(ctx)
	taskEmit := taskEmitFromCtx(ctx)

	audit := auditFor(ctx, dec, input, "pending")
	pendingID, err := i.db.CreateInterceptPending(ruleID, convID, taskID, agentName, toolName, input, dec.Message, audit)
	if err != nil {
		return AskOutcome{}, false
	}

	i.bindResult(ctx, pendingID, audit)
	ch := i.pending.add(pendingID)
	defer i.pending.remove(pendingID)
	// Polling clients can decide after INSERT commits but before the channel is
	// registered. Re-read after registration so that decision cannot be lost;
	// later decisions will be delivered through ch.
	if saved, err := i.db.GetInterceptDetail(pendingID); err == nil && saved != nil && saved.Status != "pending" {
		return AskOutcome{Allowed: saved.Status == "allowed" || (saved.Audit != nil && saved.Audit.EffectiveAction == "allow")}, true
	}

	detail, _ := json.Marshal(map[string]any{
		"pending_id": pendingID,
		"tool":       toolName,
		"input":      json.RawMessage(input),
	})
	activity := db.Activity{
		Kind:    "intercept_request",
		Summary: fmt.Sprintf("工具 %s 请求审批 (#%d)", toolName, pendingID),
		Detail:  string(detail),
	}

	if convID != 0 {
		// Chat session: write inline card to the conversation stream.
		_, _ = i.db.AppendConvActivity(convID, activity)
	} else if taskEmit != nil {
		// Task worker: emit to the exploration activity stream so it appears
		// inline in the session transcript (the emit fn stamps NodeID + Worker).
		taskEmit(activity)
	}

	if !dec.TimeoutEnabled {
		// No timeout: wait indefinitely until user decides or worker stops.
		select {
		case out := <-ch:
			return out, out.Allowed
		case <-ctx.Done():
			_, _ = i.db.ResolveIntercept(pendingID, "denied", "deny", "工作已取消")
			_ = i.db.CompleteIntercept(pendingID, audit.RunID, audit.ToolUseID, "not_executed", "执行前工作已取消", false)
			return AskOutcome{}, false
		}
	}

	secs := dec.TimeoutSeconds
	if secs <= 0 {
		secs = 60
	}
	timer := time.NewTimer(time.Duration(secs) * time.Second)
	defer timer.Stop()
	select {
	case out := <-ch:
		return out, out.Allowed
	case <-timer.C:
		allowed := dec.TimeoutAction == "allow"
		action := "deny"
		if allowed {
			action = "allow"
		}
		resolved, err := i.db.ResolveIntercept(pendingID, "timeout", action, "审批超时，按超时策略处理")
		if err != nil {
			return AskOutcome{}, false
		}
		if !resolved {
			detail, err := i.db.GetInterceptDetail(pendingID)
			ok := err == nil && detail != nil && (detail.Status == "allowed" || (detail.Audit != nil && detail.Audit.EffectiveAction == "allow"))
			return AskOutcome{Allowed: ok}, ok
		}
		return AskOutcome{Allowed: allowed}, allowed
	case <-ctx.Done():
		_, _ = i.db.ResolveIntercept(pendingID, "denied", "deny", "工作已取消")
		_ = i.db.CompleteIntercept(pendingID, audit.RunID, audit.ToolUseID, "not_executed", "执行前工作已取消", false)
		return AskOutcome{}, false
	}
}

var ErrAlreadyDecided = errors.New("审批已处理或不存在，请刷新记录")

// Decide resolves a pending request. Called by the HTTP decide endpoint.
func (i *Interceptor) Decide(pendingID int64, allowed bool) error {
	return i.DecideWithInput(pendingID, allowed, nil, nil)
}

// DecideWithInput resolves a pending request, optionally with the human-edited
// (narrowed) arguments. edited must already have passed Narrow — the caller
// (HTTP handler) validates first so a bad edit is a 400, not a half-applied
// decision; the guard re-validates before executing as a second line of defense.
//
// The edit and its diff go into the audit next to the decision, so the record
// answers "which exact arguments were approved" and not merely "approved".
func (i *Interceptor) DecideWithInput(pendingID int64, allowed bool, edited json.RawMessage, diff []string) error {
	status := "denied"
	if allowed {
		status = "allowed"
	}
	action, reason := "deny", "人工拒绝执行"
	if allowed {
		action, reason = "allow", "人工允许执行"
	}
	var extra map[string]any
	if allowed && len(edited) > 0 {
		reason = "人工改参放行（只许收窄）：" + strings.Join(diff, "；")
		extra = map[string]any{"edited_input": json.RawMessage(edited), "narrow_diff": diff}
	}
	resolved, err := i.db.ResolveInterceptWith(pendingID, status, action, reason, extra)
	if err != nil {
		return err
	}
	if !resolved {
		return ErrAlreadyDecided
	}
	i.pending.resolve(pendingID, AskOutcome{Allowed: allowed, Edited: edited, Diff: diff})
	return nil
}
