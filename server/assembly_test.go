package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// hasSkillTool reports whether the packed tool set contains the Skill meta-tool.
func hasSkillTool(tools []actool.CoreTool) bool {
	for _, t := range tools {
		if t.Name() == "Skill" {
			return true
		}
	}
	return false
}

// TestAssembleVisibleSkill verifies a filesystem skill made visible to an agent is
// packed into that agent's tool set (as the single Skill meta-tool) via the wired
// ToolAugment hook. Skills live on disk under skillDir; visibility is a per-agent
// (agent × skill_name) row keyed by the skill's directory name.
func TestAssembleVisibleSkill(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行（Go 的测试清理顺序），所以关库
	// 只能注册成 Cleanup——写 defer 会让它先于下面的数据清理跑，清理语句全部落到
	// 已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = pg.Close() })

	// Filesystem skill fixture: <skillDir>/t-assemble/SKILL.md. The directory name
	// (t-assemble) is the visibility key matched against AgentSkillNames.
	skillDir := t.TempDir()
	const skillName = "t-assemble"
	if err := os.MkdirAll(filepath.Join(skillDir, skillName), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: t-assemble\ndescription: do the thing\n---\ndo the thing"
	if err := os.WriteFile(filepath.Join(skillDir, skillName, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	wireAgentAugment(pg, skillDir, nil)
	t.Cleanup(func() { agent.ToolAugment = nil })

	ag, _ := pg.GetAgentByKey("planner")
	if ag == nil {
		t.Fatal("planner agent missing")
	}

	// Isolate this test from whatever skill visibility the planner already has in the
	// shared DB: clear it now, restore it on cleanup.
	orig, _ := pg.AgentSkillNames(ag.ID)
	if err := pg.SetAgentSkillVisibility(ag.ID, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.SetAgentSkillVisibility(ag.ID, orig) })

	// before: skill not visible → no Skill meta-tool
	extra, _, cleanup := agent.ToolAugment(context.Background(), "planner")
	cleanup()
	if hasSkillTool(extra) {
		t.Fatalf("expected no Skill meta-tool before the skill is made visible")
	}

	// make the skill visible to planner
	if err := pg.ToggleSkillVisibility(ag.ID, skillName, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.ToggleSkillVisibility(ag.ID, skillName, false) })

	// after: the Skill meta-tool is packed in
	extra, _, cleanup = agent.ToolAugment(context.Background(), "planner")
	defer cleanup()
	if !hasSkillTool(extra) {
		t.Fatalf("expected the Skill meta-tool after making the skill visible")
	}

	// AugmentTools appends extra to base without filtering base
	combined, _, c2 := agent.AugmentTools(context.Background(), "planner", nil)
	defer c2()
	if !hasSkillTool(combined) {
		t.Fatalf("AugmentTools should surface the Skill meta-tool")
	}
}

// TestLazyMCPAssemblyUsesDBSchemasWithoutDial verifies that when MCP tool schemas
// are already persisted in mcp_tools_cache, ToolAugment assembles deterministic
// CoreTool proxies without spawning external stdio/HTTP MCP servers.
func TestLazyMCPAssemblyUsesDBSchemasWithoutDial(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = pg.Close() })

	srvName := "t-lazy-mcp"
	mcpID, err := pg.SaveMCP(&db.MCPServer{
		Name:      srvName,
		Transport: "stdio",
		Command:   "/nonexistent/should-never-spawn-at-assembly",
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteMCP(mcpID) })

	if err := pg.SaveMCPTools(mcpID, []db.MCPTool{
		{Name: "mcp__t-lazy-mcp__zebra", Description: "z tool", Schema: json.RawMessage(`{"type":"object"}`)},
		{Name: "mcp__t-lazy-mcp__alpha", Description: "a tool", Schema: json.RawMessage(`{"type":"object"}`)},
	}); err != nil {
		t.Fatal(err)
	}

	ag, _ := pg.GetAgentByKey("planner")
	if ag == nil {
		t.Fatal("planner agent missing")
	}
	if err := pg.ToggleVisibility(ag.ID, "mcp", mcpID, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.ToggleVisibility(ag.ID, "mcp", mcpID, false) })

	wireAgentAugment(pg, t.TempDir(), nil)
	t.Cleanup(func() { agent.ToolAugment = nil })

	extra, def, cleanup := agent.ToolAugment(context.Background(), "planner")
	defer cleanup()

	var names []string
	for _, tl := range extra {
		if strings.HasPrefix(tl.Name(), "mcp__t-lazy-mcp__") {
			names = append(names, tl.Name())
		}
	}
	if len(names) != 2 || names[0] != "mcp__t-lazy-mcp__alpha" || names[1] != "mcp__t-lazy-mcp__zebra" {
		t.Fatalf("expected sorted lazy MCP tools from DB cache without dialing binary, got %v", names)
	}
	if len(def.GlobalNames) < 2 || def.GlobalNames[0] != "mcp__t-lazy-mcp__alpha" {
		t.Fatalf("expected sorted GlobalNames, got %v", def.GlobalNames)
	}
}
