package agent

import (
	"strings"
	"testing"
)

// A task-level snapshot must win over both the DB default template and the
// built-in body, while the code-owned tail blocks stay appended.
func TestPlannerSystemTaskOverrideWins(t *testing.T) {
	oldDefault := PlannerTemplateDefault
	t.Cleanup(func() { PlannerTemplateDefault = oldDefault })
	PlannerTemplateDefault = func() (string, bool) { return "默认模板 {{.Goal}}", true }

	got := plannerSystem("目标X", "/data", "/work", "任务快照 {{.Goal}}")
	if !strings.HasPrefix(got, "任务快照 目标X") {
		t.Fatalf("task override did not win: %q", got[:min(80, len(got))])
	}
	if strings.Contains(got, "默认模板") {
		t.Fatal("default template must not leak when a task override exists")
	}
	if !strings.Contains(got, "广度纪律") {
		t.Fatal("code-owned blocks must still be appended")
	}
}

func TestPlannerSystemFallsBackToDefaultThenBuiltin(t *testing.T) {
	oldDefault := PlannerTemplateDefault
	t.Cleanup(func() { PlannerTemplateDefault = oldDefault })

	PlannerTemplateDefault = func() (string, bool) { return "默认模板 {{.Goal}}", true }
	if got := plannerSystem("目标X", "/d", "/w", ""); !strings.HasPrefix(got, "默认模板 目标X") {
		t.Fatalf("default template not used: %q", got[:min(60, len(got))])
	}

	PlannerTemplateDefault = func() (string, bool) { return "{{.Unknown}}", true }
	if got := plannerSystem("目标X", "/d", "/w", ""); !strings.Contains(got, "规划者") {
		t.Fatal("broken default template must fall back to the built-in body")
	}
}

func TestPlannerSystemBrokenTaskOverrideFallsBack(t *testing.T) {
	oldDefault := PlannerTemplateDefault
	t.Cleanup(func() { PlannerTemplateDefault = oldDefault })
	PlannerTemplateDefault = func() (string, bool) { return "默认模板 {{.Goal}}", true }
	if got := plannerSystem("目标X", "/d", "/w", "{{.Unknown}}"); !strings.HasPrefix(got, "默认模板 目标X") {
		t.Fatalf("broken override should fall to the default template: %q", got[:min(60, len(got))])
	}
}
