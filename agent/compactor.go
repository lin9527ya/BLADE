package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/compaction"
	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/db"
)

// P14 领域化上下文管理。两件事：
//
//  1. 摘要器换成【渗透领域】的 7 段模板（授权范围/资产/架构/漏洞与攻击链/工具发现/
//     所有用户消息/进度·死路），并在压缩前把被压掉的原文落盘，把【绝对路径】写进
//     摘要请求 —— 摘要丢掉的东西还能回读，不再"压一次就没了"。
//  2. ContextView：每次模型请求在尾部附一份【常驻索引】（目标/约束原文 + 活跃意图 +
//     发现一行式 + 计数）。View 是纯投影、不写回历史，所以压缩吃不掉它；放尾部而不是
//     头部是刻意的 —— 头部是 prompt cache 的前缀，动态内容放头部会让整段历史每轮
//     掉缓存（与 BLADE 把态势块放 user 尾部的既有取舍一致）。
//
// 委托关系：Pre/Reactive/IsOverflow 原样交给 SDK 的 compaction.Compactor（阈值、微压缩、
// 边界、技能重注入全不变），我们只换"怎么摘要"和"发出去之前投影成什么样"。

// 常驻索引的重算节流：View 每个模型请求都会调，索引取的是库里的实时状态；
// 3 秒内的重复请求复用同一份，避免密集工具循环里反复查库。
const contextIndexTTL = 3 * time.Second

// DomainCompactor 实现 harness.Compactor + harness.ContextView。
type DomainCompactor struct {
	inner *compaction.Compactor
	// index 每请求求值一次（有 TTL 节流）：返回空串表示本次不附索引。
	index func() string

	mu     sync.Mutex
	cached string
	at     time.Time
	ttl    time.Duration
}

var (
	_ harness.Compactor   = (*DomainCompactor)(nil)
	_ harness.ContextView = (*DomainCompactor)(nil)
)

// NewDomainCompactor wires the SDK's compaction engine (thresholds, micro-compaction,
// boundaries, skill re-injection untouched) to an ARTeX summarizer and an optional
// per-request index builder. index may be nil (no injection).
func NewDomainCompactor(cfg compaction.Config, prov llm.Provider, maxTokens int, nonStreaming bool, dumpDir string, index func() string) *DomainCompactor {
	return &DomainCompactor{
		inner: compaction.New(cfg, bladeSummarizer(prov, maxTokens, nonStreaming, dumpDir)),
		index: index,
		ttl:   contextIndexTTL,
	}
}

// Pre/Reactive/IsOverflow 全部委托：本类型只改"摘要内容"与"请求投影"，
// 不碰压缩时机与阈值。
func (d *DomainCompactor) Pre(ctx context.Context, msgs []llm.Message, lastInputTokens int) []llm.Message {
	return d.inner.Pre(ctx, msgs, lastInputTokens)
}

func (d *DomainCompactor) Reactive(ctx context.Context, msgs []llm.Message) ([]llm.Message, bool) {
	return d.inner.Reactive(ctx, msgs)
}

func (d *DomainCompactor) IsOverflow(err error) bool { return d.inner.IsOverflow(err) }

// View 是纯投影（绝不改历史）：在默认投影之后附一条平台注入的常驻索引。
// 返回的切片只用于本次请求，宿主的 l.messages 原样不动。
func (d *DomainCompactor) View(_ context.Context, msgs []llm.Message) []llm.Message {
	base := llm.MessagesForAPI(msgs)
	block := d.indexBlock()
	if block == "" {
		return base
	}
	out := make([]llm.Message, 0, len(base)+1)
	out = append(out, base...)
	out = append(out, llm.UserText(block))
	return out
}

func (d *DomainCompactor) indexBlock() string {
	if d.index == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ttl > 0 && d.cached != "" && time.Since(d.at) < d.ttl {
		return d.cached
	}
	d.cached = strings.TrimSpace(d.index())
	d.at = time.Now()
	return d.cached
}

