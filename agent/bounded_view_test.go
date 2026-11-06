package agent

import (
	"strings"
	"testing"
)

// 有界视图开关：默认裁剪（带 blackboard 元信息），关掉后 graph_overview 默认返回完整态势。
func TestGraphOverviewBoundedViewSwitch(t *testing.T) {
	tools, _, _ := newToolWorker(t)

	bounded := callTool(t, tools.PlannerTools(), "graph_overview", `{}`)
	if !strings.Contains(bounded, `"blackboard"`) || !strings.Contains(bounded, `"totals"`) {
		t.Fatalf("默认应返回有界视图：\n%s", bounded)
	}

	tools.SetBoundedView(func() bool { return false })
	full := callTool(t, tools.PlannerTools(), "graph_overview", `{}`)
	if strings.Contains(full, `"blackboard"`) {
		t.Fatalf("关闭后不应再有有界视图元信息：\n%s", full)
	}
	for _, want := range []string{`"goals"`, `"open_intents"`, `"intents_total"`} {
		if !strings.Contains(full, want) {
			t.Fatalf("完整态势缺少 %q：\n%s", want, full)
		}
	}

	// 即使开着有界视图，detail=true 依旧显式返回全量。
	tools.SetBoundedView(func() bool { return true })
	detail := callTool(t, tools.PlannerTools(), "graph_overview", `{"detail":true}`)
	if strings.Contains(detail, `"blackboard"`) {
		t.Fatalf("detail=true 应返回完整态势：\n%s", detail)
	}
}
