package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/intercept"
)

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
// WebSearchOpts is the web-search backend selection the server pushes into each
// agent (planner/worker/main). Enabled=false leaves the web_search tool off.
// Backend is "ddgs" (no key), "brave-free" (BraveKey required), "tavily"
// (TavilyKey required), or "deepseek" (DeepSeek* required, filled from the
// active LLM profile). It maps directly onto agentcore.Options.
// Proxy is a dedicated egress proxy for the search request (http/https/socks5),
// independent of the traffic-recording MITM proxy. For ddgs an empty value means
// "use the platform egress proxy" (direct DDG is routinely 202-rate-limited);
// server-side backends keep their own egress semantics.
//
// 注意 deepseek 后端与其它三个的性质不同：DeepSeek 没有可直接调用的搜索接口，
// 搜索只存在于其 Anthropic 兼容 messages 接口内部(web_search_20250305 server
// tool)，因此每次搜索会消耗一次模型调用，且搜索请求由 DeepSeek 服务端发出——
// 不经过本机 Proxy，也不会进流量留痕。
type WebSearchOpts struct {
	Enabled   bool
	Backend   string
	BraveKey  string
	TavilyKey string
	// Proxy 仅用于显式配置独立搜索代理；留空时由 ProxyFunc / FallbackProxyFunc 动态取平台出口代理。
	Proxy string
	// ProxyFunc resolves the egress proxy per search call for ddgs/brave-html when Proxy is empty,
	// so long-running sessions follow proxy-pool failovers automatically.
	ProxyFunc func() string
	// FallbackProxyFunc resolves the egress proxy per search call when any primary backend
	// falls back to brave-html or ddgs and Proxy is empty (fail-closed, never direct when pool exists).
	FallbackProxyFunc func() string
	SearxngURL        string
	DeepSeekBaseURL   string
	DeepSeekAPIKey    string
	DeepSeekModel     string
	DeepSeekFormat    string
	// CommandCodeBaseURL / CommandCodeAPIKey 配置 commandcode 的**原生**搜索通道
	// （/alpha/web-search）：与模型无关、与 LLM 配额解耦——它的 /messages 只服务
	// Claude 系、deepseek 只在 /responses 上且拒收内置 web_search 工具。
	CommandCodeBaseURL string
	CommandCodeAPIKey  string
	// Headers 是这次搜索要额外带的 HTTP 头（可空）：有些中转端点强制要求路由头
	// （如 opencode zen 的 x-opencode-session），缺了直接 400。由宿主按配置决定。
	Headers map[string]string
	// BypassCache forces a live network probe (used by the settings "test search" endpoint).
	BypassCache bool
	// DiagnosticError carries a server-side validation error (e.g. DeepSeek profile format
	// mismatch) so diagnostics fail explicitly instead of hitting an incompatible endpoint.
	DiagnosticError string
}

type Worker struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	workDir         string
	proxyAddr       string
	proxyCACert     string                  // recording proxy's CA cert path (for WebFetch HTTPS verify)
	proxyFn         func() (string, string) // dynamic proxy resolver (proxy-pool failover); nil = static
	proxyFailureFn  func(proxyURL string, err error)
	webSearch       WebSearchOpts        // web_search tool backend selection (off by default)
	webSearchFn     func() WebSearchOpts // optional dynamic resolver (hot settings reload without rebuild)
	tx              *transcript.Store    // raw LLM conversation persistence (nil = off)
	window          int                  // context window in tokens (for compaction)
	windowFn        func() int           // optional dynamic task-chain minimum
	maxTurns        int                  // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools  []actool.CoreTool
	notifyFact  func(kind string, intentID, factID int64, summary string, assetIDs []int64)
	notifyAsset func(summary string)
	// injectConstraints resolves whether this task's operation constraints get
	// injected into the worker system prompt. Read per run so the settings toggle
	// takes effect without rebuilding the agent. nil = inject (default).
	injectConstraints func() bool
	// nonStreamingFn resolves whether this run uses the non-streaming (Complete)
	// path. Read per run so a profile/task toggle takes effect without rebuilding
	// the agent. nil = streaming (default).
	nonStreamingFn func() bool
	// execService（P13）由 host 注入：长任务执行的托管后端（进程活过本次 run）。
	// rotateEgress 由 host 注入：WAF 封禁出口时切换 Clash 节点。
	rotateEgress func(reason string) (string, error)

	execService ExecService
	// noaEnabledFn resolves whether this run uses the experimental noa context-
	// compression mechanism. Read per run, like nonStreaming. nil = off (built-in
	// compaction).
	noaEnabledFn func() bool
	// maxTokensFn resolves the per-reply output cap in tokens, on the same
	// per-run basis. nil or 0 = send no cap and let the endpoint decide.
	maxTokensFn func() int
	// compactionLLM optionally supplies an independent provider for context
	// summarization. nil = use this agent's provider.
	compactionLLM func() (CompactionLLM, bool)
	cacheOpts     cacheOptionResolvers

	// overviewCache freezes the rendered global worker overview briefly per wave
	// so concurrent workers waking up together share identical Turn-1 prefixes.
	overviewMu    sync.Mutex
	overviewCache map[int64]*workerOverviewEntry
}

const workerOverviewTTL = 45 * time.Second

type workerOverviewEntry struct {
	epoch    string
	at       time.Time
	rendered string
}

// WorkerPromptCacheKey returns a task-scoped shared cache key across worker
// runs so concurrent/sequential per-intent workers hit the same prefix cache
// bucket and sticky routing affinity.
func WorkerPromptCacheKey(explorationID int64) string {
	return fmt.Sprintf("exp%d-worker", explorationID)
}

// WorkerSessionID is the legacy per-intent transcript key. New runs use
// WorkerSessionIDForSlot so one slot transcript spans intents; this helper stays
// for reading/settling transcripts created before the slot migration.
func WorkerSessionID(explorationID, intentID int64) string {
	return fmt.Sprintf("exp%d-worker-i%d", explorationID, intentID)
}

// WorkerSessionIDForSlot is the persistent transcript key for a worker slot. The
// slot is reused across intents so the transcript (and provider prefix cache) can
// accumulate instead of starting cold for every intent. Keep the exp<exp>- prefix
// (task archive/delete scans it) and the "worker" lane (llmrec.parseSession).
func WorkerSessionIDForSlot(explorationID int64, slot int) string {
	return fmt.Sprintf("exp%d-worker-slot%d", explorationID, slot)
}

// workerResumeSlot returns the slot whose transcript should be resumed after a
// restart. Only intents explicitly marked by ResetRunningIntents qualify.
func workerResumeSlot(payload []byte) int {
	var meta struct {
		ResumeAfterRestart bool `json:"resume_after_restart"`
		ResumeSlot         int  `json:"resume_slot"`
	}
	if json.Unmarshal(payload, &meta) == nil && meta.ResumeAfterRestart && meta.ResumeSlot > 0 {
		return meta.ResumeSlot
	}
	return 0
}

func workerSlotNumber(name string) int {
	if !strings.HasPrefix(name, "work#") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(name, "work#"))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

const workerIntentMarkerPrefix = "<!-- BLADE_WORKER_INTENT:"

func workerIntentMarker(intentID int64) string {
	return workerIntentMarkerPrefix + strconv.FormatInt(intentID, 10) + " -->"
}

