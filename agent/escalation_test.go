package agent

import (
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 升级阶梯是代码持有块：固定表必须对旧库里存的可编辑正文同样生效。
func TestPlannerSystemHasEscalationLadder(t *testing.T) {
	p := plannerSystem("g", "/d", "/w", "")
	for _, want := range []string{
		"【升级阶梯（代码持有）】",
		"必须**明确不同角度**",
		"停止原样重派",
		"get_worker_trace",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("planner 提示缺少升级阶梯 %q", want)
		}
	}
}

// 领取次数要出现在规划者能看见的两个地方：常驻索引与 graph_overview。
func TestAttemptsSurfaceToPlanner(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	claim := func() {
		t.Helper()
		if ok, err := ts.ClaimIntent(intent, "work#1", db.IntentLeaseTTL); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
	}
	claim()
	if ok, err := ts.CompareAndSetIntentState(intent, "running", "open"); err != nil || !ok {
		t.Fatalf("requeue: ok=%v err=%v", ok, err)
	}
	claim() // 第二次

	block := BuildContextIndex(ContextIndexVars{Store: ts})
	if !strings.Contains(block, "第 2 次尝试") {
		t.Fatalf("常驻索引没有显示领取次数：\n%s", block)
	}
	out := callTool(t, tools.PlannerTools(), "graph_overview", `{}`)
	if !strings.Contains(out, `"attempts": 2`) && !strings.Contains(out, `"attempts":2`) {
		t.Fatalf("graph_overview 没有 attempts：\n%s", out)
	}
}
