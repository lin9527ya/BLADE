package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	actool "github.com/lin9527ya/Norma/tool"
)

func TestDeferredSystem_NoGlobal(t *testing.T) {
	// No global MCP names → single segment (base + the always-on trace-neutrality
	// block) — the single segment is still static and must be cached.
	sys, boundary := deferredSystem("SYS", DeferredInfo{})
	if len(sys) != 1 || !strings.HasPrefix(sys[0], "SYS") || boundary != 1 {
		t.Fatalf("expected [SYS+block],1 — got %v,%d", sys, boundary)
	}
	if !strings.Contains(sys[0], "目标侧痕迹必须中性") {
		t.Fatal("always-on trace-neutrality block missing")
	}
}

func TestDeferredSystem_WithGlobal(t *testing.T) {
	def := DeferredInfo{
		Deferred:    []string{"mcp__browser__navigate", "mcp__browser__click"},
		GlobalNames: []string{"mcp__browser__navigate", "mcp__browser__click"},
	}
	sys, boundary := deferredSystem("SYS", def)
	if len(sys) != 2 || !strings.HasPrefix(sys[0], "SYS") {
		t.Fatalf("expected [SYS+neutrality, block], got %v", sys)
	}
	if !strings.Contains(sys[1], "<available-deferred-tools>") ||
		!strings.Contains(sys[1], "mcp__browser__navigate") {
		t.Fatalf("block missing names:\n%s", sys[1])
	}
	if boundary != len(sys) {
		t.Fatalf("boundary=%d want %d (whole prompt cached)", boundary, len(sys))
	}
}

func TestDeferredSystem_GatedNotInBlock(t *testing.T) {
	// A skill-gated server's tools are deferred but NOT in the global block.
	def := DeferredInfo{
		Deferred:    []string{"mcp__browser__navigate", "mcp__secret__do"},
		GlobalNames: []string{"mcp__browser__navigate"}, // secret gated → excluded
	}
	sys, _ := deferredSystem("SYS", def)
	if strings.Contains(sys[1], "mcp__secret__do") {
		t.Fatalf("gated tool must not appear in global block:\n%s", sys[1])
	}
	if !strings.Contains(sys[1], "mcp__browser__navigate") {
		t.Fatal("global tool should appear in block")
	}
}

func TestDeferredSystem_AlwaysSkillsLayer(t *testing.T) {
	// 常驻技能（常量层）排在 deferred 工具块之前，二者同属稳定段、一起被缓存。
	def := DeferredInfo{
		AlwaysSkills: "## 【常驻技能 · 始终遵守】\n### src-rules\n规则正文",
		GlobalNames:  []string{"mcp__browser__navigate"},
	}
	sys, boundary := deferredSystem("SYS", def)
	if len(sys) != 3 {
		t.Fatalf("want [SYS+neutrality, always, deferred], got %v", sys)
	}
	if sys[1] != def.AlwaysSkills || !strings.Contains(sys[2], "<available-deferred-tools>") {
		t.Fatalf("layer order wrong:\n%s\n%s", sys[1], sys[2])
	}
	if boundary != len(sys) {
		t.Fatalf("boundary=%d want %d (whole prompt cached)", boundary, len(sys))
	}

	// 只有常驻、没有 deferred 块：两段 + boundary 覆盖全部。
	sys, boundary = deferredSystem("SYS", DeferredInfo{AlwaysSkills: "RULES"})
	if len(sys) != 2 || sys[1] != "RULES" || boundary != 2 {
		t.Fatalf("always-only: %v,%d", sys, boundary)
	}

	// 空白常驻等效于没有：行为与旧版完全一致。
	sys, boundary = deferredSystem("SYS", DeferredInfo{AlwaysSkills: "   \n"})
	if len(sys) != 1 || !strings.HasPrefix(sys[0], "SYS") || boundary != 1 {
		t.Fatalf("blank always must be inert: %v,%d", sys, boundary)
	}
}

func TestSeedUnlockFromHistory(t *testing.T) {
	skillCall := func(name string) llm.ContentBlock {
		return llm.ContentBlock{Type: llm.BlockToolUse, Name: "Skill", Input: json.RawMessage(`{"name":"` + name + `"}`)}
	}
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{skillCall("browsing")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, Name: "Bash", Input: json.RawMessage(`{}`)}, // ignored
			skillCall("recon"),
		}},
	}
	var got []string
	seedUnlockFromHistory(msgs, func(name string) { got = append(got, name) })
	if strings.Join(got, ",") != "browsing,recon" {
		t.Fatalf("unlocked=%v want [browsing recon]", got)
	}
	seedUnlockFromHistory(msgs, nil) // nil → no-op, no panic
}