// workerIntentIDIn 从文本里解析当前意图 marker 的 id。
//
// marker 可能位于消息任意位置（启动轮是「态势 + 意图块」，见 messagesSinceWorkerIntent），
// 所以不能用前缀匹配；也不能只比前缀——两条不同意图的 marker 前缀相同，必须解析出完整 id。
func workerIntentIDIn(text string) (int64, bool) {
	start := strings.Index(text, workerIntentMarkerPrefix)
	if start < 0 {
		return 0, false
	}
	rest := text[start+len(workerIntentMarkerPrefix):]
	end := strings.Index(rest, " -->")
	if end < 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func hasWorkerIntentMessage(messages []llm.Message, intentID int64) bool {
	for _, message := range messages {
		if message.Role != llm.RoleUser {
			continue
		}
		if id, ok := workerIntentIDIn(message.Text()); ok && id == intentID {
			return true
		}
	}
	return false
}

// lastWorkerIntentMessageIndex returns the index of the latest current-intent
// marker, or -1. Used to bound approval/audit context to the current intent when
// a slot transcript contains older intents.
func lastWorkerIntentMessageIndex(messages []llm.Message) int {
	last := -1
	for i, message := range messages {
		if message.Role != llm.RoleUser {
			continue
		}
		if _, ok := workerIntentIDIn(message.Text()); ok {
			last = i
		}
	}
	return last
}

const workerChatMarkerPrefix = "<!-- BLADE_WORKER_CHAT:"

func workerChatMarker(requestID string) string {
	return workerChatMarkerPrefix + requestID + " -->"
}

func hasWorkerChatMessage(messages []llm.Message, requestID string) bool {
	marker := workerChatMarker(requestID)
	for _, message := range messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), marker) {
			return true
		}
	}
	return false
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default). Read per
// run so a profile or task-chain toggle takes effect without rebuilding.
func (w *Worker) SetNonStreaming(fn func() bool) { w.nonStreamingFn = fn }

// SetExecService 注入长任务执行服务（P13）；nil = 未接线（工具返回可读拒绝）。
func (w *Worker) SetExecService(svc ExecService) { w.execService = svc }

// SetRotateEgressHandler wires the Clash egress rotation callback.
func (w *Worker) SetRotateEgressHandler(fn func(reason string) (string, error)) { w.rotateEgress = fn }

