package db

import "testing"

// 心跳下限与提示缓存窗口（约 5 分钟）对齐：10 分钟的心跳意味着纯心跳唤醒必然冷启动，
// 实测 planner 轮首命中率因此只有 40%（worker 89%）。
func TestPlanHeartbeatFloorMatchesCacheWindow(t *testing.T) {
	if MinPlanHeartbeatSeconds != 300 {
		t.Fatalf("心跳下限应为 300s（与缓存窗口对齐），got %d", MinPlanHeartbeatSeconds)
	}
	if got := normalizeHeartbeat(0); got != 300 {
		t.Fatalf("缺省心跳应归一为 300，got %d", got)
	}
	if got := normalizeHeartbeat(900); got != 900 {
		t.Fatalf("显式大值应保留，got %d", got)
	}
}
