package server

import (
	"os"
	"testing"

	"github.com/lin9527ya/blade/agent"
)

// 迁移只在"库里仍是旧内置正文"时生效；用户编辑过的正文必须保留。
func TestMigratePlannerPrompt(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	a, err := m.pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		t.Fatalf("planner agent: %v", err)
	}
	old, err := os.ReadFile("../docs/prompts/planner/default.body.md")
	if err != nil {
		t.Fatalf("读旧正文备份: %v", err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingPlannerPromptV2)
		_, _ = m.pg.SavePrompt(a.ID, agent.PlannerPromptBody(), "test cleanup", "test")
	})

	// 其它用例构造 Server 时可能已经跑过迁移并置旗标；本用例显式从"未迁移"开始。
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingPlannerPromptV2)
	// ① 旧内置正文 → 迁移到 v2。
	if _, err := m.pg.SavePrompt(a.ID, string(old), "test seed v1", "test"); err != nil {
		t.Fatal(err)
	}
	migratePlannerPrompt(m.pg)
	cur, err := m.pg.CurrentPrompt(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur != agent.PlannerPromptBody() {
		t.Fatalf("旧正文应迁移到 v2（当前长度 %d，want %d）", len(cur), len(agent.PlannerPromptBody()))
	}

	// ② 用户编辑过的正文 → 不覆盖（清旗标后重跑）。
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingPlannerPromptV2)
	if _, err := m.pg.SavePrompt(a.ID, "用户自己改过的正文", "user edit", "test"); err != nil {
		t.Fatal(err)
	}
	migratePlannerPrompt(m.pg)
	cur, _ = m.pg.CurrentPrompt(a.ID)
	if cur != "用户自己改过的正文" {
		t.Fatalf("用户编辑不应被覆盖，得到 %q", cur)
	}
}
