package agent

import (
	"strings"
	"testing"
	"time"
)

// 收官期是**软信号**：意图照建，但回执里带 endgame_warning；没有期限时一个字都不提。
// 硬拒会把"收官期还需要清理/补齐的意图"一起挡掉。
func TestAddIntentWarnsInEndgame(t *testing.T) {
	tools, _, _ := newToolWorker(t)

	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"summary":"收官期的清理方向","risk_area":"other"}`)
	if !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("意图没建上：%s", out)
	}
	if strings.Contains(out, "endgame_warning") {
		t.Fatalf("没有时限时不该出现收官期提示：%s", out)
	}

	now := time.Now()
	tools.SetTaskClock(TaskClock{DeadlineUnix: now.Add(5 * time.Minute).Unix(), EndgameUnix: now.Add(-time.Minute).Unix()})
	out = callTool(t, tools.PlannerTools(), "add_intent",
		`{"summary":"收官期的新方向","risk_area":"other"}`)
	if !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("收官期不该拒收意图：%s", out)
	}
	if !strings.Contains(out, "endgame_warning") || !strings.Contains(out, "收官期") {
		t.Fatalf("收官期回执缺少提醒：%s", out)
	}
}

// 批量提交也要带（模型更常用 intents 数组）。
func TestAddIntentBatchWarnsInEndgame(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	now := time.Now()
	tools.SetTaskClock(TaskClock{DeadlineUnix: now.Add(5 * time.Minute).Unix(), EndgameUnix: now.Add(-time.Minute).Unix()})
	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"intents":[{"summary":"批量方向一","risk_area":"other"},{"summary":"批量方向二","risk_area":"other"}]}`)
	if !strings.Contains(out, "endgame_warning") {
		t.Fatalf("批量回执缺少收官期提醒：%s", out)
	}
}

// 收官期纪律是代码持有块：旧库里存的可编辑正文写于这个阶段之前，必须仍然生效。
func TestPlannerSystemHasEndgameDoctrine(t *testing.T) {
	p := plannerSystem("g", "/d", "/w", "")
	for _, want := range []string{
		"【收官期（代码持有）】",
		"不再新开探索面",
		"以收官期为准",
		"mark_artifact_cleared",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("planner 提示缺少收官期纪律 %q", want)
		}
	}
}
