package agent

import (
	"fmt"
	"strings"
	"testing"
)

// 节点标识只有一种：**全局 id**。曾经叙事用任务内序号、工具参数用全局 id，
// 两套编号同时出现在同一句话里，模型会拿序号去调工具而找错节点（VPS 实测）。
// 这个用例钉住"任何地方都不再出现序号"。
func TestAgentLabelsUseGlobalIDsOnly(t *testing.T) {
	tools, ts, _ := newToolWorker(t) // 已有 1 条意图
	goalID, err := ts.AddGoal(map[string]any{"text": "拿下后台"}, "goals")
	if err != nil {
		t.Fatal(err)
	}
	secondID := lootNodeID(t, callTool(t, tools.PlannerTools(), "add_intent", `{"risk_area":"other","summary":"第二条方向"}`))

	block := BuildContextIndex(ContextIndexVars{Store: ts})
	if !strings.Contains(block, fmt.Sprintf("goal-%d [", goalID)) {
		t.Fatalf("目标应显示全局 id goal-%d：\n%s", goalID, block)
	}
	if !strings.Contains(block, fmt.Sprintf("intent-%d", secondID)) {
		t.Fatalf("意图应显示全局 id intent-%d：\n%s", secondID, block)
	}
	// 序号已被彻底移除：任何 "intent-1" / "goal-1" 这类小序号都不该出现
	// （测试库里的 id 是全局自增的，远大于任务内条数）。
	if strings.Contains(block, "intent-1 ") || strings.Contains(block, "intent-2") {
		t.Fatalf("不该再出现任务内序号：\n%s", block)
	}
	if !strings.Contains(block, "全局 id") {
		t.Fatalf("索引应说明编号口径是全局 id：\n%s", block)
	}

	// 概览里只有 id，没有 seq。
	overview := tools.graphOverviewData()
	goals, _ := overview["goals"].([]map[string]any)
	if len(goals) != 1 || goals[0]["id"] != goalID {
		t.Fatalf("概览的 goals 应带 id：%#v", overview["goals"])
	}
	if _, hasSeq := goals[0]["seq"]; hasSeq {
		t.Fatalf("概览不该再带 seq：%#v", goals[0])
	}
	open, _ := overview["open_intents"].([]map[string]any)
	found := false
	for _, it := range open {
		if it["id"] == secondID {
			found = true
			if _, hasSeq := it["seq"]; hasSeq {
				t.Fatalf("意图不该再带 seq：%#v", it)
			}
		}
	}
	if !found {
		t.Fatalf("open_intents 应包含第二条：%#v", overview["open_intents"])
	}
}
