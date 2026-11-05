package agent

import (
	"fmt"
	"strings"
	"testing"
)

// 态势里要出现台账：状态计数 + 待处理缺口（含未碰过的资产）。
func TestGraphOverviewIncludesLedger(t *testing.T) {
	ts, _, _, _, _ := breadthToolSet(t)
	out := callTool(t, ts.PlannerTools(), "graph_overview", `{}`)
	for _, want := range []string{`"ledger"`, `"gaps_total"`, "debt.breadth-test.example", `"note"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("态势缺少台账字段 %q：\n%s", want, out)
		}
	}
}

// 台账工具按缺口过滤：gaps_only 只返回 untested/failed 且未豁免的格子。
func TestExplorationLedgerToolGapsOnly(t *testing.T) {
	ts, _, _, _, _ := breadthToolSet(t)
	out := callTool(t, ts.PlannerTools(), "exploration_ledger", `{"gaps_only":true}`)
	if !strings.Contains(out, `"gaps_total"`) || !strings.Contains(out, "debt.breadth-test.example") {
		t.Fatalf("台账工具缺少缺口：\n%s", out)
	}
}

// 豁免：理由太短拒收；合法豁免写库，并在下一次读取时从缺口里消失。
func TestDismissCoverageWaivesGap(t *testing.T) {
	ts, _, _, _, debt := breadthToolSet(t)
	short := callTool(t, ts.PlannerTools(), "dismiss_coverage",
		fmt.Sprintf(`{"asset_id":%d,"risk_area":"sqli","reason":"太短"}`, debt))
	if !strings.Contains(short, "至少 10 个字") {
		t.Fatalf("过短理由应被拒收：%s", short)
	}

	out := callTool(t, ts.PlannerTools(), "dismiss_coverage",
		fmt.Sprintf(`{"asset_id":%d,"risk_area":"sqli","reason":"该接口只读且无参数，注入类不适用"}`, debt))
	if !strings.Contains(out, "coverage waiver recorded") {
		t.Fatalf("豁免没写进去：%s", out)
	}
	// 同一个 ToolSet 内缓存要失效，下一次读到的缺口应少一格。
	ledger := callTool(t, ts.PlannerTools(), "exploration_ledger", `{"asset_id":`+fmt.Sprint(debt)+`,"risk_area":"sqli"}`)
	if !strings.Contains(ledger, `"waived":1`) && !strings.Contains(ledger, `"waived": 1`) {
		t.Fatalf("豁免后台账应标记 waived：\n%s", ledger)
	}
}