// ContextIndexVars 是常驻索引的构造输入。ts 为 nil（无探索图的自定义 agent）
// 时只输出目标/约束缺失的简短块。
type ContextIndexVars struct {
	Store  *db.ExplorationStore
	Intent *db.Node // worker 的本条意图（可空）
	// CompanyLines 是"同公司其它任务沉淀"的一行式（P23，可空）。由调用方从 ToolSet 现读
	// （agent 包不持有 DB 句柄）；注入面刻意收紧到 ≤5 行——它每个请求都发一次。
	CompanyLines []string
	// PlaybookLines 是"本任务指纹命中的历史打法"一行式（可空）。同样由调用方现读：
	// 指纹匹配要扫资产表，不该让索引构造去碰；注入 ≤2 行。
	PlaybookLines []string
	// HasOverview 表示当前 agent 的工具面里有 graph_overview / list_facts（mainagent/planner）。
	// worker 没有这两个工具——索引里的"要细节调 …"必须按它分支，否则会把 worker 教去调
	// 一个不存在的工具。
	HasOverview bool
}

// renderCompanySection 渲染常驻索引里的"本公司已知"段（P23）。≤5 行 + 截断提示 +
// 只读先验的定性说明——索引每个请求都发一次，注入面必须收紧。
func renderCompanySection(lines []string, hasOverview bool) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n本公司已知（同公司其它任务的沉淀·只读先验·不代表本任务已测）：")
	for i, line := range lines {
		if i >= 5 {
			if hasOverview {
				b.WriteString(fmt.Sprintf("\n  · …还有 %d 条（要全量调 graph_overview 的 company_knowledge）", len(lines)-5))
			} else {
				b.WriteString(fmt.Sprintf("\n  · …还有 %d 条（索引只给前 5 条）", len(lines)-5))
			}
			break
		}
		b.WriteString("\n  · " + line)
	}
	return b.String()
}

// BuildContextIndex 构造常驻索引正文：目标原文、约束原文、活跃方向、发现一行式、手上战果
// （据点/凭据，含「未动用」标记）、同类指纹打法与计数。
// 上限刻意收紧（每类 ≤8 行、单行截断）——它每个请求都发一次，是"防丢"的保险，
// 不是态势全量（全量走 graph_overview / node_detail）。
func BuildContextIndex(v ContextIndexVars) string {
	ts := v.Store
	if ts == nil {
		return ""
	}
	// 意图/目标的编号用**任务内序号**：全局 id 跨任务连续（新任务第一条可能就是
	// intent-659），读起来像漏了六百多条。序号只用于叙事；调工具时用工具返回的 id。
	intentSeq, _ := ts.IntentOrdinals(0)
	goalSeq, _ := ts.NodeOrdinals(db.KindGoal, 0)
	seqOf := func(m map[int64]int64, id int64) int64 {
		if v := m[id]; v > 0 {
			return v
		}
		return id
	}
	var b strings.Builder
	b.WriteString("【常驻上下文 · 平台注入，非用户发言（每次请求附带，压缩不掉）】\n")
	if v.HasOverview {
		b.WriteString("（这是给你兜底的实时索引；要细节调 graph_overview / node_detail / list_facts / list_findings）\n")
	} else {
		b.WriteString("（这是给你兜底的实时索引；要细节调 node_detail / list_findings / list_worker_traces）\n")
	}
	b.WriteString("（intent-N / goal-N 里的 N 是**本任务内**的序号；调用工具时用工具返回的 id 字段）")

	if goals, err := ts.ListByKind(db.KindGoal, 20); err == nil && len(goals) > 0 {
		b.WriteString("\n目标：")
		for _, g := range goals {
			var p map[string]any
			_ = json.Unmarshal(g.Payload, &p)
			b.WriteString(fmt.Sprintf("\n  · goal-%d [%s] %s", seqOf(goalSeq, g.ID), g.State, clipLine(asText(p["text"]), 160)))
		}
	}
	if cb := strings.TrimSpace(constraintBlock(ts)); cb != "" {
		b.WriteString("\n操作约束（原文，最高优先级）：")
		b.WriteString(cb)
	}
	b.WriteString(renderCompanySection(v.CompanyLines, v.HasOverview))
	if v.Intent != nil {
		sum := digestSummary(v.Intent)
		b.WriteString(fmt.Sprintf("\n本工作意图：intent-%d %s（写回事实/发现时用它；工具里用 id %d）", seqOf(intentSeq, v.Intent.ID), clipLine(sum, 160), v.Intent.ID))
	}
	if intents, err := ts.ListNodeDigests(db.KindIntent, []string{"open", "running"}, 8); err == nil && len(intents) > 0 {
		open, running := []string{}, []string{}
		for _, n := range intents {
			line := fmt.Sprintf("intent-%d「%s」", seqOf(intentSeq, n.ID), clipLine(n.Summary, 90))
			if n.State == "running" {
				running = append(running, line)
			} else {
				open = append(open, line)
			}
		}
		if len(running) > 0 {
			b.WriteString("\n在跑方向：" + strings.Join(running, "、"))
		}
		if len(open) > 0 {
			b.WriteString("\n待领方向：" + strings.Join(open, "、"))
		}
	}
	if findings, err := ts.ListNodeDigests(db.KindFinding, nil, 8); err == nil && len(findings) > 0 {
		b.WriteString("\n已登记发现（最新在前）：")
		for _, n := range findings {
			st := n.State
			if st == "" {
				st = "pending"
			}
			b.WriteString(fmt.Sprintf("\n  · finding-%d [%s] %s", n.ID, st, clipLine(n.Summary, 110)))
		}
	}
	if st, err := ts.Stats(); err == nil {
		b.WriteString(fmt.Sprintf("\n计数：意图 %d（含已结束）、事实 %d、发现 %d",
			st[db.KindIntent], st[db.KindFact], st[db.KindFinding]))
	}
	b.WriteString(renderLootSection(ts, v.HasOverview))
	b.WriteString(renderPlaybookSection(v.PlaybookLines))
	return b.String()
}

