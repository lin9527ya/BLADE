package server

import (
	"context"
	"testing"
)

// 有界视图设置：缺省 true（省 token），存 false 后每轮现读为 false。
func TestPlannerBoundedViewSetting(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	t.Cleanup(func() { m.Close() })

	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingPlannerBoundedView) })
	if !s.plannerBoundedView() {
		t.Fatal("默认应为有界视图（true）")
	}
	if err := m.pg.SetSetting(settingPlannerBoundedView, "false"); err != nil {
		t.Fatal(err)
	}
	if s.plannerBoundedView() {
		t.Fatal("存 false 后应读到 false")
	}
}
