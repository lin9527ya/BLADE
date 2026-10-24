package agent

import (
	"fmt"
	"strings"
	"testing"
)

// 叙事里的 intent-N / goal-N 用**任务内序号**（新任务从 1 开始），机器寻址仍用全局 id。
func TestAgentLabelsUseTaskLocalOrdinals(t *testing.T) {
	tools, ts, _ := newToolWorker(t) // 已有 1 条意图（seq=1）
	goalID, err := ts.AddGoal(map[string]any{"text": "拿下后台"}, "goals")
	if err != nil {
		t.Fatal(err)
	}
	secondID := lootNodeID(t, callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"第二条方向"}`))

	block := BuildContextIndex(ContextIndexVars{Store: ts})
	if !strings.Contains(block, "goal-1 [") {
		t.Fatalf("目标应显示任务内序号 goal-1：\n%s", block)
	}
	if !strings.Contains(block, "intent-2") {
		t.Fatalf("意图应显示任务内序号 intent-2：\n%s", block)
	}
	if strings.Contains(block, fmt.Sprintf("goal-%d", goalID)) && goalID != 1 {
		t.Fatalf("叙事里不该出现全局 goal id：\n%s", block)
	}
	if strings.Contains(block, fmt.Sprintf("intent-%d", secondID)) && secondID != 2 {
		t.Fatalf("叙事里不该出现全局 intent id：\n%s", block)
	}
	if !strings.Contains(block, "序号") {
		t.Fatalf("索引应说明编号口径：\n%s", block)
	}

	// 概览同时给 id（工具寻址）与 seq（叙事指代）。
	overview := tools.graphOverviewData()
	goals, _ := overview["goals"].([]map[string]any)
	if len(goals) != 1 || goals[0]["seq"] != int64(1) || goals[0]["id"] != goalID {
		t.Fatalf("概览的 goals 应带 seq 与 id：%#v", overview["goals"])
	}
	open, _ := overview["open_intents"].([]map[string]any)
	found := false
	for _, it := range open {
		if it["id"] == secondID {
			found = true
			if it["seq"] != int64(2) {
				t.Fatalf("被拒方向的 seq 应为 2：%#v", it)
			}
		}
	}
	if !found {
		t.Fatalf("open_intents 应包含第二条：%#v", overview["open_intents"])
	}
}

// 防打转的拒绝文案：叙事用序号，但要求调工具时给出可寻址的全局 id。
func TestDedupMessageUsesOrdinalPlusMachineID(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	first := lootNodeID(t, callTool(t, tools.PlannerTools(), "add_intent",
		`{"summary":"探测 /admin 后台登录口的 SQL 注入","risk_area":"sqli"}`))
	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"summary":"探测 admin 后台登录口的 SQL 注入","risk_area":"sqli"}`)
	if !strings.Contains(out, "intent-2") {
		t.Fatalf("拒绝文案应用任务内序号：%s", out)
	}
	if !strings.Contains(out, fmt.Sprintf("intent_id=%d", first)) {
		t.Fatalf("要调工具的那处应给出全局 id：%s", out)
	}
	_ = ts
}
