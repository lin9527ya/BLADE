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

// agent_prompts 首次启动就被 seed 成出厂正文。若把它照字面当成「全局覆盖」，模板库的
// 默认模板永远轮不到——这正是四级链写进注释却没实现的漏洞。出厂正文那一版必须视同
// 未覆盖；用户改过（内容不同）才真正压过模板库。
func TestPlannerSystemSeededFactoryPromptDoesNotShadowTemplate(t *testing.T) {
	oldDefault := PlannerTemplateDefault
	oldOverride := PromptOverride
	t.Cleanup(func() { PlannerTemplateDefault = oldDefault; PromptOverride = oldOverride })

	PlannerTemplateDefault = func() (string, bool) { return "模板库默认 {{.Goal}}", true }
	PromptOverride = func(key string) (string, bool) {
		if key != "planner" {
			return "", false
		}
		return plannerDefaultTmpl, true // 就是 seed 进去的那一版
	}

	got := plannerSystem("目标X", "/d", "/w", "")
	if !strings.HasPrefix(got, "模板库默认 目标X") {
		t.Fatalf("出厂正文不应压过模板库默认：%q", got[:min(60, len(got))])
	}
}

func TestPlannerSystemEditedGlobalPromptWinsOverTemplate(t *testing.T) {
	oldDefault := PlannerTemplateDefault
	oldOverride := PromptOverride
	t.Cleanup(func() { PlannerTemplateDefault = oldDefault; PromptOverride = oldOverride })

	PlannerTemplateDefault = func() (string, bool) { return "模板库默认 {{.Goal}}", true }
	PromptOverride = func(key string) (string, bool) {
		if key != "planner" {
			return "", false
		}
		return "人工改过的全局正文 {{.Goal}}", true
	}

	got := plannerSystem("目标X", "/d", "/w", "")
	if !strings.HasPrefix(got, "人工改过的全局正文 目标X") {
		t.Fatalf("改过的全局正文应优先于模板库：%q", got[:min(60, len(got))])
	}
}

// 追加段与潜行纪律不属于任何一级正文：任务快照与模板库默认这两条路径同样要带上，
// 否则静默模式下换一份正文就把操作约束丢了。
func TestPlannerSystemExtrasApplyToEveryBodySource(t *testing.T) {
	oldDefault := PlannerTemplateDefault
	oldOverride := PromptOverride
	oldAppend := PromptAppend
	oldNoise := NoiseLevel
	t.Cleanup(func() {
		PlannerTemplateDefault = oldDefault
		PromptOverride = oldOverride
		PromptAppend = oldAppend
		NoiseLevel = oldNoise
	})
	NoiseLevel = func() string { return "quiet" }
	PromptAppend = func(key string) (string, bool) {
		if key == "planner" {
			return "SCENE", true
		}
		return "", false
	}
	PlannerTemplateDefault = func() (string, bool) { return "模板库默认 {{.Goal}}", true }

	cases := []struct {
		name     string
		override string
		prefix   string
	}{
		{"任务快照", "任务快照 {{.Goal}}", "任务快照 目标X"},
		{"模板库默认", "", "模板库默认 目标X"},
	}
	for _, tc := range cases {
		got := plannerSystem("目标X", "/d", "/w", tc.override)
		if !strings.HasPrefix(got, tc.prefix) {
			t.Fatalf("%s: 正文来源不对：%q", tc.name, got[:min(60, len(got))])
		}
		for _, want := range []string{"SCENE", "潜行纪律", "广度纪律", "中间产物输出规约"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: 缺少平台持有段 %q", tc.name, want)
			}
		}
	}
}

// 模板库里没有默认模板时（全新库 / 用户把默认让给别的之前），planner 必须落到内置正文。
// 这是「不选模板 = 用系统自带 planner」的代码保证：③ 返回 ok=false 就该直接到 ④，
// 而不是留下空正文。
func TestPlannerSystemWithoutDefaultTemplateUsesBuiltin(t *testing.T) {
	oldDefault := PlannerTemplateDefault
	oldOverride := PromptOverride
	t.Cleanup(func() { PlannerTemplateDefault = oldDefault; PromptOverride = oldOverride })

	PromptOverride = nil
	PlannerTemplateDefault = func() (string, bool) { return "", false }

	got := plannerSystem("目标X", "/d", "/w", "")
	if !strings.Contains(got, "你是一个网络安全平台授权渗透测试系统的") {
		t.Fatalf("无默认模板时应落到内置正文：%q", got[:min(80, len(got))])
	}
	if !strings.Contains(got, "任务目标：目标X") {
		t.Fatal("内置正文没有渲染 {{.Goal}}")
	}
	for _, want := range []string{"广度纪律", "战略提示（hints）的采纳纪律", "中间产物输出规约"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少代码持有段 %q", want)
		}
	}
}