// 战果在索引里的注入面：每类最多 3 行、扫描上限只用于算「未动用」总数。
const (
	ctxLootScanCap = 500
	ctxLootLines   = 3
)

// renderLootSection 渲染「手上还有什么」：据点与凭据一行式，并标出哪条**还没被任何意图动用**。
//
// 为什么放进常驻索引：压缩之后 planner 会暂时"忘记"手上还有没用过的能力，转头去重复拿
// 已经拿到的东西。这一行解决的是「记不住存在」，不是「该怎么用」——细节仍走 graph_overview
// 的 footholds/credentials 与 node_detail。
//
// 凭据只给指纹（db.CredentialView.Fingerprint）：索引会进每一轮提示词与日志，明文不进来。
// 已失效的战果不列——不能用就不是机会，占行只会挤掉还有用的。
func renderLootSection(ts *db.ExplorationStore, hasOverview bool) string {
	footholds, _ := ts.ListByKind(db.KindFoothold, ctxLootScanCap)
	creds, _ := ts.ListByKind(db.KindCredential, ctxLootScanCap)
	if len(footholds) == 0 && len(creds) == 0 {
		return ""
	}
	used := usedLootSet(ts, footholds, creds)
	var b strings.Builder
	renderLootKind(&b, "据点", db.KindFoothold, footholds, used, hasOverview, func(n *db.Node) string {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		return clipLine(asText(p["summary"]), 90)
	})
	renderLootKind(&b, "凭据", db.KindCredential, creds, used, hasOverview, func(n *db.Node) string {
		v := db.ParseCredential(n)
		if fp := v.Fingerprint(); fp != "" {
			return fp
		}
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		return clipLine(asText(p["summary"]), 90)
	})
	return b.String()
}

// renderLootKind 渲染一类战果，**未动用的排在前面**——它们是这条索引存在的理由，
// 不能因为前面几条已经用掉就挤出行外。
func renderLootKind(b *strings.Builder, title, kind string, nodes []*db.Node, used map[int64]bool, hasOverview bool, line func(*db.Node) string) {
	active := make([]*db.Node, 0, len(nodes))
	outstanding := make([]*db.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.State != db.StateLootActive {
			continue
		}
		if lootOutstanding(n, used) {
			outstanding = append(outstanding, n)
		} else {
			active = append(active, n)
		}
	}
	if len(outstanding)+len(active) == 0 {
		return
	}
	ordered := append(append(make([]*db.Node, 0, len(outstanding)+len(active)), outstanding...), active...)
	b.WriteString("\n" + title + "（" + fmt.Sprint(len(outstanding)) + " 条未动用）：")
	shown := ordered
	if len(shown) > ctxLootLines {
		shown = shown[:ctxLootLines]
	}
	for _, n := range shown {
		mark := "已动用"
		if lootOutstanding(n, used) {
			mark = "未动用"
		}
		desc := line(n)
		if desc == "" {
			b.WriteString(fmt.Sprintf("\n  · %s-%d [%s]", kind, n.ID, mark))
			continue
		}
		b.WriteString(fmt.Sprintf("\n  · %s-%d [%s] %s", kind, n.ID, mark, desc))
	}
	if rest := len(ordered) - len(shown); rest > 0 {
		if hasOverview {
			b.WriteString(fmt.Sprintf("\n  · …还有 %d 条（要全量调 graph_overview）", rest))
		} else {
			b.WriteString(fmt.Sprintf("\n  · …还有 %d 条（索引只给前 %d 条）", rest, ctxLootLines))
		}
	}
}

