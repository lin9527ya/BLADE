package server

import (
	"slices"
	"testing"

	"github.com/lin9527ya/blade/toolpolicy"
)

// footprintCommandTools 必须与登记表一致：漏一个工具，痕迹归纳就静默少一类。
func TestFootprintToolsDerivedFromRegistry(t *testing.T) {
	want := toolpolicy.FootprintTools()
	for name := range want {
		if !footprintCommandTools[name] {
			t.Errorf("%s 在 toolpolicy 标了 Footprint，但 footprintCommandTools 没有它", name)
		}
	}
	for name := range footprintCommandTools {
		if !want[name] {
			t.Errorf("%s 在 footprintCommandTools 但没在 toolpolicy 登记 Footprint", name)
		}
	}
}

// 受 agent_loot_exec 控制的工具必须正好是装配时剔除的那一组。
func TestLootExecGatedTools(t *testing.T) {
	want := []string{"enum_privilege", "foothold_exec", "loot_download", "loot_exec", "loot_upload", "tunnel_open"}
	got := toolpolicy.GatedTools("agent_loot_exec")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("gated tools = %v, want %v", got, want)
	}
}
