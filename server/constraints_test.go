package server

import "testing"

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
