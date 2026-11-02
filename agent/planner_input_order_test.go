package agent

import (
	"strings"
	"testing"
)

// 规划轮输入的**分段顺序**必须是「稳定 → 易变」：缓存按 token 前缀匹配，把每轮都变的
// 态势快照放在最前面，第一条消息从开头就分叉，整段前缀都吃不到缓存（实测 planner 轮首
// 命中率 40%，worker 89%）。
func TestComposePlannerInputStableFirst(t *testing.T) {
	staticRules := "据下面的态势，判定目标。目标已【真正达成】时用 prove_goal 逐个标记。硬底线：……"
	lead := "本轮是定时巡检（心跳到点）的唤醒："
	volatile := "【本轮态势（graph_overview 预取）】：{\"facts\":1}"
	todos := "待办：1. 补测 /admin"

	input := composePlannerInput(staticRules, lead, volatile, todos)

	if !strings.HasPrefix(input, staticRules) {
		t.Fatalf("静态规则必须在最前：%s", input[:80])
	}
	iRules := strings.Index(input, staticRules)
	iLead := strings.Index(input, lead)
	iVol := strings.Index(input, "本轮态势")
	iTodo := strings.Index(input, "待办：")
	if !(iRules < iLead && iLead < iVol && iVol < iTodo) {
		t.Fatalf("顺序应为 规则(%d) → 开场白(%d) → 态势(%d) → 待办(%d)", iRules, iLead, iVol, iTodo)
	}
}

// 同一类唤醒、同一策略下，两轮输入的前缀（规则 + 开场白）必须逐字一致，否则缓存仍会
// 在第一个字节处分叉。
func TestComposePlannerInputPrefixStableAcrossRounds(t *testing.T) {
	staticRules := "规则段：判定口径 + 硬底线"
	lead := "本轮是定时巡检（心跳到点）的唤醒："
	a := composePlannerInput(staticRules, lead, "【本轮态势】第一轮", "待办 A")
	b := composePlannerInput(staticRules, lead, "【本轮态势】第二轮", "待办 B")

	prefix := staticRules + "\n\n" + lead
	if !strings.HasPrefix(a, prefix) || !strings.HasPrefix(b, prefix) {
		t.Fatal("两轮输入必须以同一段稳定前缀开头")
	}
	if a == b {
		t.Fatal("态势与待办变化时，两轮输入不应完全相同")
	}
}
