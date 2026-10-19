package db

import "testing"

// TestSkillBindingsAlwaysTier pins the 常驻 tier on agent_skill_visibility: bindings
// persist with their flags, AgentAlwaysSkillNames returns only visible+always names
// in stable order, the ≤5 cap is enforced before any write, and the plain
// (no-always) setter keeps working and clears stale flags.
func TestSkillBindingsAlwaysTier(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { d.Close() })
	a, err := d.GetAgentByKey("planner")
	if err != nil || a == nil {
		t.Fatalf("planner agent: %+v err=%v", a, err)
	}
	t.Cleanup(func() { _ = d.SetAgentSkillVisibility(a.ID, nil) }) // 清空恢复

	if err := d.SetAgentSkillBindings(a.ID, []SkillBinding{
		{Name: "gamma", Always: true},
		{Name: "beta"},
		{Name: "alpha", Always: true},
	}); err != nil {
		t.Fatal(err)
	}
	always, err := d.AgentAlwaysSkillNames(a.ID)
	if err != nil || len(always) != 2 || always[0] != "alpha" || always[1] != "gamma" {
		t.Fatalf("always must be visible+always in name order: %v err=%v", always, err)
	}
	names, err := d.AgentSkillNames(a.ID)
	if err != nil || len(names) != 3 {
		t.Fatalf("visible=%v err=%v", names, err)
	}

	// 上限：6 条常驻 → 拒绝，且不落库（校验在任何写入之前）。
	over := make([]SkillBinding, 0, 6)
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		over = append(over, SkillBinding{Name: n, Always: true})
	}
	if err := d.SetAgentSkillBindings(a.ID, over); err == nil {
		t.Fatal("≤5 cap must be enforced")
	}
	if names, _ := d.AgentSkillNames(a.ID); len(names) != 3 {
		t.Fatalf("rejected write must not touch rows: %v", names)
	}

	// 旧入口（无常驻标记）照常工作，并把残留的 always 清掉。
	if err := d.SetAgentSkillVisibility(a.ID, []string{"delta"}); err != nil {
		t.Fatal(err)
	}
	if always, _ := d.AgentAlwaysSkillNames(a.ID); len(always) != 0 {
		t.Fatalf("plain setter must clear always flags: %v", always)
	}
}
