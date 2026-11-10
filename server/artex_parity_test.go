package server

import (
	"testing"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

func TestARTEXPParityPlannerPromptMigration(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	a, err := pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		t.Skip("planner agent missing")
	}
	prev, err := pg.CurrentPrompt(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if prev != "" {
			_, _ = pg.SavePrompt(a.ID, prev, "test restore", "test")
		}
		_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, "planner_prompt_artex_parity_v1")
	})
	if _, err := pg.SavePrompt(a.ID, "旧 BLADE 约束正文", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, "planner_prompt_artex_parity_v1"); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}}
	s.reseedPlannerPrompt()
	cur, err := pg.CurrentPrompt(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := agent.BuiltinPromptSeeds()["planner"]
	if cur != want {
		t.Fatalf("planner prompt was not switched to ARTEX default")
	}
	if v, _, _ := pg.GetSetting("planner_prompt_artex_parity_v1"); v != "true" {
		t.Fatalf("migration flag not set")
	}
}
