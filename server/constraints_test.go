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
	if err := m.pg.SetSetting("exploration_constraints_profile", "enhanced"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.pg.Exec(`DELETE FROM settings WHERE key='exploration_constraints_profile'`) }()
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
	got := dropRetiredConstraintTools(tools)
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