// TestUnlockGatingFlow mirrors what OnInvoke / seedUnlockFromHistory do: a gated
// tool starts locked and becomes callable only after its skill unlocks it.
func TestUnlockGatingFlow(t *testing.T) {
	serverTools := map[string][]string{"secret": {"mcp__secret__do"}}
	unlock := actool.NewUnlockSet("mcp__browser__navigate") // global only
	unlockSkill := func(name string) {
		if name == "unlock-secret" {
			unlock.Add(serverTools["secret"]...)
		}
	}
	if unlock.Has("mcp__secret__do") {
		t.Fatal("gated tool should start locked")
	}
	unlockSkill("unlock-secret")
	if !unlock.Has("mcp__secret__do") {
		t.Fatal("gated tool should be unlocked after skill load")
	}
}

// worker 的 schema 瘦身清单必须只包含真实存在的工具，且不能把高频写回/执行主路径
// 也 deferred——那样每轮会多一次「先查 schema」的往返，反而拖慢主流程。
func TestWorkerDeferredTools(t *testing.T) {
	base := NewToolSet(nil, "").WorkerTools()
	have := map[string]bool{}
	for _, tl := range base {
		have[tl.Name()] = true
	}
	deferred := map[string]bool{}
	for _, name := range workerDeferredTools {
		if !have[name] {
			t.Fatalf("deferred 工具 %q 不在 WorkerTools 里：提示词会指向不存在的工具", name)
		}
		deferred[name] = true
	}
	for _, core := range []string{
		"insert_assets", "record_fact", "report_finding", "report_credential",
		"report_foothold", "report_artifact", "list_assets", "get_worker_trace",
		"search_all_worker_traces", "node_detail", "list_findings", "conclude_intent",
		"run_execution", "wait_execution", "get_execution",
	} {
		if deferred[core] {
			t.Fatalf("高频/核心工具 %q 不应 deferred：每轮会多一次 SearchExtraTools 往返", core)
		}
	}
	got := deferWorkerTools(base)
	if len(got) != len(workerDeferredTools) {
		t.Fatalf("deferWorkerTools 丢了名字：%v", got)
	}
	sys, _ := deferredSystem("SYS", DeferredInfo{Deferred: got, GlobalNames: got})
	if len(sys) < 2 || !strings.Contains(sys[1], "report_footprint") {
		t.Fatalf("deferred 块缺少 worker 工具：%v", sys)
	}
}

// 主 agent 的低频工具 deferred 清单：名字必须真实存在，核心对话/写回工具不得
// 被 deferred（否则用户一句「记一下」会先多一轮查 schema）。
func TestMainAgentDeferredTools(t *testing.T) {
	base := NewToolSet(nil, "").MainAgentTools()
	have := map[string]bool{}
	for _, tl := range base {
		have[tl.Name()] = true
	}
	deferred := map[string]bool{}
	for _, name := range mainDeferredTools {
		if !have[name] {
			t.Fatalf("deferred 工具 %q 不在 MainAgentTools 里", name)
		}
		deferred[name] = true
	}
	for _, core := range []string{
		"graph_overview", "list_findings", "list_facts", "node_detail",
		"get_worker_output", "get_worker_trace", "search_all_worker_traces",
		"add_hint", "add_intent", "steer_work", "set_goals", "set_constraints",
		"add_task_scope", "add_company_scope", "insert_assets", "list_assets",
		"list_companies", "report_finding", "record_fact", "report_foothold",
		"report_credential",
	} {
		if deferred[core] {
			t.Fatalf("核心对话/写回工具 %q 不应 deferred", core)
		}
	}
	got := deferMainTools(base)
	if len(got) != len(mainDeferredTools) {
		t.Fatalf("deferMainTools 丢了名字：%v", got)
	}
	sys, _ := deferredSystem("SYS", DeferredInfo{Deferred: got, GlobalNames: got})
	if len(sys) < 2 || !strings.Contains(sys[1], "report_footprint") {
		t.Fatalf("deferred 块缺少主 agent 工具：%v", sys)
	}
}

// 有 MCP/技能时 def.Unlock 非 nil 且只含 MCP 名字；内置 deferred 工具必须一并加入
// unlock，否则 ExecuteExtraTool 会以为它是技能工具、永远报 "not unlocked"。
func TestMergeDeferredUnlocksBuiltins(t *testing.T) {
	def := DeferredInfo{Unlock: actool.NewUnlockSet("mcp__browser__navigate")}
	mergeDeferred(&def, []string{"close_candidate", "report_footprint"})
	if !def.Unlock.Has("close_candidate") || !def.Unlock.Has("report_footprint") {
		t.Fatalf("built-in deferred tools not unlocked: %v", def.Unlock.List())
	}
	if !def.Unlock.Has("mcp__browser__navigate") {
		t.Fatal("existing MCP unlock was lost")
	}
	if len(def.Deferred) != 2 || len(def.GlobalNames) != 2 {
		t.Fatalf("deferred wiring = %+v", def)
	}
	// nil unlock（无 MCP/技能）：保持 nil，SDK 会按 DeferredTools 自行全量解锁。
	var bare DeferredInfo
	mergeDeferred(&bare, []string{"close_candidate"})
	if bare.Unlock != nil {
		t.Fatal("nil unlock should stay nil")
	}
}