// usedLootSet 算出哪些战果已经被**真正动用**。口径与 graph_overview 一致：一条 uses 边
// 只说明有意图指向它，算不算数还要看那个意图的收场状态（db.LootConsumingIntentState）。
//
// 成本受控：只查触及这些战果的边 + 这些意图的状态，不扫全图；索引另有 3 秒 TTL。
func usedLootSet(ts *db.ExplorationStore, groups ...[]*db.Node) map[int64]bool {
	lootSet := map[int64]bool{}
	ids := make([]int64, 0, 16)
	for _, g := range groups {
		for _, n := range g {
			lootSet[n.ID] = true
			ids = append(ids, n.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	edges, err := ts.EdgesTouching(ids)
	if err != nil {
		return nil
	}
	users := map[int64][]int64{}
	fromIDs := make([]int64, 0, 8)
	seen := map[int64]bool{}
	for _, e := range edges {
		if e.Rel != db.RelUses || !lootSet[e.To] {
			continue
		}
		users[e.To] = append(users[e.To], e.From)
		if !seen[e.From] {
			seen[e.From] = true
			fromIDs = append(fromIDs, e.From)
		}
	}
	if len(fromIDs) == 0 {
		return nil
	}
	state := map[int64]string{}
	if nodes, nerr := ts.NodesByIDs(fromIDs); nerr == nil {
		for _, n := range nodes {
			state[n.ID] = n.State
		}
	}
	used := map[int64]bool{}
	for lootID, us := range users {
		for _, uid := range us {
			if db.LootConsumingIntentState(state[uid]) {
				used[lootID] = true
				break
			}
		}
	}
	return used
}

// renderPlaybookSection 渲染"本任务指纹命中的历史打法"（≤2 行）。与 graph_overview 的
// matching_playbooks 同一份数据、更小的窗口——索引每个请求都发一次。
func renderPlaybookSection(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n同类指纹的历史打法（全局只读先验·先确认指纹一致再 lookup_playbooks）：")
	for _, l := range lines {
		b.WriteString("\n  · " + clipLine(l, 150))
	}
	return b.String()
}

// digestSummary 取节点的一句话描述（payload.summary，退回 payload.text）。
// 命名与 planner.go / compaction.go 里已有的同名 helper 区分开。
func digestSummary(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return ""
	}
	if s := asText(p["summary"]); s != "" {
		return s
	}
	return asText(p["text"])
}

func asText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// clipLine 单行截断（按 rune，避免切出半个多字节字符）。
func clipLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// --- 摘要器 ---

// domainCompactPrompt is 领域化 7 段模板（可被 DB 覆盖，见 CompactionPromptOverride）。
// {{.DumpPath}} 是本次被压掉原文的落盘绝对路径。
const domainCompactPrompt = `你的任务：把上面的渗透测试会话压缩成一份【领域化交接摘要】——它将成为后续模型唯一可见的历史，接手者必须凭它继续干活，所以宁可具体、不要含糊。

严格按下面 7 个小节输出（小节标题逐字保留；某节确实没有内容就写"（无）"，不要省略小节）：

## 1. 授权范围与约束
- 任务目标、授权范围/scope、操作约束（allow/deny）的【原文】
- 用户给出的每一条限制、禁令、偏好——逐字引用，不要改写成概括

## 2. 资产与服务枚举
- 已确认资产：域名/IP/端口/服务/指纹（状态码、title、技术栈等）
- 已出现但未探明的资产线索

## 3. 架构与薄弱点
- 技术栈、框架、中间件、认证方式、信任边界
- 观察到但未验证的薄弱点（明确标注"未验证"）

## 4. 已确认漏洞与攻击路径
- 每条：类型、位置（URL/参数）、严重度、证据强度、验证到哪一步（未复现的要标明）
- 已真实走通的攻击链步骤（①→②→③）；没触发过的不要写成已确认
- 已被推翻/撤回的结论单独列出——避免接手者重蹈覆辙

## 5. 工具核心发现
- 关键命令 + 最重要的一两行输出；不要整段粘贴大输出（原文在磁盘上，见文末）

## 6. 所有用户消息
- 逐条列出被压缩对话里【用户】说过的每一句话（逐字，按时间顺序编号）
- 硬要求：用户原话是最高优先级约束，绝不能只写成概括

## 7. 进度·死路·下一步
- 当前正在做什么、做到哪一步
- 已确认的死路：试过什么、为什么不通（避免重复劳动）
- 下一步建议

【原文回读】本次被压缩的完整对话已落盘：{{.DumpPath}}。需要原始工具输出、完整报文或更早的细节时，用 Read 读该文件（或用 Bash 在该目录 grep 检索），不要凭空猜。
只输出上面 7 个小节的纯文本，不要调用任何工具。`

// CompactionPromptOverride 让服务端用 DB 里的领域压缩模板覆盖内置模板
// （镜像 PromptOverride 的接法）。未接线 / 无覆盖时用内置模板。
var CompactionPromptOverride func() (string, bool)

// compactionPromptTemplate 解析最终使用的模板（DB 覆盖优先，渲染失败回退内置）。
func compactionPromptTemplate(dumpPath string) string {
	tmpl := domainCompactPrompt
	if CompactionPromptOverride != nil {
		if t, ok := CompactionPromptOverride(); ok && strings.TrimSpace(t) != "" {
			tmpl = t
		}
	}
	out, err := renderTmpl(tmpl, struct{ DumpPath string }{DumpPath: dumpPath})
	if err != nil {
		// 覆盖模板坏了（例如引用了不存在的变量）→ 用内置模板，别让压缩整体失败。
		if out2, err2 := renderTmpl(domainCompactPrompt, struct{ DumpPath string }{DumpPath: dumpPath}); err2 == nil {
			return out2
		}
		return domainCompactPrompt
	}
	return out
}

// bladeSummarizer 替换 SDK 的 ProviderSummarizer：同样的"重放真实消息 + 追加摘要请求"
// 形状与溢出重试，但 ① 摘要请求前把被压掉的原文落盘并把绝对路径写进提示词；
// ② 用领域 7 段模板而不是 claude-code 的通用模板。
func bladeSummarizer(prov llm.Provider, maxTokens int, nonStreaming bool, dumpDir string) compaction.Summarizer {
	maxOut := 20000
	if maxTokens > 0 && maxTokens < maxOut {
		maxOut = maxTokens
	}
	return func(ctx context.Context, msgs []llm.Message) (string, error) {
		dumpPath := dumpCompactedHistory(dumpDir, msgs)
		prompt := compactionPromptTemplate(dumpPath)
		work := llm.PairToolBlocks(msgs)
		var lastErr error
		for attempt := 0; attempt <= compactPTLRetries; attempt++ {
			raw, err := summaryCall(ctx, prov, maxOut, work, prompt, nonStreaming)
			if err == nil {
				return formatSummary(raw), nil
			}
			if !compaction.IsOverflow(err) {
				return "", err
			}
			lastErr = err
			if len(work) <= 2 {
				break
			}
			// 摘要请求自己也撑爆窗口：丢最旧的 1/4 再试（新内容在下游更有价值）。
			work = work[len(work)/4:]
		}
		return "", lastErr
	}
}

const (
	compactPTLRetries       = 3
	compactStreamingRetries = 2
	summarySystemPrompt     = "You are a helpful AI assistant tasked with summarizing conversations."
)

// summaryCall 发一次摘要请求；瞬时故障重试，溢出立即返回让调用方降级重试。
func summaryCall(ctx context.Context, p llm.Provider, maxOut int, head []llm.Message, prompt string, nonStreaming bool) (string, error) {
	if p == nil {
		return "", fmt.Errorf("compaction: provider 未接线")
	}
	reqMsgs := make([]llm.Message, 0, len(head)+1)
	reqMsgs = append(reqMsgs, head...)
	reqMsgs = append(reqMsgs, llm.UserText(prompt))
	req := llm.CompletionRequest{
		System:    []string{summarySystemPrompt},
		Messages:  reqMsgs,
		MaxTokens: maxOut,
		Thinking:  "disabled",
	}
	var lastErr error
	for range compactStreamingRetries {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var text string
		var callErr error
		if nonStreaming {
			msg, _, _, err := p.Complete(ctx, req)
			text, callErr = msg.Text(), err
		} else {
			acc := llm.NewAccumulator()
			for ev, err := range p.Stream(ctx, req) {
				if err != nil {
					callErr = err
					break
				}
				acc.Add(ev)
			}
			if callErr == nil {
				text = acc.Message().Text()
			}
		}
		if callErr == nil {
			return text, nil
		}
		if compaction.IsOverflow(callErr) {
			return "", callErr
		}
		lastErr = callErr
	}
	return "", lastErr
}

var (
	analysisBlockRe = regexp.MustCompile(`(?s)<analysis>.*?</analysis>`)
	summaryBlockRe  = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
)

// formatSummary 去掉模型可能带出的 <analysis> 推理块；若它用 <summary> 包裹正文则取其内容。
func formatSummary(raw string) string {
	s := analysisBlockRe.ReplaceAllString(raw, "")
	if m := summaryBlockRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "（摘要为空）"
	}
	return s
}

// dumpCompactedHistory 把本次将被摘要掉的对话原文写到 <dumpDir>/compaction/<unix>.txt，
// 返回绝对路径（写失败时返回"(未落盘)"，压缩照常进行——落盘是保险，不是前置条件）。
func dumpCompactedHistory(dumpDir string, msgs []llm.Message) string {
	if strings.TrimSpace(dumpDir) == "" || len(msgs) == 0 {
		return "(未落盘)"
	}
	dir := filepath.Join(dumpDir, "compaction")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[compaction] 落盘目录创建失败: %v", err)
		return "(未落盘)"
	}
	path, err := filepath.Abs(filepath.Join(dir, fmt.Sprintf("%d.txt", time.Now().Unix())))
	if err != nil {
		path = filepath.Join(dir, fmt.Sprintf("%d.txt", time.Now().Unix()))
	}
	body := renderHistoryText(msgs)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		log.Printf("[compaction] 原文落盘失败: %v", err)
		return "(未落盘)"
	}
	return path
}

