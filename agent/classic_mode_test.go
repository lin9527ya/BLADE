package agent

import (
	"strings"
	"testing"
)

func classicConstraints() Overrides {
	return Overrides{ConstraintEnabled: func(string) bool { return false }}
}

// 经典模式：planner 提示不含补位/广度/台账/复盘/开局等约束块，但保留安全/契约块。
func TestPlannerPromptClassicOmitsConstraintBlocks(t *testing.T) {
	WithOverrides(classicConstraints(), func() {
		got := plannerSystem("目标", "/d", "/w", "")
		for _, absent := range []string{
			"【槽位与队列深度（代码持有）】", "【广度纪律（代码持有", "【探索台账（代码持有",
			"【通路台账（代码持有", "【开局摸底（代码持有", "【复盘轮（代码持有）】",
			"【升级阶梯（代码持有）】", "【收官期（代码持有）】",
		} {
			if strings.Contains(got, absent) {
				t.Fatalf("经典 planner 提示不应包含 %q", absent)
			}
		}
		for _, present := range []string{"意图摘要写法", "探索约束开关", "不可信数据", "出口代理", "中间产物输出规约", "混合版规划骨架", "防停摆硬底线"} {
			if !strings.Contains(got, present) {
				t.Fatalf("经典 planner 提示应保留 %q", present)
			}
		}
	})
}

// 经典模式：planner 工具面不注册记账/补证/穷尽工具，能力工具保留。
func TestPlannerToolsClassicOmitsConstraintTools(t *testing.T) {
	WithOverrides(classicConstraints(), func() {
		names := map[string]bool{}
		for _, tool := range NewToolSet(nil, "planner").PlannerTools() {
			names[tool.Name()] = true
		}
		for _, banned := range []string{
			"record_probe_fact", "exploration_ledger", "chain_detail", "dismiss_coverage",
			"coverage_matrix", "accept_completion_risk", "mark_strategy_exhausted", "dismiss_leads",
		} {
			if names[banned] {
				t.Fatalf("经典 planner 工具面不应包含 %q", banned)
			}
		}
		for _, keep := range []string{"add_intent", "prove_goal", "abandon_goal", "accept_artifact_risk", "lookup_cves"} {
			if !names[keep] {
				t.Fatalf("经典 planner 工具面应保留能力/安全工具 %q", keep)
			}
		}
	})
}

// 经典模式：态势不输出补位/广度/台账/线索字段，避免模型被记账材料牵着走。
func TestGraphOverviewClassicOmitsConstraintFields(t *testing.T) {
	WithOverrides(classicConstraints(), func() {
		ts, _, _, _, _ := breadthToolSet(t)
		out := ts.graphOverviewData()
		for _, k := range []string{
			"breadth", "ledger", "chains", "scope_surface", "intent_queue",
			"recent_leads", "unconsumed_facts", "capabilities",
		} {
			if _, ok := out[k]; ok {
				t.Fatalf("经典态势不应包含 %q", k)
			}
		}
		if _, ok := out["goals"]; !ok {
			t.Fatal("经典态势仍应保留 goals")
		}
	})
}

// 经典模式：risk_area/kind 非法只警告并回落，不再拒收整条意图。
func TestAddIntentClassicSoftWarnings(t *testing.T) {
	WithOverrides(classicConstraints(), func() {
		ts, _, _, _, _ := breadthToolSet(t)
		out := callTool(t, ts.PlannerTools(), "add_intent",
			`{"summary":"对 https://debt.breadth-test.example/admin 做认证面测试","risk_area":"not-a-risk","kind":"bogus"}`)
		if !strings.Contains(out, "intent created") {
			t.Fatalf("经典模式应创建意图：%s", out)
		}
		if !strings.Contains(out, "risk_warning") || !strings.Contains(out, "kind_warning") {
			t.Fatalf("经典模式应返回 risk/kind 软警告：%s", out)
		}
	})
}

// 增强模式不应再叠加混合版骨架（增强模式有自己的槽位/广度/台账/复盘块）。
func TestPlannerPromptEnhancedOmitsHybridBlock(t *testing.T) {
	if strings.Contains(plannerSystem("g", "/d", "/w", ""), "混合版规划骨架") {
		t.Fatal("增强模式不应包含混合版规划骨架")
	}
}

// 经典混合版：一次提交超过 4 条只提醒，不拒收。
func TestAddIntentClassicBatchSoftCap(t *testing.T) {
	WithOverrides(classicConstraints(), func() {
		tools, _, _ := newToolWorker(t)
		out := callTool(t, tools.PlannerTools(), "add_intent", `{"intents":[
			{"summary":"对 /a 测 SQL 注入","risk_area":"sqli"},
			{"summary":"对 /b 测 XSS","risk_area":"xss"},
			{"summary":"对 /c 测 SSRF","risk_area":"ssrf"},
			{"summary":"对 /d 测越权","risk_area":"idor"},
			{"summary":"对 /e 测上传","risk_area":"upload"}
		]}`)
		if !strings.Contains(out, "batch_warning") {
			t.Fatalf("5 条批量应返回 batch_warning：%s", out)
		}
		if !strings.Contains(out, `"ids"`) {
			t.Fatalf("批量仍应创建意图：%s", out)
		}
	})
}

// 混合版硬底线必须同时出现在系统骨架和每轮 user 输入里；增强模式不覆盖。
func TestPlannerHardFloorHybrid(t *testing.T) {
	WithOverrides(classicConstraints(), func() {
		if got := plannerHardFloor(nil, true); !strings.Contains(got, "混合版硬底线") || !strings.Contains(got, "必须至少产出 1 条") {
			t.Fatalf("经典混合模式硬底线错误: %s", got)
		}
	})
	if got := plannerHardFloor(nil, true); strings.Contains(got, "混合版硬底线") {
		t.Fatalf("增强模式不应使用混合版硬底线: %s", got)
	}
}
