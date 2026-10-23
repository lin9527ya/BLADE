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
	// block), no cache boundary.
	sys, boundary := deferredSystem("SYS", DeferredInfo{})
	if len(sys) != 1 || !strings.HasPrefix(sys[0], "SYS") || boundary != 0 {
		t.Fatalf("expected [SYS+block],0 — got %v,%d", sys, boundary)
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
	if len(sys) != 1 || !strings.HasPrefix(sys[0], "SYS") || boundary != 0 {
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
