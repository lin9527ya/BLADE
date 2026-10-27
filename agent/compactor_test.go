package agent

import (
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/compaction"
	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/db"
)

// dumpPathRe finds the absolute dump path inside the summary-request prompt.
var dumpPathRe = regexp.MustCompile(`/[^\s。，、）()]+?\.txt`)

// recordingProvider captures the summarizer's request and replays canned text.
type recordingProvider struct {
	mu   sync.Mutex
	reqs []llm.CompletionRequest
	text string
	err  error
}

func (p *recordingProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	if p.err != nil {
		return llm.Message{}, "", llm.Usage{}, p.err
	}
	return assistantMsg(p.text), "end_turn", llm.Usage{}, nil
}

// assistantMsg builds a minimal assistant text message (there is no SDK constructor).
func assistantMsg(s string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(s)}}
}

// toolUseMsg builds an assistant message carrying one tool_use block.
func toolUseMsg(id, name, input string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{
		Type: llm.BlockToolUse, ID: id, Name: name, Input: []byte(input),
	}}}
}

func (p *recordingProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("test provider is non-streaming only"))
	}
}

func (p *recordingProvider) last() llm.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		return llm.CompletionRequest{}
	}
	return p.reqs[len(p.reqs)-1]
}

// TestDomainCompactorDelegatesThresholds pins the delegation contract: threshold
// behavior (micro/auto compaction) stays the SDK's, and IsOverflow keeps the SDK's
// classification — this type only changes WHAT a summary says and how a request is
// projected.
func TestDomainCompactorDelegatesThresholds(t *testing.T) {
	prov := &recordingProvider{text: "## 1. 授权范围与约束\n（无）"}
	d := NewDomainCompactor(compaction.Config{ContextWindow: 200000}, prov, 0, true, t.TempDir(), nil)

	small := []llm.Message{llm.UserText("hi"), assistantMsg("hello")}
	if out := d.Pre(context.Background(), small, 0); len(out) != len(small) {
		t.Fatalf("under threshold: Pre must not rewrite history (%d → %d)", len(small), len(out))
	}
	if len(prov.reqs) != 0 {
		t.Fatalf("under threshold: no summary request expected, got %d", len(prov.reqs))
	}
	// 明显超过阈值 → 触发一次摘要（委托给 SDK 的引擎，用我们注入的摘要器）。
	// 历史必须长到 SDK 能保留"最近 N 条尾巴"（KeepRecent），否则无可摘要段。
	big := make([]llm.Message, 0, 12)
	for i := 0; i < 6; i++ {
		big = append(big, llm.UserText(strings.Repeat("A", 30000)), assistantMsg(strings.Repeat("B", 30000)))
	}
	out := d.Pre(context.Background(), big, 900000)
	if len(out) <= len(big) {
		t.Fatalf("over threshold: a boundary + summary must be inserted (%d → %d)", len(big), len(out))
	}
	if len(prov.reqs) != 1 {
		t.Fatalf("over threshold: exactly one summary request expected, got %d", len(prov.reqs))
	}
	if !d.IsOverflow(errors.New("prompt is too long: 300000 tokens > 200000 maximum context")) {
		t.Fatal("IsOverflow must delegate to the SDK classifier")
	}
	if d.IsOverflow(errors.New("boom")) {
		t.Fatal("IsOverflow must not classify an ordinary error as overflow")
	}
}

// TestDomainCompactorViewAppendsIndexAndIsPure pins the ContextView contract: the
// projection is base + one injected platform block, and the INPUT history is never
// mutated (View must be pure — the host keeps l.messages untouched).
func TestDomainCompactorViewAppendsIndexAndIsPure(t *testing.T) {
	d := NewDomainCompactor(compaction.Config{}, nil, 0, true, t.TempDir(), func() string { return "【常驻上下文】index-body" })
	d.ttl = 0 // 测试里不节流
	msgs := []llm.Message{llm.UserText("human turn"), assistantMsg("assistant answer")}
	before := len(msgs)

	out := d.View(context.Background(), msgs)
	if len(out) != before+1 {
		t.Fatalf("index must be appended once: %d → %d", before, len(out))
	}
	last := out[len(out)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text(), "index-body") {
		t.Fatalf("injected block wrong: %+v", last)
	}
	if len(msgs) != before {
		t.Fatalf("View mutated the input history: %d → %d", before, len(msgs))
	}
	// 空索引 → 投影与默认完全一致（不加噪声）。
	d2 := NewDomainCompactor(compaction.Config{}, nil, 0, true, t.TempDir(), func() string { return "  " })
	plain := d2.View(context.Background(), msgs)
	if len(plain) != before {
		t.Fatalf("empty index must not inject anything: %d", len(plain))
	}
	// 投影沿用 SDK 的默认规则：边界标记被剔除。
	withBoundary := append([]llm.Message{llm.BoundaryMessage(llm.BoundaryMeta{Trigger: "auto"})}, msgs...)
	got := d.View(context.Background(), withBoundary)
	for _, m := range got {
		if llm.IsBoundaryMarker(m) {
			t.Fatal("boundary markers must be dropped by the projection")
		}
	}
}

