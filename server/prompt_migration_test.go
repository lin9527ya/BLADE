package server

import (
	"os"
	"testing"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
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
		_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingPlannerPromptV3)
		_, _ = m.pg.SavePrompt(a.ID, agent.PlannerPromptBody(), "test cleanup", "test")
	})

	// 其它用例构造 Server 时可能已经跑过迁移并置旗标；本用例显式从"未迁移"开始。
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingPlannerPromptV3)
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

	// ①b v2（首版瘦身）→ 迁移到 v3。
	v2, err := os.ReadFile("../docs/prompts/planner/v2.body.md")
	if err != nil {
		t.Fatalf("读 v2 正文备份: %v", err)
	}
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingPlannerPromptV3)
	if _, err := m.pg.SavePrompt(a.ID, string(v2), "test seed v2", "test"); err != nil {
		t.Fatal(err)
	}
	migratePlannerPrompt(m.pg)
	cur, _ = m.pg.CurrentPrompt(a.ID)
	if cur != agent.PlannerPromptBody() {
		t.Fatalf("v2 正文应迁移到 v3（当前长度 %d）", len(cur))
	}

	// ② 用户编辑过的正文 → 不覆盖（清旗标后重跑）。
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingPlannerPromptV3)
	if _, err := m.pg.SavePrompt(a.ID, "用户自己改过的正文", "user edit", "test"); err != nil {
		t.Fatal(err)
	}
	migratePlannerPrompt(m.pg)
	cur, _ = m.pg.CurrentPrompt(a.ID)
	if cur != "用户自己改过的正文" {
		t.Fatalf("用户编辑不应被覆盖，得到 %q", cur)
	}
}

// 0.9.25 的出厂 planner 正文必须能被升级迁移识别：否则已部署的库会继续使用
// 「同一否定方向至多复核一次」的机械规则，代码里的新协议永远不生效。
func TestMigrateCurrentBuiltinPlannerPrompt(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	a, err := m.pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		t.Fatalf("planner agent: %v", err)
	}
	old, err := os.ReadFile("testdata/prompts/planner-v0.9.25.body.md")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.SavePrompt(a.ID, agent.PlannerPromptBody(), "test cleanup", "test")
	})
	if _, err := m.pg.SavePrompt(a.ID, string(old), "test seed v0.9.25", "test"); err != nil {
		t.Fatal(err)
	}
	migrateExplorationPrompts(m.pg)
	cur, err := m.pg.CurrentPrompt(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur != agent.PlannerPromptBody() {
		t.Fatalf("0.9.25 出厂正文应迁移到本轮正文（当前长度 %d，want %d）", len(cur), len(agent.PlannerPromptBody()))
	}
}

func TestPruneRemovedDomainTaskCategories(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingPruneDomainCategoriesV1)
	})

	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingPruneDomainCategoriesV1)
	keepCat, err := m.pg.CreateTaskCategory("custom-keep")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.pg.DeleteTaskCategory(keepCat.ID) })

	for _, name := range removedDomainTaskCategoryNames {
		if _, err := m.pg.Exec(`INSERT INTO task_categories(name, nkey) VALUES ($1, $1) ON CONFLICT (nkey) DO NOTHING`, name); err != nil {
			t.Fatal(err)
		}
	}
	// Keep one referenced by a live task so we verify referenced rows are preserved.
	var busyID int64
	if err := m.pg.QueryRow(`SELECT id FROM task_categories WHERE nkey=$1`, "云上渗透").Scan(&busyID); err != nil {
		t.Fatal(err)
	}
	task, err := m.pg.CreateTaskWithOptions("prune-cat-guard", "guard", db.TaskCreateOptions{CategoryID: &busyID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = m.pg.DeleteTask(task.ID)
		_, _ = m.pg.DeleteTaskCategory(busyID)
	})

	pruneRemovedDomainTaskCategories(m.pg)

	if cat, err := m.pg.GetTaskCategory(keepCat.ID); err != nil || cat == nil {
		t.Fatalf("custom category should remain: cat=%+v err=%v", cat, err)
	}
	if cat, err := m.pg.GetTaskCategory(busyID); err != nil || cat == nil {
		t.Fatalf("referenced category should remain: cat=%+v err=%v", cat, err)
	}
	for _, name := range removedDomainTaskCategoryNames {
		if name == "云上渗透" {
			continue
		}
		var cnt int
		if err := m.pg.QueryRow(`SELECT count(*) FROM task_categories WHERE nkey=$1`, name).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt != 0 {
			t.Fatalf("unreferenced legacy domain category %q should be deleted", name)
		}
	}

	// Subsequent re-runs stay no-op once marked complete, so user-recreated names survive.
	recreated, err := m.pg.CreateTaskCategory("代码审计")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.pg.DeleteTaskCategory(recreated.ID) })
	pruneRemovedDomainTaskCategories(m.pg)
	if got, err := m.pg.GetTaskCategory(recreated.ID); err != nil || got == nil {
		t.Fatalf("recreated category after migration flag should remain: got=%+v err=%v", got, err)
	}
}