func (w *Worker) nonStreaming() bool { return w.nonStreamingFn != nil && w.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (w *Worker) SetNoaEnabled(fn func() bool) { w.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (w *Worker) SetMaxTokens(fn func() int) { w.maxTokensFn = fn }

// SetCompactionLLM supplies an optional independent provider for context
// summarization. nil/false = keep using this agent's own provider.
func (w *Worker) SetCompactionLLM(fn func() (CompactionLLM, bool)) { w.compactionLLM = fn }

// SetCacheOptions wires per-run resolvers for CacheTTL, PromptCacheRetention, and PreserveThinking.
func (w *Worker) SetCacheOptions(ttlFn, retentionFn func() string, preserveThinkingFn func() bool) {
	w.cacheOpts = cacheOptionResolvers{cacheTTLFn: ttlFn, retentionFn: retentionFn, preserveThinkingFn: preserveThinkingFn}
}

func (w *Worker) maxTokens() int {
	if w.maxTokensFn == nil {
		return 0
	}
	return w.maxTokensFn()
}

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the worker system prompt. nil = inject (default).
func (w *Worker) SetConstraintInject(fn func() bool) { w.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (w *Worker) wantConstraints() bool { return w.injectConstraints == nil || w.injectConstraints() }

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "你即将因预算耗尽被终止。不要再运行任何命令/探测。请依次：(1) 把你上面已识别但还没写回的内容逐条写回——新资产用 insert_assets、探索结论/事实用 record_fact、拿到的据点用 report_foothold、读到的凭据用 report_credential、留在目标上的东西用 report_artifact、确认漏洞用 report_finding；(2) **最后单独用一句话纯文本**总结你做了什么、得到哪些关键结论（这句会作为本次运行的结果展示，务必输出）。"

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, extraTools: extra}
}

// defaultToolsExcept returns actool.DefaultTools() minus the named tools (by
// CoreTool.Name()). Used to trim SDK default tools an agent shouldn't have.
func defaultToolsExcept(exclude ...string) []actool.CoreTool {
	drop := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		drop[n] = true
	}
	all := actool.DefaultTools()
	out := make([]actool.CoreTool, 0, len(all))
	for _, t := range all {
		if !drop[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (w *Worker) SetNotifyFact(fn func(kind string, intentID, factID int64, summary string, assetIDs []int64)) {
	w.notifyFact = fn
}
func (w *Worker) SetNotifyAsset(fn func(summary string)) { w.notifyAsset = fn }

func (w *Worker) SetCompactionWindowResolver(fn func() int) { w.windowFn = fn }

func (w *Worker) compactionWindow() int {
	if w.windowFn != nil {
		return w.windowFn()
	}
	return w.window
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) {
	w.proxyAddr, w.proxyCACert, w.proxyFn = addr, caCert, nil
}

// SetProxyFunc makes the worker resolve its egress proxy at each run start, so a
// proxy-pool failover takes effect without rebuilding the agent. The fallback is
// the static SetProxy value when fn is nil.
func (w *Worker) SetProxyFunc(fn func() (string, string)) { w.proxyFn = fn }

// SetProxyFailureHook registers a host callback invoked when native HTTP tools
// hit a transport error on the active egress proxy.
func (w *Worker) SetProxyFailureHook(fn func(proxyURL string, err error)) { w.proxyFailureFn = fn }

func (w *Worker) proxySnapshot() (string, string) {
	if w.proxyFn != nil {
		return w.proxyFn()
	}
	return w.proxyAddr, w.proxyCACert
}

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.webSearch = o }

// SetWebSearchFunc wires a per-run dynamic resolver so settings/agent switches
// take effect immediately on the next worker run without rebuilding bundles.
func (w *Worker) SetWebSearchFunc(fn func() WebSearchOpts) { w.webSearchFn = fn }

// ProxyEnv exposes proxyEnv to the host: a command the host launches on the agent's
// behalf (P13 托管长任务) must get the SAME env as the agent's own Bash tool, or its
// traffic would bypass the recording MITM (no evidence binding) and fail TLS.
func ProxyEnv(proxyAddr, caCert string) []string { return proxyEnv(proxyAddr, caCert) }

// EnsureRunDir is the same convention as ensureRunDir for callers holding the task
// id in the ledger's string form (the P13 execution service): a host-launched long
// task then runs in the directory the agent's Bash tool uses, so relative artifacts
// land where the agent expects them.
func EnsureRunDir(base, taskID string, intentID int64) string {
	dir := filepath.Join(base, "tasks", taskID)
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	_ = os.MkdirAll(dir, 0o775)
	return dir
}

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the egress proxy (the recording MITM when capture is on, or the global proxy
// directly when it is off) and, only when a MITM CA is present, makes the common
// toolchain trust it — so tools need no manual -x/--proxy/-k. Each ecosystem reads
// a different CA var (verified empirically): SSL_CERT_FILE→curl/urllib/Go/openssl,
// REQUESTS_CA_BUNDLE→python requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl,
// GIT_SSL_CAINFO→git, NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+
// honor the proxy vars. ALL_PROXY is set too so a socks5 egress proxy (which curl
// only reads from ALL_PROXY, not HTTP(S)_PROXY) works in the capture-off path.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"ALL_PROXY=" + proxyAddr, "all_proxy=" + proxyAddr, // socks5 egress: curl reads only this
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert != "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// workerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the worker system
// prompt, seeded into agent_prompts. The trafficTool block and the 中间产物输出规约
// are NOT here — they are code-owned and appended by workerSystem after rendering
// (段 [B]/[C]), so editing the DB body can never drop them.
const workerDefaultTmpl = `你是一个网络安全平台授权渗透测试系统的"执行者"(work agent)。你领到【一条意图】——它描述的是**一个可解释的研究问题**（在哪个入口做什么、验证或证明什么），不是一条命令。你的职责是把这个问题研究清楚、把发现写回知识图谱、然后停止返回。

**边界（红线）**：
1. **围绕你领到的这一个研究问题就地打穿**：同一目标、同一授权范围、同一预算内，应当连续完成必要的验证与纵向递进，并主动调整局部手法（入口、参数、payload、编码、身份）；不要因为最初一条命令失败就草草收尾，更**不要在刚测出强异常、有效凭据或可利用迹象（如报错注入回显、XSS 反射点、弱口令进后台、SSTI/SSRF 苗头）时就立刻收手并改用 propose_intents 抛回排队队列重新冷启动**——趁热上下文仍在，优先就地推进到最小稳定 PoC 与实际漏洞/据点/凭据落库（report_finding / report_foothold / report_credential）。以下情况才交回规划者：**跨资产/跨主机的横向面、完全独立的另一条漏洞链、或超出当前单意图预算的长周期深度方向**——这些才用 record_fact.leads、propose_intents 或 request_scope_expansion 提交。不要把线索塞进 summary 后就不管，也不要自己擅自扩面或另起炉灶。
2. 初次受阻（payload 被过滤 / 404 / 注入无回显）不代表已探透——把本意图的所有绕过手段走完再输出结论；**批量探测与 Fuzz 先建基线**：发变体载荷前先发 2~3 次正常请求确立**基线指纹**（状态码 / 去动态字段后的稳定长度或结构 / 关键响应头 / 中位耗时）；跑完后把与基线一致的样本做签名聚合统计（如「142/150 与基线一致 [200, 3420B, 42ms]」），不得只写“测过无异常”而省略样本量与基线指纹。
3. **捕获六维弱信号（写进 record_fact.detail）**：凡偏离基线的异常即使尚不足以成洞，也须按类拆列**最小复现载荷 + 差分维度 + 证据文件路径**：① 状态码/Location 跳变、② 体量/字段结构突变、③ 稳定耗时异常、④ 响应头/Cookie 异动、⑤ 报错/指纹/路径泄露原文（禁止只写一句“返回 500”）、⑥ 输入回显/转义/截断/类型转换。
4. 只在授权范围内操作。系统提示顶部若附【操作约束】，那是最高优先级红线：每条命令/探测执行前先自检，违反即不做（哪怕它落在你领到的意图里）。
5. **何时收尾（不是越久越好）**：success_criteria 满足 / stop_condition 出现 / 合理手法已穷尽 / 同一条件重复尝试没有新信息且预算将尽——任一条成立就写回结论并停止；不要用换措辞的重复实验硬撑。

**边发现边写回**（写进图才算数，脑子/文字里的不算；每得一个结果立刻写，别攒到最后被步数耗尽丢掉）。七种写回，别串图：
- **新资产/资源 → insert_assets（资产图）**：子域 / service / endpoint / 指纹 等一切资产【本身】。**这里只登记资产；探索结论/判断不写这里，用 record_fact；据点与凭据也不写这里（见下，它们各是一等公民）。**
- **探索结论/事实 → record_fact（探索图，传 intent_id）**：都用它。**同一条结论的多个观察合并进 detail**；但【彼此独立的攻击面/假设/旁路线索】应分条记录，让图血缘、enables 与 polarity 能各自精确派生。把独立攻击面全压进一条 summary， planner 概览看不到，也会丢失后续意图的 parent 锚点。**只写增量**：只记这次【新得到】的，别把已有事实换措辞重记（只印证已有、无新增就不必记）。**summary 仅写客观观测与基线差分**（载体+样本量+基线指纹+异常分类），不把“建议/下一步/思路”写进 summary（后续方向请交 propose_intents 或 leads）；**detail 建议按可倒查骨架组织**：① 覆盖与鉴权健康度（含 429/封禁阈值）、② 基线与过滤/截断行为、③ 异常差分清单、④ 已排除范围与客观卡点、⑤ 顺带发现与落盘路径。**只写真实看到的**：给 evidence（一行：命令+最能证明的一两行输出，简洁，细节在 detail）、标 confidence（observed=直接看到 / inferred=据现象推断）。
- **拿到据点 → report_foothold（探索图，传 intent_id，evidence 与 confidence 必填）**：**你本次真实建立过、现在还能用**的通路才算（反弹 shell / SSH / webshell / 会话）。给不出可核对的一行输出（命令 + 证明通路存在的输出）会被工具拒收。**这是规划层判断「能不能从这条通路往深打」的唯一依据：不记等于没拿到**——别的 worker 不知道它存在，会把同一台机器从外面重新打一遍。权限如实标（user / root / SYSTEM），别拔高。
- **读到凭据 → report_credential（探索图，传 intent_id，evidence 与 confidence 必填）**：口令 / 哈希 / 私钥 / 令牌，**真实读到的**才记（读到它的那条命令与输出进 evidence）。记下来规划层才会派「拿它去哪试」的意图——凭据是横向与提权最直接的抓手。**自己试出来的弱口令不算读到的凭据**，那是探索结论，用 record_fact 记。
  · **顺手把 targets（能登哪）写上**：知道这条口令属于哪个服务/哪台机器时，写清 host/port/service。已经拿它真的登进去了，就在那条目标上标 verified: true 并附上登录成功的那一行输出——那比 confidence 硬，规划层据此可以直接派「用它登 X 做 Y」，而不是再派一条试探。
  · **已经记过的凭据后来登成功了 → 用 verify_credential（传凭据 id + host + evidence）**，不要再 report 一条：重复 report 会在图上留下两条一样的凭据。
- **留在目标上的东西 → report_artifact（探索图，传 intent_id，evidence 与 confidence 必填）**：webshell、上传的工具/脚本、建的账号、计划任务、改过的配置——**你留在客户系统上的东西**。它跟上面几条是反的：那些记「拿到了什么」，这条记「给客户留下了什么」。**没清掉的 webshell 就是留在客户系统上的后门**，而且交付时说不清我们动过什么。要写清 host 与 locator（文件路径/账号名/任务名），报告靠它才找得到东西。**文件类 locator 必须是目标上的绝对路径**（如 /var/www/html/upload/x.php），另用 url 写访问入口（如 http://host/upload/x.php）——绝对路径给清理，URL 给复现，报告两样都要。
  · 状态如实标：**active**=确认还在（默认）、**unknown**=落了但没确认还在不在。**别把 unknown 写成 active**。
  · **清掉之后用 mark_artifact_cleared 销账**（传 artifact_id + 清理时的那行输出）——没有那行输出，「清掉了」只是个说法，而它决定的是客户系统上还有没有后门。**只在真的清掉时用**：没清就说没清，不要销账。
  · 只是顺手记一笔，不是流程要求：没往目标上落东西就不用记。
- **检测验证 → report_detection_check（探索图，传 intent_id）**：做了某个动作、**且你能查到防御侧的日志/告警时**，记下这一手有没有被检出。三态必须分清：**triggered**=确实响了（有日志为证）、**not_triggered**=确实没响（你查过、该时段确实没有记录）、**unknown**=你没法判定（没有日志源、没权限、时段对不上）。**「unknown」不是「没响」**——把「我没看到」写成「它没拦住」，给出的结论比不给更危险。查不出所以然就老实记 unknown；**环境里根本没有日志可查时，不要为了凑数去记**。
- **遇到可识别的技术栈/CMS/中间件/登录口 → 先 lookup_playbooks（按 asset_id 或 technologies/favicon）**：打法库里可能有同类指纹目标"打穿过"的完整步骤（识别线索→验证→利用→坑）。命中后按它的验证路径快速试，**仍以实际回显为准**；用完后用 record_playbook_feedback 回报 worked/failed/inconclusive（卡片靠成功率排序，越用越准）。
- **确认漏洞 → report_finding（探索图，含 PoC，传 intent_id，confidence 必填）**：**只有你本次真实触发过、拿到可复现证据（请求/响应或命令输出）才用**。严禁把"版本/指纹匹配到 CVE""参数看起来可注入""外部漏洞库/更新日志/代码 diff 推断"当已确认，也不要用查 CVE 库或对比补丁版本替代实际触发。confidence 诚实标定：high=可复现触发链 / medium=直接观察到强异常未构造完整对照 / low=据指纹版本推断。**evidence 要能独立支撑报告**：写清目标（host:port/URL）、入口与参数、**落地文件的绝对路径与访问方式**、真实执行过的每条命令/请求及关键回显；多步利用按步骤写。只有结论没有路径/命令/回显，报告就写不出来；落地了文件同时用 report_artifact 记一条。
- **拿到域内/云上枚举结果 → ingest_ad_data / ingest_cloud_data（落进图，别只当文本）**：在域内机器上跑出 SharpHound 采集结果、或拿到云凭据后跑 aws iam get-account-authorization-details / kubectl get ... -o json，就把结果（文件路径或内联 JSON）**摄入本任务的图**——规划层据此才看得到「从这点能走到哪」（用 ad_paths 读）。**不记等于没拿到**：图是空的，下一轮就会从外面重新打一遍。摄入读的是已经到手的数据、不产生对目标的动作，因此不过 RoE/命令门。
- **动手前想知道"这一手会留下什么" → opsec_notes（传 command）**：返回**预计**的留痕位置（目标系统日志 / EDR / 网络侧 / 应用日志 / 本机痕迹）、具体信号（Windows 事件 ID、sysmon、auditd…）与预计可见度。用在挑手法、判断要不要先和蓝队/客户打招呼时。**它是预期不是实测**——实测结论仍用 report_detection_check 记；知识库没收录时会明说"没收录"，那**不代表没有日志**。
- **候选三态闭合（禁止静默丢弃）**：一个研究过的候选必须落到三态之一——① 触发成功 → report_finding；② **排除 → close_candidate(outcome=ruled_out)**，counterevidence 点名阻断它的具体控制点/机制、说明为何不在攻击者路径上（"没找到控制点 ≠ 安全"，别把"没打进去"当排除）；③ 闭合不了 → **close_candidate(outcome=open_proof_gap)**，proof_gap 写清缺什么信息、下一步怎么补。看版本/指纹有嫌疑但触发不了 → 先记 record_fact(inferred)，确证不了就 close_candidate(open_proof_gap)，别硬记成 finding，也别默默略过。


**侧向通道速记**：旁路线索 → record_fact.leads；值得规划者派单的下一步 → propose_intents（只写候选事实，不自动建意图）；范围外但疑似属于目标的资产 → request_scope_expansion（只提交归属证据，不直接打）。这些通道与主意图写回同等重要，不要因为“不是这条意图”就丢掉。

完成本意图后用一句话总结你做了什么、写回了哪些事实。`

// writebackBaselineBlock 是**始终生效**的写回自检：线索/深度开关关闭时也不能丢
// 凭据、据点、遗留物与否定结论。它只保留最必要的四句，避免把深度纪律整段带回来。
const writebackBaselineBlock = `

【写回自检（代码持有）】
- 收尾前自检：建了账号/拿到口令 → report_credential；拿到据点 → report_foothold；留下文件 → report_artifact；观察/线索 → record_fact；仅有证据支持排除的结论才标 negative，失败中的新入口或异常用 inconclusive + leads。
- 被 WAF/登录墙/限速挡住 ≠ 已排除：记 blocked 或 open_proof_gap，不要记 negative。
- 漏写等于没做——planner 看不到，下一轮只会再派一遍。`

// workerTrafficBlock is 段 [B]: the traffic-tool note, code-injected only when
// traffic capture (recording) is on — i.e. the traffic_* tools actually exist.
// Gated on recording, NOT on the egress proxy: a global proxy with capture off
// routes traffic but records nothing, so the tools would not be there. Not stored,
// not editable.
func workerTrafficBlock(recording bool) string {
	if !recording {
		return ""
	}
	return "\n\n**流量工具**：\n- traffic_search / traffic_get / traffic_blob：回看响应、找已访问过的资源，**先查流量、不要重复 curl 同一 URL**。traffic_search **必须指定 host**、默认只回 3 条极轻量索引(id/method/url/status/resp_len，无响应内容)，需要更多显式调大 limit；可用 body_contains 在请求/响应正文里做全文搜索(至少 3 字符，支持子串和中文，如找密码/密钥/报错/内网地址)；要看某条原文用 traffic_get(id)，其中超大正文显示为 @blob sha256:<hash>，用 traffic_blob(hash) 分段取全文。"
}

// artifactSpec is 段 [C]: the code-owned, non-editable tail appended to every
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(dir string) string {
	return "\n\n**中间产物输出规约**：脚本、payload、抓到的响应体、临时数据等一切中间产物，**一律写到本任务工作目录 " + dir + "**（相对路径即写在这里，也可用该绝对路径）——**不要写 /tmp、不要用其它绝对路径**。"
}

// workerArtifactSpec is the worker's 段 [C]: its per-intent run dir is pre-created
// by the engine (ensureRunDir), so it just writes relative paths there — no manual
// mkdir, no cross-worker name collisions.
func workerArtifactSpec(runDir string) string {
	return "\n\n**中间产物输出规约**：脚本、payload、抓到的响应体、临时数据等一切中间产物，**一律写到本次意图的专属工作目录 " + runDir + "**（已自动建好，直接用相对路径写在这里即可，无需再手动建目录）——**不要写 /tmp、不要用其它绝对路径**。"
}

// ensureRunDir builds and creates an agent's working directory under base:
// <base>/tasks/<taskID> for planner/main; <base>/tasks/<taskID>/i<intentID> for a
// worker (intentID<=0 → task dir only). The "tasks/" segment groups per-task dirs
// symmetrically with the chat agent's "sessions/<sessionID>". Best-effort mkdir — on
// failure, writes fail the same way an unwritable CWD would.
func ensureRunDir(base string, taskID, intentID int64) string {
	dir := filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10))
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	// 0775 而非 0755：agent 降权模式（server/agent_isolation.go）下命令以 worker 用户
	// 运行，目录由服务端（root）创建，组可写才让 worker 写进自己的产物目录。
	_ = os.MkdirAll(dir, 0o775)
	return dir
}

// cmdOutDir is the SDK large-tool-output spill dir under an agent's run dir.
func cmdOutDir(dir string) string { return filepath.Join(dir, "cmd-output") }

// taskOutDir is where the SDK's background tasks (Bash 的后台化、TaskOutput 等) 落盘。
// 放在本次运行目录下（而不是 SDK 默认的 $TMPDIR/norma）：与 cmd-output / 执行台账 /
// 压缩落盘同根，任务删除时的文件清理、备份、人工排查都只面对一个根，$TMPDIR 既不随
// 宿主生命周期、也可能挂在 tmpfs 上（重启即失）。
func taskOutDir(dir string) string { return filepath.Join(dir, "task-output") }

// perIntentWorkerSession 是 worker 会话粒度的 A/B 开关：每个意图一条独立会话
// （ARTEX 的做法），而不是按槽位跨意图复用历史。默认每意图隔离；显式关闭可恢复槽位复用。既有 logical session 仍保留以便续跑。
func perIntentWorkerSession() bool {
	return db.PerIntentWorkerSession("")
}

func workerSystem(proxyAddr, caCert, dataDir string) string {
	body := renderSystem("worker", workerDefaultTmpl, WorkerVars{ProxyAddr: proxyAddr, DataDir: dataDir, Now: nowStr()})
	// caCert is present only when the recording MITM is on, which is exactly when
	// the traffic_* tools are registered — so it gates the traffic-tool note.
	// Optional finding guidance is added for every role after tool resolution.
	// Per-intent content (intent payload/assets/runDir/start) deliberately lives
	// outside this stable string so a reused slot can keep the same cached prefix.
	return body + workerTrafficBlock(caCert != "") + workerExecSpec() + cveToolBlock + footprintWorkerBlock() + egressDisciplineBlock(true) +
		writebackBaselineBlock + untrustedDataBlock()
}

// renderWorkerIntentTurn is the per-run user message for the claimed intent. It is
// deliberately NOT in the system prompt so a reused slot keeps the same cached
// prefix. When running in the default per-intent isolated session, cross-intent
// history warnings are omitted.
func renderWorkerIntentTurn(intent *db.Node, assetsJSON, runDir string, slot int) string {
	slotShared := !perIntentWorkerSession() || (intent != nil && strings.Contains(intent.LogicalSessionID, "-slot"))
	return renderWorkerIntentTurnWithMode(intent, assetsJSON, runDir, slot, slotShared)
}

func renderWorkerIntentTurnWithMode(intent *db.Node, assetsJSON, runDir string, slot int, slotShared bool) string {
	var b strings.Builder
	b.WriteString(workerIntentMarker(intent.ID))
	b.WriteString("\n【当前意图（本次唯一任务：聚焦这一条，及时写回发现、战果或旁路线索，做完即停）】\n")
	if slotShared {
		b.WriteString("以下历史对话属于此前已经结束的意图，仅作背景参考；不要继续旧目标，也不要把旧 intent id/旧工作目录用于本条写回。\n")
	}
	b.WriteString(SanitizeUntrusted(string(intent.Payload)))
	// Planner observations are research notes, not evidence. The full payload is
	// already visible above; this label prevents a worker from treating a
	// read-only glance as an observed fact it can just copy into record_fact.
	var plannerNote struct {
		Observation string `json:"planner_observation"`
	}
	if json.Unmarshal(intent.Payload, &plannerNote) == nil && strings.TrimSpace(plannerNote.Observation) != "" {
		b.WriteString("\n【规划者观察（待验证，不是事实/证据）】" + clipLine(plannerNote.Observation, 500) +
			"\n请用你自己的实测证实或推翻它；不要把它直接当结论写回，也不要引用为证据。")
	}
	// P0 契约：worker 看得到完整 payload，但契约里的 produces/acceptance 决定
	// “什么算做完”，单独提一句，避免被长 payload 淹没。
	var contractWrap struct {
		Contract *intentContract `json:"contract"`
	}
	if json.Unmarshal(intent.Payload, &contractWrap) == nil && contractWrap.Contract != nil {
		var parts []string
		if len(contractWrap.Contract.Produces) > 0 {
			parts = append(parts, "约定产物："+strings.Join(contractWrap.Contract.Produces, "、"))
		}
		if acc := strings.TrimSpace(contractWrap.Contract.Acceptance); acc != "" {
			parts = append(parts, "验收："+clipLine(acc, 300))
		}
		if len(parts) > 0 {
			b.WriteString("\n【意图契约（P0）】" + strings.Join(parts, "；") +
				"。完成前逐项对照；未满足的产物写清限制，不要宣称完成。")
		}
	}
	b.WriteString(fmt.Sprintf("\n意图 id: %d（record_fact / report_finding 等写回时传它）", intent.ID))
	if slot > 0 {
		if slotShared {
			b.WriteString(fmt.Sprintf("\n执行槽位: Worker #%d（本槽位会话跨意图复用）", slot))
		} else {
			b.WriteString(fmt.Sprintf("\n执行槽位: Worker #%d（本意图独立会话）", slot))
		}
	} else {
		b.WriteString(fmt.Sprintf("\n执行身份: 独立 Worker #%d（本意图专属独立会话）", intent.ID))
	}
	if strings.TrimSpace(assetsJSON) != "" {
		b.WriteString("\n本意图 asset_ids 对应的目标资产：\n" + SanitizeUntrusted(assetsJSON))
	}
	b.WriteString(workerArtifactSpec(runDir))
	b.WriteString("\n\n开始执行当前意图：聚焦这一条，及时写回事实、资产、发现、战果或旁路线索，做完即停。")
	return b.String()
}

// ensureSlotDir is the stable per-slot directory for compaction/noa artifacts. It
// is intentionally separate from the per-intent runDir: the transcript and its
// summaries outlive one intent, so their archives should too.
func ensureSlotDir(base string, taskID int64, slot int) string {
	dir := filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10), "worker-slot"+strconv.Itoa(slot))
	_ = os.MkdirAll(dir, 0o775)
	return dir
}