// renderHistoryText 把消息渲染成可读文本（工具调用与结果都保留原文）。
func renderHistoryText(msgs []llm.Message) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# 压缩前原文（%s，共 %d 条消息）\n", time.Now().Format("2006-01-02 15:04:05"), len(msgs)))
	for i, m := range msgs {
		fmt.Fprintf(&b, "\n===== [%d] %s =====\n", i+1, m.Role)
		for _, blk := range m.Content {
			switch blk.Type {
			case llm.BlockText:
				b.WriteString(blk.Text + "\n")
			case llm.BlockThinking:
				b.WriteString("[thinking] " + blk.Thinking + "\n")
			case llm.BlockToolUse:
				fmt.Fprintf(&b, "[tool_use %s] %s\n", blk.Name, string(blk.Input))
			case llm.BlockToolResult:
				fmt.Fprintf(&b, "[tool_result]%s %s\n", errMark(blk.IsError), blockText(blk))
			}
		}
	}
	return b.String()
}

func errMark(isErr bool) string {
	if isErr {
		return "(error)"
	}
	return ""
}

// blockText 取工具结果块里的文本（可能有多段）。
func blockText(blk llm.ContentBlock) string {
	if blk.Text != "" {
		return blk.Text
	}
	var parts []string
	for _, c := range blk.Content {
		if c.Type == llm.BlockText && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// iter 类型断言：llm.Provider.Stream 返回 iter.Seq2（见 llm/types.go）。
var _ iter.Seq2[llm.StreamEvent, error]

// --- 装配 ---

// InstallContextManager 在 opts 上装 P14 领域上下文管理器。调用点必须在 enableNoa 之后：
// noa 与它抢同一个 Options.Compactor 槽位，noa 已占用时本函数不动它（互斥而非静默覆盖）。
func InstallContextManager(opts *agentcore.Options, windowTokens, maxTokens int, dumpDir string, index func() string) {
	if opts == nil {
		return
	}
	if opts.Compactor != nil {
		return // noa 已接管
	}
	cfg := compactionConfig(windowTokens, maxTokens)
	if cfg == nil {
		cfg = &compaction.Config{}
	}
	opts.Compactor = NewDomainCompactor(*cfg, opts.Provider, opts.MaxTokens, opts.NonStreaming, dumpDir, index)
	// Compactor 与 Compaction 互斥（SDK 会告警并忽略后者），显式清掉。
	opts.Compaction = nil
}