// TestBladeSummarizerDumpsOriginalAndHintsPath pins the "原文可回读" half: the
// summary request carries the 7 domain sections AND the ABSOLUTE path of a file
// holding the very messages being summarized (tool output included verbatim).
func TestBladeSummarizerDumpsOriginalAndHintsPath(t *testing.T) {
	prov := &recordingProvider{text: "ok"}
	dumpDir := t.TempDir()
	sum := bladeSummarizer(prov, 0, true, dumpDir)

	msgs := []llm.Message{
		llm.UserText("扫一下 example.com"),
		toolUseMsg("t1", "Bash", `{"command":"nmap -sV example.com"}`),
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("t1", "PORT   STATE SERVICE\n80/tcp open  http", false)}},
	}
	got, err := sum(context.Background(), msgs)
	if err != nil || got != "ok" {
		t.Fatalf("summary: %q err=%v", got, err)
	}
	req := prov.last()
	if len(req.System) != 1 || !strings.Contains(req.System[0], "summariz") {
		t.Fatalf("summary request must carry the summarizer system prompt: %+v", req.System)
	}
	prompt := req.Messages[len(req.Messages)-1].Text()
	for _, section := range []string{
		"## 1. 授权范围与约束", "## 2. 资产与服务枚举", "## 3. 架构与薄弱点",
		"## 4. 已确认漏洞与攻击路径", "## 5. 工具核心发现", "## 6. 所有用户消息", "## 7. 进度·死路·下一步",
	} {
		if !strings.Contains(prompt, section) {
			t.Fatalf("领域模板缺少小节 %q", section)
		}
	}
	// 提示词里的路径必须是绝对路径，且文件真实存在、含被压掉的原文。
	m := dumpPathRe.FindString(prompt)
	if m == "" {
		t.Fatalf("摘要提示词里没有给出原文绝对路径:\n%s", prompt)
	}
	path := m
	if !filepath.IsAbs(path) {
		t.Fatalf("原文路径必须是绝对路径: %q", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("原文落盘文件不可读: %v", err)
	}
	if !strings.Contains(string(body), "80/tcp open  http") || !strings.Contains(string(body), "nmap -sV example.com") {
		t.Fatalf("落盘文件必须含原始工具输出:\n%s", body)
	}
	if !strings.Contains(string(body), "扫一下 example.com") {
		t.Fatal("落盘文件必须含用户原话")
	}
}

