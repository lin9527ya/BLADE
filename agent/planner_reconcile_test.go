package agent

import (
	"strings"
	"testing"
)

// 热会话大多数轮走 delta，但每 N 轮必须回到完整有界视图，让 attention 窗口和
// 落在 delta 字段索引之外的老项重新进入注意力。
func TestPlannerFullReconcileCadence(t *testing.T) {
	p := NewPlanner(nil, "", t.TempDir(), nil, 0, 0)
	for round, want := range map[int]bool{1: true, 4: true, 5: false, 6: true, 10: false} {
		p.hotRounds = round
		if got := p.shouldUseDelta(); got != want {
			t.Fatalf("hotRounds=%d shouldUseDelta=%v want %v", round, got, want)
		}
	}
}

func TestPlannerPromptMentionsAttentionAndPersistence(t *testing.T) {
	body := plannerSystem("g", "/d", "/w")
	for _, want := range []string{
		"attention_oldest_leads", "attention_oldest_facts", "attention_oldest_unresolved",
		"detail_required=true", "进程重启后恢复",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("planner system prompt missing %q", want)
		}
	}
}
