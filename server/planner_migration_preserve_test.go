package server

import (
	"os"
	"testing"

	"github.com/lin9527ya/blade/agent"
)

func TestPlannerStartupMigrationsPreserveUserAppendAndBody(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	a, err := m.pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		t.Fatalf("planner: %v", err)
	}
	original, err := m.pg.CurrentPrompt(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalAppend, err := m.pg.CurrentPromptAppend(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{settingPlannerPromptV3, "planner_prompt_slots_v12"} {
		v, ok, err := m.pg.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if ok {
				_ = m.pg.SetSetting(key, v)
			} else {
				_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, key)
			}
		})
	}
	t.Cleanup(func() { _, _ = m.pg.SavePromptWithAppend(a.ID, original, originalAppend, "test cleanup", "test") })
	old, err := os.ReadFile("../docs/prompts/planner/default.body.md")
	if err != nil {
		t.Fatal(err)
	}
	customAppend := "仅测试用户明确给出的主机，保留这条自定义追加要求。"
	s := &Server{m: m}
	for _, custom := range []bool{false, true} {
		for _, key := range []string{settingPlannerPromptV3, "planner_prompt_slots_v12"} {
			if _, err := m.pg.Exec(`DELETE FROM settings WHERE key=$1`, key); err != nil {
				t.Fatal(err)
			}
		}
		body := string(old)
		if custom {
			body += "\n用户额外规则：不自行增加测试范围。"
		}
		if _, err := m.pg.SavePromptWithAppend(a.ID, body, customAppend, "fixture", "test"); err != nil {
			t.Fatal(err)
		}
		// 与真实启动顺序一致：新 hash 迁移之后，旧 reseed 再运行。
		migratePlannerPrompt(m.pg)
		s.reseedPlannerPrompt()
		got, err := m.pg.CurrentPrompt(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := agent.PlannerPromptBody()
		if custom {
			want = body
		}
		if got != want {
			t.Fatalf("custom=%v: migration overwrote body", custom)
		}
		appendText, err := m.pg.CurrentPromptAppend(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if appendText != customAppend {
			t.Fatalf("custom=%v: append lost: %q", custom, appendText)
		}
		versions, err := m.pg.ListPromptVersions(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		before := len(versions)
		migratePlannerPrompt(m.pg)
		s.reseedPlannerPrompt()
		versions, err = m.pg.ListPromptVersions(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != before {
			t.Fatal("repeat startup created duplicate prompt versions")
		}
	}
}