// workerExecSpec is 段 [D]: the code-owned long-task contract (P13). Appended the
// same way as 段 [B]/[C] — the DB-editable body cannot remove it. It fixes the two
// semantics the model must not get wrong: 超时是观察不是失败, and 还有执行在跑就不能
// 声称这条意图探完了 (the platform will hold the intent open anyway).
func workerExecSpec() string {
	return "\n\n**长任务托管（run_execution）**：\n" +
		"- 预计超过 2 分钟的任务（nuclei/ffuf/nmap 等扫描器、目录/子域/参数枚举、字典跑批、大批量请求）**一律用 run_execution 起**：" +
		"它把进程托管在独立会话组里，**活过你这次运行**——你收笔、被暂停、换模型都不会杀掉它；输出实时落盘（返回里给出绝对路径），状态记进执行台账。几秒级的短命令仍直接用 Bash。\n" +
		"- 起来之后用 wait_execution 有界等待（默认 60 秒，可给 timeout_ms 调大）：**超时是观察不是失败**——它返回当下的 running 快照和部分输出，你可以先去干别的、过一会儿再 get_execution / wait_execution 看进展。\n" +
		"- **只要这条意图名下还有执行在跑（running/queued），就不能声称这条意图探完了**：平台不会让它收尾，执行落终态后这条意图会自动退回待领，你会在新的运行里读到完整输出并把结论写回。\n" +
		"- 要收掉一条执行用 cancel_execution 并写明理由（理由记进台账，会回传给模型和审计）；不要用 kill/pkill 直接杀进程——那会绕过台账。"
}

// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

func renderWorkerRunningPeers(ts *db.ExplorationStore, selfIntentID int64) string {
	if ts == nil {
		return ""
	}
	running, err := ts.ListByKindState(db.KindIntent, "running", 8)
	if err != nil || len(running) == 0 {
		return ""
	}
	lines := make([]string, 0, len(running))
	for _, n := range running {
		if n == nil || n.ID == selfIntentID {
			continue
		}
		var p map[string]any
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		summary := clipLine(SanitizeUntrusted(asText(p["summary"])), 100)
		if summary == "" {
			continue
		}
		line := fmt.Sprintf("  · #%d %s", n.ID, summary)
		if hyp := clipLine(SanitizeUntrusted(asText(p["hypothesis"])), 80); hyp != "" {
			line += " | hypothesis=" + hyp
		}
		if ids := intentAssetIDs(n); len(ids) > 0 {
			line += fmt.Sprintf(" (asset_ids=%v)", ids)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return "【当前并发在跑的同伴意图（实时参考，勿重复探测同入口）】\n" + strings.Join(lines, "\n")
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// launch USER message for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
func renderWorkerGraphOverview(data map[string]any) string {
	// coverage 是给规划者判断「哪类测得少 / 要不要扩范围」的信号，与 worker
	// 「只做领到的那条意图、别追未覆盖的点」的职责边界相悖 → 从 worker 视图里剔除。
	// data 是本次 worker 专属的新 map，删键不影响 planner。
	delete(data, "coverage")
	b, err := json.Marshal(SanitizeJSONStrings(data))
	if err != nil {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n【全局探索态势（只读，帮你把自己这条意图放进大局看）】：\n" +
		"下面是整个任务当前的探索概况。用途有两个：一是知道别人已发现什么，别重复；二是让你探自己这条意图时，能联想到它和全局的关系。\n" +
		"**发散是好事**：探本意图时尽管深想、多联想。唯一的界线是——别真的动手去执行别的意图（那是别的 worker 的事，由规划者调度）。但凡你联想到有价值的线索（跨资产的联动、疑似另一条利用链的入口、全局层面的可疑点），**务必写进 fact 交规划者**——这是你重要的产出，不是可有可无。宁可多报一条让规划者判断，也别自己咽下去。\n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// notifyFinding, if non-nil, is called (intentID, summary) when this worker writes
// a finding (report_finding) so the task's planner wakes mid-flight — with context
// on which intent found what — instead of waiting for the worker to finish.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string)) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, "", "")
}

// ExecuteWithMessage runs the next turn in the same intent conversation with a
// human-authored message. The HTTP handler does not edit the transcript;
// agentcore records the message as a normal user turn when this Worker starts.
// This keeps Worker continuation identical to the regular agent chat flow.
func (w *Worker) ExecuteWithMessage(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, strings.TrimSpace(requestID), strings.TrimSpace(message))
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	// Resolve egress per run: with capture off this follows the proxy pool's
	// active candidate; with capture on it is the local MITM (upstream switches
	// inside the MITM). Never read the static fields directly below.
	if intent.ExecutionGeneration == 0 {
		current, err := ts.ClaimNode(ctx, intent.ID, name, "open", db.IntentLeaseTTL, nil, false)
		if err != nil {
			return "", WriteCounts{}, err
		}
		if current == nil {
			return "", WriteCounts{}, db.ErrStaleExecution
		}
		intent = current
	}
	authority := intent.WriteAuthority(taskID, ts.ID())
	ts = ts.WithAuthority(authority)
	if as != nil {
		as = as.WithAuthority(authority)
	}
	proxyAddr, proxyCA := w.proxySnapshot()
	tsx := NewToolSet(ts, name)
	defer tsx.CloseHTTP()
	tsx.SetEgressProxyFunc(w.proxySnapshot)
	tsx.SetProxyFailureHook(w.proxyFailureFn)
	tsx.SetFindingRecorder(w.findingRecorder)
	tsx.SetTaskID(taskID)
	coverageEnabled := as == nil || as.CoverageEnabled(taskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetWriteAuthority(authority)
	tsx.SetOwnerIntent(intent.ID)              // writes anchor to this intent and reject stale intent_id
	tsx.SetExecService(w.execService)          // P13：长任务执行（进程活过本次 run）
	tsx.SetRotateEgressHandler(w.rotateEgress) // WAF 封禁出口时切换 Clash 节点
	tsx.SetEnrich(enr)                         // async DNS/HTTP auto-completion for assets this worker writes
	tsx.SetNotifyFinding(notifyFinding)        // report_finding 落库时当场唤醒 planner，带上「哪个意图+finding」
	tsx.SetNotifyFact(w.notifyFact)            // 事实/矛盾/否定写回 → 语义触发
	tsx.SetNotifyAsset(w.notifyAsset)          // 新资产登记 → 语义触发
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, Bash is hidden via Settlement.DisabledTools (no local gating needed).
	base := append(tsx.WorkerTools(), w.extraTools...)
	// worker 刻意不给 MultiEdit/Glob/Grep：文件精改用 Edit、检索走 Bash(grep/find)，
	// 收敛工具面、减少低价值调用。其余 SDK 默认工具(Read/Write/Edit/LS/Bash/Sleep)照常。
	base = append(base, defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), IntentID: intent.ID, LogicalSessionID: intent.LogicalSessionID, ExecutionGeneration: intent.ExecutionGeneration, ExecutionOwner: intent.Owner})
	tools, def, cleanup := AugmentTools(ctx, "worker", base)
	defer cleanup()
	mergeDeferred(&def, deferWorkerTools(tools))
	tools = append(tools, newBLADEWebFetch(w.proxySnapshot, proxyCA, taskID))
	ws := resolveWebSearchOpts(w.webSearch, w.webSearchFn)
	if ws.Enabled {
		tools = append(tools, NewWebSearchToolWithResolver(func() WebSearchOpts {
			return resolveWebSearchOpts(w.webSearch, w.webSearchFn)
		}))
	}

	// 意图是 worker 的【唯一职责、贯穿整个 run 的不变量】→ 连同启动指令、意图锚定的目标资产
	// 原始数据一起放进 system prompt：system 每次 run 都重新拼一遍、绝不会被 compaction 压掉，
	// 长 run 里意图永远在场，续跑时也不依赖 transcript 历史是否留住那条首消息。代价是 system
	// 混入 per-intent 易变数据、失去跨意图缓存复用；这是刻意的取舍（意图丢失比省 token 严重得多）。
	// 与 planner「态势块放 user turn」分叉是有意的：planner 本身是产意图的那个、没有单一 mandate，
	// worker 有。仅【全局态势 overview】留在启动 user 消息里——它可降级、容忍 stale，压掉无碍。
	// 本次意图的专属工作目录 <workDir>/tasks/<taskID>/i<intentID>，引擎侧先建好。
	runDir := ensureRunDir(w.workDir, taskID, intent.ID)
	tsx.SetRunDir(runDir)
	tsx.SetTaskRoot(filepath.Join(w.workDir, "tasks", strconv.FormatInt(taskID, 10)))
	// 槽位会话：worker 名字即 work#N。任务内的所有意图都在同一槽位会话里顺序执行，
	// transcript/system 前缀因此可以跨意图复用（缓存命中的前提）。
	slot := workerSlotNumber(name)
	sessionID := WorkerSessionID(ts.ID(), intent.ID)
	ctxDir := runDir
	// A restart may move the resumed intent to a different slot (worker count
	// changed) or the same slot may now prefer a newer queued intent. The DB
	// marks the actually-interrupted intent with resume_after_restart/resume_slot;
	// resume THAT slot's transcript even when this worker's slot number differs.
	resumeSlot := workerResumeSlot(intent.Payload)
	if slot > 0 {
		if perIntentWorkerSession() {
			// A/B 开关：每意图独立会话（ARTEX 的做法）。槽位复用的历史会把上一条
			// 意图的结论带进当前意图，实测容易让 worker 把"以前查过"当"这次不用查"。
			sessionID = WorkerSessionID(ts.ID(), intent.ID)
			ctxDir = filepath.Join(runDir, "session")
		} else if resumeSlot > 0 {
			sessionID = WorkerSessionIDForSlot(ts.ID(), resumeSlot)
			ctxDir = ensureSlotDir(w.workDir, taskID, resumeSlot)
		} else {
			sessionID = WorkerSessionIDForSlot(ts.ID(), slot)
			ctxDir = ensureSlotDir(w.workDir, taskID, slot)
		}
	}
	if intent.LogicalSessionID != "" {
		sessionID = intent.LogicalSessionID
		if at := strings.LastIndex(sessionID, "-slot"); at >= 0 {
			if originalSlot, err := strconv.Atoi(sessionID[at+5:]); err == nil && originalSlot > 0 {
				ctxDir = ensureSlotDir(w.workDir, taskID, originalSlot)
			}
		} else {
			ctxDir = filepath.Join(runDir, "session")
		}
	}
	// The run-wide intent is not the current tool action. Do not forward it or
	// inherit a parent run's background into the action reviewer.
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	overview := w.cachedWorkerOverview(ts, coverageEnabled, func() string {
		return renderWorkerGraphOverview(tsx.graphOverviewDataForWorker())
	})
	// system 只放跨意图不变的内容；意图/资产/runDir 放进本次启动的 user turn，
	// 否则同一槽位每条意图都会改写 system 前缀，provider 的前缀缓存直接失效。
	sysBody := workerSystem(proxyAddr, proxyCA, w.workDir)
	var constraintTail string
	if w.wantConstraints() {
		constraintTail = constraintBlock(ts) // 操作约束置于静态 AlwaysSkills/Capability/Deferred 段之后，保证跨任务静态前缀完整命中缓存
	}
	assetsJSON := ""
	if as != nil {
		if ids := intentAssetIDs(intent); len(ids) > 0 {
			if assets, err := as.GetByIDs(ids); err == nil && len(assets) > 0 {
				if b, err := json.Marshal(assets); err == nil {
					assetsJSON = string(b)
				}
				// 意图明确针对的这些资产 → 自动纳入任务测试范围（与 insertAssets 同一套
				// 保守粒度）。upsertTaskScope 的 ON CONFLICT DO NOTHING + uq_task_scope
				// 唯一索引保证不会重复添加；重跑/重试同样是幂等 no-op。
				// 资产覆盖度功能关闭时不再累积测试范围(分母)。
				if coverageEnabled {
					for _, a := range assets {
						if err := as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP); err != nil {
							log.Printf("[worker] 自动入范围失败 task=%d type=%s: %v（范围边界可能少一条）", taskID, a.Type, err)
						}
					}
				}
			}
		}
	}
	system, boundary := deferredSystemWithDynamic(sysBody, def, constraintTail)
	// 任务级 deadline(经 ctx 注入)夹逼本 run 的墙钟预算 + 决定收尾词(见 taskclock.go)。
	tc := taskClockFrom(ctx)
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
	// 收尾阶段保留完整 tool schemas（KeepToolSchemas=true）以免剥工具打穿 KV 缓存；
	// 改在执行入口拦截 Bash / run_execution，继续保留 wait/get/cancel_execution。
	settle := wrapupSettlement("worker", []string{"Bash", "run_execution"})
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("worker", []string{"Bash", "run_execution"}, clamped)
	}
	opts := agentcore.Options{
		Provider:                w.prov,
		SystemPrompt:            system,
		DynamicBoundary:         boundary,
		Tools:                   tools,
		DeferredTools:           def.Deferred,
		UnlockSet:               def.Unlock,
		PermissionMode:          permission.ModeBypass,
		OmitCurrentDateReminder: true,
		PromptCacheKey:          WorkerPromptCacheKey(ts.ID()),
		EnableWebFetch:          false, // BLADE-owned WebFetch（带任务拟真 UA + 记录代理 + CA 校验）挂在 Tools 里
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立的出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:          false, // BLADE-owned web_search（含 Brave HTML 回退）挂在 Tools 里
		WebSearchBackend:         ws.Backend,
		BraveSearchAPIKey:        ws.BraveKey,
		TavilySearchAPIKey:       ws.TavilyKey,
		DeepSeekSearchBaseURL:    ws.DeepSeekBaseURL,
		DeepSeekSearchHeaders:    ws.Headers,
		CommandCodeSearchBaseURL: ws.CommandCodeBaseURL,
		CommandCodeSearchAPIKey:  ws.CommandCodeAPIKey,
		DeepSeekSearchAPIKey:     ws.DeepSeekAPIKey,
		DeepSeekSearchModel:      ws.DeepSeekModel,
		WebSearchProxy:           ws.Proxy,
		// Bash 子命令的 HTTP 默认走记录代理 + 信任其 CA（工具无需 -x/-k）。
		// 每次工具调用现取：出口在 run 中途恢复/切换时，下一条命令立刻用上新值，
		// 而不是把启动瞬间的快照（可能是 fail-closed 的黑洞）冻结一整轮。
		BashEnvFunc: func() []string {
			addr, ca := w.proxySnapshot()
			taskRoot := filepath.Join(w.workDir, "tasks", strconv.FormatInt(taskID, 10))
			return append(proxyEnv(addr, ca), TaskSubprocessEnv(taskRoot, taskID)...)
		},
		WorkingDir: runDir,
		MaxTurns:   w.maxTurns, // 0 = unlimited (configurable in agent management)
		// 墙钟预算,轮边界判,不打断半路;0 = 不限。有任务级 deadline 时夹逼到 min(自身预算,
		// 距 deadline 剩余),让本 run 在任务到点时自然进收尾(见 taskclock.go)。
		MaxDuration: maxDur,
		// 命中预算(轮次 OR 时长)→ SDK 跑一轮收尾(隐藏 Bash),把已识别的写回,避免烂尾。
		// clamped(被任务 deadline 夹逼)时用 PromptByReason:因超时=任务到点→任务超时词,
		// 因步数=夹逼窗口内步数先耗尽→回落 per-run 词。非 clamped 维持纯 per-run。
		Settlement: settle,
		// large tool output spills to cmd-output/ with a head + pointer (SDK tool.Capture);
		// full output preserved on disk. 截断上限用 SDK 默认(30000 字符)。
		ToolOutputDir: cmdOutDir(runDir),
		TaskOutputDir: taskOutDir(runDir),
		Todos:         actool.NewTodoStore(), // 会话级临时待办（TodoWrite），纯规划用，退出即丢
		NonStreaming:  w.nonStreaming(),      // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:     w.maxTokens(),         // 0 = 不发上限,由服务端默认值决定
	}
	w.cacheOpts.apply(&opts)
	if hooks != nil { // typed-nil guard: only set when concrete (avoids harness panic)
		opts.Hooks = hooks
	}
	if w.tx != nil { // persist raw LLM conversation; one file per worker slot (reused across intents)
		opts.Transcript = w.tx
		opts.SessionID = sessionID
	}
	intentID := intent.ID
	emitWrap := func(r db.Activity) {
		if emit != nil {
			r.NodeID, r.Worker = &intentID, name
			emit(r)
		}
	}
	// 启动 user 消息 = 可降级的全局态势 overview + 当前意图块（带 marker）。
	// 仅在槽位复用模式下会跨意图携带历史对话；按实际 sessionID 渲染对应提示。
	intentTurn := renderWorkerIntentTurnWithMode(intent, assetsJSON, runDir, slot, strings.Contains(sessionID, "-slot"))
	if TaskMobileInfo != nil {
		if mobSummary, hasApp, hasDev := TaskMobileInfo(taskID); hasApp || hasDev {
			intentTurn += mobileWorkerContextBlock(mobSummary, hasDev)
		}
	}
	if brief := tsx.playbookBriefForIntent(intent); brief != "" {
		intentTurn += brief
	}
	input := overview
	if strings.TrimSpace(input) == "" {
		input = "开始执行当前意图：聚焦这一条，及时写回事实、资产、发现、战果或旁路线索，做完即停。"
	}
	input += "\n\n" + intentTurn
	if peers := renderWorkerRunningPeers(ts, intent.ID); peers != "" {
		input += "\n\n" + peers
	}

	// 实验功能:开启后由 noa 接管上下文压缩(归档集中在 <workDir>/noa/<SessionID> 下,持久)。
	win := w.compactionWindow()
	noaSt := enableNoa(&opts, w.noaEnabledFn, w.workDir, sessionID, win, noaWarn(sessionID))
	// P14 领域上下文：领域化摘要（7 段 + 原文落盘回读）+ 每请求附常驻索引（目标/约束/
	// 活跃方向/发现一行式）。noa 开启时仅取代 7 段摘要器，外层 indexView 常驻索引仍保留。
	installClock := taskClockFrom(ctx)
	var compactionOverride []CompactionLLM
	if w.compactionLLM != nil {
		if c, ok := w.compactionLLM(); ok {
			compactionOverride = append(compactionOverride, c)
		}
	}
	InstallContextManagerDynamic(&opts, win, w.maxTokens(), ctxDir, func(compacted bool) string {
		indexSlot := slot
		if !strings.Contains(sessionID, "-slot") {
			indexSlot = 0
		}
		return BuildContextIndex(ContextIndexVars{Store: ts, Intent: intent, IntentAssets: assetsJSON, RunDir: runDir, Slot: indexSlot, Clock: installClock, CompanyLines: tsx.companyKnowledgeLines(), PlaybookLines: tsx.playbookLines(), CVELines: tsx.cveLines(), Uncompacted: !compacted, StabilizeClock: true})
	}, compactionOverride...)
	attachNoaState(opts.Compactor, noaSt)
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)

	// Resume prior conversation if this intent was paused/blocked/exhausted and is
	// being re-run. The transcript ID is deterministic per intent, so if a prior
	// session exists the worker continues from where it left off instead of
	// restarting from scratch.
	alreadyRecorded := false
	hasIntentTurn := false
	if w.tx != nil {
		_ = s.Resume(sessionID)
		alreadyRecorded = requestID != "" && hasWorkerChatMessage(s.Messages(), requestID)
		hasIntentTurn = hasWorkerIntentMessage(s.Messages(), intent.ID)
		if len(s.Messages()) > 0 {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
		}
		if hasIntentTurn {
			// 同一意图重跑：transcript 里已经有这条意图（含其卡点与工具历史），
			// 不再重复注入整块 overview + 意图正文。人工 message 只追加增量。
			input = fmt.Sprintf("继续执行当前意图 #%d。只做它、只产生事实、assets、finding、做完即停。", intent.ID)
			if message != "" && !alreadyRecorded {
				input += "\n\n" + workerChatMarker(requestID) + "\n【人工对话输入的新意图】\n" + message +
					"\n\n请立即按这条人工输入执行，完成后再根据上下文决定原任务是否需要继续。"
			}
		}
	}
	if message != "" && !hasIntentTurn {
		if alreadyRecorded {
			input = fmt.Sprintf("继续执行当前意图 #%d；上一次人工对话输入已记录，不要重复已经完成的动作。", intent.ID)
		} else {
			// 首次注入：intentTurn 随 overview 一起进入当前轮。
			input += "\n\n" + workerChatMarker(requestID) + "\n【人工对话输入的新意图】\n" + message +
				"\n\n请立即按这条人工输入执行，完成后再根据上下文决定原任务是否需要继续。"
		}
	}

	// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
	// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
	// MaxDuration now interrupts an in-flight tool at the wall-clock deadline and
	// enters the wrap-up phase on the live ctx, so a run whose tool overran the budget
	// still settles (no external hard-timeout backstop needed). ctx itself carries only
	// pause / planner kill / shutdown, which the engine distinguishes and re-queues/stops.
	_, reason, err := captureRunSession(ctx, s, input, emitWrap)
	return reason, tsx.Writes(), err
}

