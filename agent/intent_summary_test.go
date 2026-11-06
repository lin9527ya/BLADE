package agent

import (
	"strings"
	"testing"
)

// TestPlannerSystemForbidsDisciplineSections 守住“意图摘要只写入口+动作+验证目标”：
// planner 曾把操作边界/速率/UA/请求上限/与其它意图不重叠写成“⑦纪律”段落塞进
// add_intent.summary，worker 会把它当意图正文读走。代码持有段必须始终追加。
func TestPlannerSystemForbidsDisciplineSections(t *testing.T) {
	got := plannerSystem("目标", "/d", "/w", "自定义正文")
	for _, want := range []string{
		"意图摘要写法（代码持有，所有模式生效）",
		"⑦纪律",
		"请求上限",
		"不要用长列表引用",
		"操作者在任务约束里声明",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("plannerSystem missing intent-summary discipline rule %q", want)
		}
	}
}

// TestAddIntentDescriptionForbidsDisciplineSections 让工具描述与系统提示同口径：
// 模型即使只看 add_intent 的 schema，也不会再把纪律段写进 summary。
func TestAddIntentDescriptionForbidsDisciplineSections(t *testing.T) {
	desc := NewToolSet(nil, "planner").addIntent().Description()
	for _, want := range []string{"不写纪律", "请求上限", "操作边界"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("add_intent description missing %q: %s", want, desc)
		}
	}
}
