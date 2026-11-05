package agent

import (
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 前置已经是死态时，新建 depends_on 要当场拒收——否则这条链会永远进不了 frontier。
func TestAddIntentRejectsDeadDependency(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	if ok, err := ts.CompareAndSetIntentState(intent, "open", "exhausted"); err != nil || !ok {
		t.Fatalf("settle upstream: ok=%v err=%v", ok, err)
	}
	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"summary":"提权：用上游的 shell 读 /etc/shadow","risk_area":"other","depends_on":[`+strconv.FormatInt(intent, 10)+`]}`)
	if !strings.Contains(out, "永远跑不起来") || !strings.Contains(out, "exhausted") {
		t.Fatalf("应拒收并说明前置已死：%s", out)
	}
}

// 态势要把"永远等不到"的排队意图摆出来（open_intents 窗口外的也靠这个总数）。
func TestGraphOverviewExposesStrandedDependents(t *testing.T) {
	tools, ts, upstream := newToolWorker(t)
	downstream, err := ts.AddIntent(map[string]any{"summary": "下游：提权"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(downstream, db.RelDependsOn, upstream); err != nil {
		t.Fatal(err)
	}
	if ok, err := ts.CompareAndSetIntentState(upstream, "open", "blocked"); err != nil || !ok {
		t.Fatalf("settle upstream: ok=%v err=%v", ok, err)
	}
	out := callTool(t, tools.PlannerTools(), "graph_overview", `{}`)
	if !strings.Contains(out, `"stranded_dependents_total": 1`) && !strings.Contains(out, `"stranded_dependents_total":1`) {
		t.Fatalf("态势缺少 stranded_dependents_total：\n%s", out)
	}
	if !strings.Contains(out, `"dead_deps"`) {
		t.Fatalf("态势缺少 dead_deps：\n%s", out)
	}
}
