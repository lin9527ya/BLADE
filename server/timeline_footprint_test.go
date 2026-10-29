package server

import "testing"

func TestTimelineFootprintLabels(t *testing.T) {
	cases := map[string]string{
		"open":      "未处置",
		"unknown":   "未确认",
		"accepted":  "接受风险",
		"cleaned":   "已清理",
		"not_found": "未发现",
	}
	for state, want := range cases {
		if got := timelineStateLabel(tlKindFootprint, state); got != want {
			t.Errorf("状态 %s 标签 = %q，应为 %q", state, got, want)
		}
	}
	if got := timelineKindLabel(tlKindFootprint); got != "痕迹" {
		t.Errorf("kind 标签 = %q", got)
	}
	found := false
	for _, k := range timelineMilestoneKinds {
		if k == tlKindFootprint {
			found = true
		}
	}
	if !found {
		t.Error("footprint 应进入默认里程碑")
	}
}