// TestBladeSummarizerStripsAnalysis pins output hygiene: a model that still emits an
// <analysis> block (or wraps the digest in <summary>) yields clean section text.
func TestBladeSummarizerStripsAnalysis(t *testing.T) {
	prov := &recordingProvider{text: "<analysis>let me think</analysis>\n<summary>## 1. 授权范围与约束\n仅 example.com</summary>"}
	sum := bladeSummarizer(prov, 0, true, t.TempDir())
	got, err := sum(context.Background(), []llm.Message{llm.UserText("x")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "let me think") || strings.Contains(got, "<summary>") {
		t.Fatalf("analysis/summary 包裹必须被剥掉: %q", got)
	}
	if !strings.Contains(got, "仅 example.com") {
		t.Fatalf("正文必须保留: %q", got)
	}
	// 落盘失败也不阻断压缩：dumpDir 为空 → 提示词里是"(未落盘)"。
	sum2 := bladeSummarizer(prov, 0, true, "")
	if _, err := sum2(context.Background(), []llm.Message{llm.UserText("x")}); err != nil {
		t.Fatalf("落盘不可用时压缩仍须继续: %v", err)
	}
	if p := prov.last().Messages[len(prov.last().Messages)-1].Text(); !strings.Contains(p, "(未落盘)") {
		t.Fatalf("未落盘时提示词须如实说明: %s", p)
	}
}

// TestInstallContextManagerYieldsToNoa pins the mutual exclusion: noa owns the same
// Options.Compactor slot, and our installer must never silently overwrite it.
func TestInstallContextManagerYieldsToNoa(t *testing.T) {
	noa := &DomainCompactor{}
	opts := &agentcore.Options{Compactor: noa}
	InstallContextManager(opts, 200000, 0, t.TempDir(), nil)
	if opts.Compactor != harness.Compactor(noa) {
		t.Fatal("noa 已占用 Compactor 槽位时不得被覆盖")
	}
	opts2 := &agentcore.Options{}
	InstallContextManager(opts2, 200000, 0, t.TempDir(), func() string { return "x" })
	if opts2.Compactor == nil || opts2.Compaction != nil {
		t.Fatalf("未装 noa 时应装上 DomainCompactor 且清空 Compaction: %+v", opts2)
	}
}

// profile 的输出上限要进压缩配置：SDK 用它算摘要预留，0 会按 20K 预留，
// 对 64K 窗口意味着提前压缩。
func TestCompactionConfigCarriesOutputReserve(t *testing.T) {
	cfg := compactionConfig(64000, 8000)
	if cfg.ContextWindow != 64000 || cfg.MaxOutputTokens != 8000 {
		t.Fatalf("compaction config = %+v，want window=64000 max_out=8000", cfg)
	}
	if cfg := compactionConfig(0, 0); cfg.ContextWindow != defaultWindowK*1000 || cfg.MaxOutputTokens != 0 {
		t.Fatalf("empty config should keep defaults: %+v", cfg)
	}
}

// TestBuildContextIndexCarriesGoalScopeAndGraph pins the index contents against a
// real graph: goal text, constraint text, live directions, findings and counts.
func TestBuildContextIndexCarriesGoalScopeAndGraph(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("p14 index", "常驻索引", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	st := d.Exploration(task.ExplorationID)
	if _, err := st.AddGoal(map[string]any{"text": "打穿 example.com 的登录口"}, "planner"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddConstraint("deny", "禁止对生产库做写操作", "user"); err != nil {
		t.Fatal(err)
	}
	it, err := st.AddIntent(map[string]any{"summary": "枚举登录口端点"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(it, "work#1", db.IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	idx := BuildContextIndex(ContextIndexVars{Store: st, Intent: &db.Node{ID: it, Payload: []byte(`{"summary":"枚举登录口端点"}`)}})
	for _, want := range []string{
		"打穿 example.com 的登录口", "禁止对生产库做写操作", "intent-1", "在跑方向", "计数：意图",
	} {
		if !strings.Contains(idx, want) {
			t.Fatalf("常驻索引缺少 %q:\n%s", want, idx)
		}
	}
	if got := BuildContextIndex(ContextIndexVars{}); got != "" {
		t.Fatalf("没有探索图时应返回空索引: %q", got)
	}
	// 索引有 TTL 节流：短时间内重复请求复用同一份，避免密集工具循环反复查库。
	idxCalls := 0
	dc := NewDomainCompactor(compaction.Config{}, nil, 0, true, t.TempDir(), func() string { idxCalls++; return "x" })
	dc.View(context.Background(), nil)
	dc.View(context.Background(), nil)
	if idxCalls != 1 {
		t.Fatalf("TTL 内应复用索引（查库次数=%d）", idxCalls)
	}
	dc.ttl = 0
	dc.at = time.Now()
	dc.View(context.Background(), nil)
	if idxCalls != 2 {
		t.Fatalf("TTL=0 时应每次重算（查库次数=%d）", idxCalls)
	}
}

// TestSessionIDRulesAreSharedBetweenStores pins the invariant behind "两套状态不许漂移":
// the transcript (history) and the noa archive (compaction state) are keyed by the
// SAME session id — one rule per agent role, so a resumed conversation and its
// archive can never describe different conversations. Segment 0 keeps the legacy
// name so pre-existing transcripts still resume.
func TestSessionIDRulesAreSharedBetweenStores(t *testing.T) {
	if got := MainSessionID(7, 0); got != "exp7-main" {
		t.Fatalf("seg 0 必须沿用旧名（老 transcript 才能恢复）: %q", got)
	}
	if got := MainSessionID(7, 2); got != "exp7-main-s2" {
		t.Fatalf("分段会话 id 不对: %q", got)
	}
	if got := PlannerSessionID(7); got != "exp7-planner" {
		t.Fatalf("planner 会话 id 不对: %q", got)
	}
	if got := WorkerSessionID(7, 9); got != "exp7-worker-i9" {
		t.Fatalf("worker 会话 id 不对: %q", got)
	}
}