const workerOverviewMaxTasks = 64

func (w *Worker) cachedWorkerOverview(ts *db.ExplorationStore, coverageEnabled bool, build func() string) string {
	if w == nil || ts == nil {
		return build()
	}
	expID := ts.ID()
	epoch := workerOverviewEpoch(ts, coverageEnabled)
	now := time.Now()
	w.overviewMu.Lock()
	defer w.overviewMu.Unlock()
	if w.overviewCache == nil {
		w.overviewCache = make(map[int64]*workerOverviewEntry)
	}
	if e := w.overviewCache[expID]; e != nil && e.epoch == epoch && now.Sub(e.at) < workerOverviewTTL {
		return e.rendered
	}
	rendered := build()
	if len(w.overviewCache) >= workerOverviewMaxTasks {
		clear(w.overviewCache)
	}
	w.overviewCache[expID] = &workerOverviewEntry{
		epoch:    epoch,
		at:       now,
		rendered: rendered,
	}
	return rendered
}

// workerOverviewEpoch hashes only slow-changing situational state (coverage flag,
// goals, active hints, facts/findings/loot counts & latest ids) while ignoring
// intent claim/running churn so concurrent workers in one wave share the same
// frozen Turn-1 overview prefix.
func workerOverviewEpoch(ts *db.ExplorationStore, coverageEnabled bool) string {
	var digest string
	_ = ts.ReadSnapshot(context.Background(), func(_ *db.DB, s *db.ExplorationStore) error {
		h := sha256.New()
		fmt.Fprintf(h, "cov=%t;", coverageEnabled)
		if stats, err := s.Stats(); err == nil {
			for _, kind := range []string{db.KindGoal, db.KindHint, db.KindFact, db.KindFinding, db.KindFoothold, db.KindCredential} {
				fmt.Fprintf(h, "%s=%d;", kind, stats[kind])
			}
		}
		if af, err := s.CountActiveFindings(); err == nil {
			fmt.Fprintf(h, "af=%d;", af)
		}
		for _, kind := range []string{db.KindFact, db.KindFinding, db.KindFoothold, db.KindCredential} {
			if latest, err := s.ListByKind(kind, 1); err == nil && len(latest) > 0 {
				fmt.Fprintf(h, "top_%s=%d:%d:%s;", kind, latest[0].ID, latest[0].ContentVersion, latest[0].State)
			}
		}
		if goals, err := s.ListByKind(db.KindGoal, 100); err == nil {
			for _, g := range goals {
				fmt.Fprintf(h, "g=%d:%s:%d;", g.ID, g.State, g.ContentVersion)
			}
		}
		if hints, err := s.ListByKindState(db.KindHint, db.StateHintActive, db.MaxActiveHints); err == nil {
			for _, ht := range hints {
				fmt.Fprintf(h, "h=%d:%s:%d;", ht.ID, ht.State, ht.ContentVersion)
			}
		}
		digest = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	return digest
}
