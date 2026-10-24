package server

import (
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
)

func toolNamed(tools []actool.CoreTool, name string) bool {
	for _, t := range tools {
		if t.Name() == name {
			return true
		}
	}
	return false
}

// loot_exec 未启用时要从工具面移除，而不是留一个永远拒绝的工具让模型反复去试。
func TestDropToolByNameRemovesOnlyThatTool(t *testing.T) {
	ts := agent.NewToolSet(nil, "")
	tools := ts.PlannerTools()
	if !toolNamed(tools, "loot_exec") {
		t.Fatal("前置：PlannerTools 应包含 loot_exec")
	}
	before := len(tools)

	got := dropToolByName(tools, "loot_exec")
	if toolNamed(got, "loot_exec") {
		t.Fatal("loot_exec 应被移除")
	}
	if len(got) != before-1 {
		t.Fatalf("只该少一个工具：before=%d after=%d", before, len(got))
	}
	if !toolNamed(got, "run_execution") {
		t.Fatal("其它执行类工具不该被连坐")
	}

	// 名字不存在时保持原样（不改长度，也不 panic）。
	if same := dropToolByName(got, "不存在的工具"); len(same) != len(got) {
		t.Fatalf("移除不存在的工具不该改变长度：%d → %d", len(got), len(same))
	}
}
