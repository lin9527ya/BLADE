package agent

import (
	"fmt"
	"strings"
	"testing"
)

func withNoise(t *testing.T, level string) {
	t.Helper()
	old := NoiseLevel
	NoiseLevel = func() string { return level }
	t.Cleanup(func() { NoiseLevel = old })
}

// 静默模式下 planner/worker 的提示词末尾会追加**代码持有**的潜行纪律——改 DB 正文也删不掉。
func TestStealthBlockInQuietMode(t *testing.T) {
	withNoise(t, "quiet")
	for _, key := range []string{"planner", "worker"} {
		got := renderSystem(key, "基础提示词", PlannerVars{})
		for _, want := range []string{"潜行纪律", "先最小验证、再扩面", "被检出就换", "不改变边界"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s 静默模式缺少 %q：\n%s", key, want, got)
			}
		}
	}
	// 只动嘴不动手的 agent 不需要（mainagent/goals 没有这套约束）。
	if got := renderSystem("mainagent", "基础提示词", MainVars{}); strings.Contains(got, "潜行纪律") {
		t.Fatalf("对话 agent 不该带潜行纪律：\n%s", got)
	}
}

func TestStealthBlockAbsentInNormalMode(t *testing.T) {
	withNoise(t, "normal")
	if got := renderSystem("planner", "基础提示词", PlannerVars{}); strings.Contains(got, "潜行纪律") {
		t.Fatalf("常规模式不该有潜行纪律：\n%s", got)
	}
	old := NoiseLevel
	NoiseLevel = nil // 未接线 = normal
	t.Cleanup(func() { NoiseLevel = old })
	if got := renderSystem("worker", "基础提示词", WorkerVars{}); strings.Contains(got, "潜行纪律") {
		t.Fatalf("未接线时不该有潜行纪律：\n%s", got)
	}
}

// 检出热点：只列**真的响了**的（unknown 不算），带技术名与命中的资产。
func TestOverviewListsDetectionHotspots(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	for _, body := range []string{
		`{"summary":"扫到 4625 告警","technique":"T1110","result":"triggered","evidence":"日志里看见","confidence":"high"}`,
		`{"summary":"没响","technique":"T1190","result":"not_triggered","evidence":"查过","confidence":"medium"}`,
		`{"summary":"没法判定","technique":"T1078","result":"unknown","evidence":"没有日志源","confidence":"low"}`,
	} {
		if out := callTool(t, tools.WorkerTools(), "report_detection_check", body); !strings.HasPrefix(out, "detection check recorded: ") {
			t.Fatalf("记录检测验证失败：%s", out)
		}
	}
	overview := tools.graphOverviewData()
	hotspots, _ := overview["detection_hotspots"].([]map[string]any)
	if len(hotspots) != 1 {
		t.Fatalf("只该列 triggered 的一条：%#v", overview["detection_hotspots"])
	}
	if hotspots[0]["technique"] != "T1110" {
		t.Fatalf("热点应带技术名：%#v", hotspots[0])
	}
	_ = fmt.Sprint(hotspots)
}
