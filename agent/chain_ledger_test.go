package agent

import (
	"strings"
	"testing"
)

// 态势要出现通路台账；未动用的战果要标成 unused 并给出下一步建议。
func TestGraphOverviewChainsAndDetail(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	out := callTool(t, tools.WorkerTools(), "report_foothold",
		`{"risk_area":"other","summary":"web01 上的 root 反弹 shell","access":"反弹 shell","privilege":"root",`+
			`"evidence":"nc -lvnp 4444 → connect","confidence":"high"}`)
	if !strings.HasPrefix(out, "foothold recorded: ") {
		t.Fatalf("据点没记上：%s", out)
	}

	over := callTool(t, tools.PlannerTools(), "graph_overview", `{}`)
	for _, want := range []string{`"chains"`, `"unused"`, `"stage"`} {
		if !strings.Contains(over, want) {
			t.Fatalf("态势缺少通路字段 %q：\n%s", want, over)
		}
	}

	detail := callTool(t, tools.PlannerTools(), "chain_detail", `{}`)
	for _, want := range []string{`"next_steps"`, "还没被任何意图动用", `"stage":"privilege"`} {
		if !strings.Contains(detail, want) {
			t.Fatalf("chain_detail 缺少 %q：\n%s", want, detail)
		}
	}
}
