package server

import (
	"context"
	"encoding/json"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
)

// 即使数据库还留着 enhanced/custom 的旧 profile，也不能把 BLADE 已下线的
// 探索约束工具重新注入 planner。
func TestPlannerConstraintToolsCannotBeReinjected(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer func() { _ = m.SetConstraintProfilePreset("classic") }()
	if err := m.SetConstraintProfilePreset("enhanced"); err != nil {
		t.Fatal(err)
	}
	build := func(name string) actool.CoreTool {
		return actool.Build(actool.Spec{
			Name: name,
			Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
				return actool.Result{}, nil
			},
		})
	}
	retired := []string{"coverage_matrix", "exploration_ledger", "chain_detail", "dismiss_coverage", "accept_completion_risk", "dismiss_leads", "record_probe_fact"}
	tools := make([]actool.CoreTool, 0, len(retired)+1)
	for _, name := range retired {
		tools = append(tools, build(name))
	}
	tools = append(tools, build("graph_overview"))
	got := dropRetiredConstraintTools(m.pg, tools)
	names := map[string]bool{}
	for _, tool := range got {
		names[tool.Name()] = true
	}
	for _, name := range retired {
		if names[name] {
			t.Fatalf("retired constraint tool %s was re-injected under enhanced profile", name)
		}
	}
	if !names["graph_overview"] {
		t.Fatal("capability tool was dropped")
	}
}

// 升级后默认经典（约束全关）；预设可切增强，再切回经典。
func TestConstraintProfilePreset(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	if got := m.ExplorationConstraintsProfile(); got != "classic" {
		t.Fatalf("默认 profile = %q, want classic", got)
	}
	if m.ConstraintEnabled("dispatch") {
		t.Fatal("classic 模式默认不应开启 dispatch 约束")
	}
	if err := m.SetConstraintProfilePreset("enhanced"); err != nil {
		t.Fatal(err)
	}
	if !m.ConstraintEnabled("dispatch") || !m.ConstraintEnabled("ledger") {
		t.Fatal("enhanced 预设应打开全部分组")
	}
	if err := m.SetConstraintProfilePreset("classic"); err != nil {
		t.Fatal(err)
	}
	if m.ConstraintEnabled("dispatch") {
		t.Fatal("切回 classic 后应全部关闭")
	}
}
