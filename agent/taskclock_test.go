package agent

import (
	"strings"
	"testing"
	"time"
)

// 倒计时的语义必须精确：没有截止时不报剩余（不能把"没有期限"显示成 0 分钟），
// 收官期只在真正跨过起点后成立。
func TestTaskClockRemainingAndEndgame(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	noDeadline := TaskClock{}
	if _, ok := noDeadline.Remaining(now); ok {
		t.Fatal("没有截止时不该报剩余时间")
	}
	if noDeadline.Endgame(now) {
		t.Fatal("没有收官期时 Endgame 必须为 false")
	}

	tc := TaskClock{DeadlineUnix: now.Add(10 * time.Minute).Unix(), EndgameUnix: now.Add(2 * time.Minute).Unix()}
	if rem, ok := tc.Remaining(now); !ok || rem != 10*time.Minute {
		t.Fatalf("剩余时间 = %v/%v，want 10m", rem, ok)
	}
	if tc.Endgame(now) {
		t.Fatal("还没到收官期起点，不该判为收官期")
	}
	if !tc.Endgame(now.Add(2 * time.Minute)) {
		t.Fatal("到达起点那一刻就是收官期（边界包含）")
	}
	if !tc.Endgame(now.Add(3 * time.Minute)) {
		t.Fatal("起点之后都是收官期")
	}
	// 过了截止仍是收官期（剩余为负），由收尾逻辑决定何时真正停下。
	if rem, ok := tc.Remaining(now.Add(11 * time.Minute)); !ok || rem >= 0 {
		t.Fatalf("过了截止后剩余应为负，得到 %v/%v", rem, ok)
	}
}

// 常驻索引每请求现算：剩余时间要出现；收官期要明说"不再新开探索面"。
func TestContextIndexShowsClockAndEndgame(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	_ = tools
	now := time.Now()
	block := BuildContextIndex(ContextIndexVars{Store: ts,
		Clock: TaskClock{DeadlineUnix: now.Add(42 * time.Minute).Unix()}})
	// 渲染与断言之间可能跨分钟：41/42 都接受。
	if !strings.Contains(block, "任务时限：剩余约 41 分钟") && !strings.Contains(block, "任务时限：剩余约 42 分钟") {
		t.Fatalf("索引缺少剩余时间：\n%s", block)
	}
	block = BuildContextIndex(ContextIndexVars{Store: ts,
		Clock: TaskClock{DeadlineUnix: now.Add(5 * time.Minute).Unix(), EndgameUnix: now.Add(-time.Minute).Unix()}})
	if !strings.Contains(block, "【收官期") || !strings.Contains(block, "不再新开探索面") {
		t.Fatalf("收官期提示缺失：\n%s", block)
	}
}
