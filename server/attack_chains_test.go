package server

import (
	"strings"
	"testing"

	"github.com/lin9527ya/artex/db"
)

// 攻击路径重建是纯函数（只读边与标签表），所以这些用例不连库。
//
// 它守的是分析里那句「报告里的提权/横向只能靠文案」：两种边早就记在图里了，能不能把它们
// 串成一条读得出来的路，全在这个函数里。

// 一条两步链：据点 A --(intent 10)--> 凭据 B --(intent 20)--> 据点 C。
func TestBuildAttackChainsTwoSteps(t *testing.T) {
	edges := []db.Edge{
		{From: 10, Rel: db.RelUses, To: 1},   // intent10 动用据点A
		{From: 10, Rel: db.RelYields, To: 2}, // intent10 产出凭据B
		{From: 20, Rel: db.RelUses, To: 2},   // intent20 动用凭据B
		{From: 20, Rel: db.RelYields, To: 3}, // intent20 产出据点C
	}
	labels := map[int64]string{1: "据点A", 2: "凭据B", 3: "据点C"}
	summaries := map[int64]string{10: "读配置拿到凭据", 20: "用凭据横到下一台"}
	states := map[int64]string{10: "done", 20: "done"}
	techs := map[int64][]string{10: {"T1552.001"}, 20: {"T1021.004"}}

	chains := buildAttackChains(edges, labels, summaries, states, techs)
	if len(chains) != 1 {
		t.Fatalf("应重建出 1 条路径，得到 %d：%+v", len(chains), chains)
	}
	c := chains[0]
	if c.Head != "据点A" {
		t.Errorf("起点应是没有被任何一步产出的那个战果，得到 %q", c.Head)
	}
	if len(c.Steps) != 2 {
		t.Fatalf("该有 2 跳，得到 %d：%+v", len(c.Steps), c.Steps)
	}
	if c.Steps[0].IntentID != 10 || len(c.Steps[0].Produced) != 1 || c.Steps[0].Produced[0] != "凭据B" {
		t.Errorf("第 1 跳不对：%+v", c.Steps[0])
	}
	if c.Steps[1].IntentID != 20 || c.Steps[1].Produced[0] != "据点C" {
		t.Errorf("第 2 跳不对：%+v", c.Steps[1])
	}
	// 技术标注要跟着跳走：「用 T1552.001 读了凭据」才是一句完整的话。
	if len(c.Steps[0].Techniques) != 1 || c.Steps[0].Techniques[0] != "T1552.001" {
		t.Errorf("第 1 跳的 ATT&CK 标注丢了：%+v", c.Steps[0])
	}
	if len(c.Steps[1].Techniques) != 1 || c.Steps[1].Techniques[0] != "T1021.004" {
		t.Errorf("第 2 跳的 ATT&CK 标注丢了：%+v", c.Steps[1])
	}
}

// 一步都没走出去（起点被用了一下，但什么也没产出）不算路径——那是战果段里已经写过的
// 「动用于」那一行，不该在这里重复一遍。
func TestBuildAttackChainsIgnoresDeadEnd(t *testing.T) {
	edges := []db.Edge{
		{From: 10, Rel: db.RelUses, To: 1},
	}
	chains := buildAttackChains(edges,
		map[int64]string{1: "据点A"}, map[int64]string{10: "试了试"}, map[int64]string{10: "done"}, nil)
	if len(chains) != 0 {
		t.Fatalf("没走出去的起点不该成路径：%+v", chains)
	}
}

// 中途没善终、又什么都没产出的意图不算路径上的一跳：它没走过去。
func TestBuildAttackChainsSkipsIntentsThatDiedWithoutOutput(t *testing.T) {
	edges := []db.Edge{
		{From: 10, Rel: db.RelUses, To: 1},
		{From: 10, Rel: db.RelYields, To: 2},
		{From: 20, Rel: db.RelUses, To: 2}, // 用了凭据B 但 blocked 且无产出
	}
	chains := buildAttackChains(edges,
		map[int64]string{1: "据点A", 2: "凭据B"},
		map[int64]string{10: "读配置", 20: "横到下一台"},
		map[int64]string{10: "done", 20: "blocked"}, nil)
	if len(chains) != 1 || len(chains[0].Steps) != 1 {
		t.Fatalf("blocked 且无产出的那一跳不该出现：%+v", chains)
	}
}

// 但**产出了东西就不一样**：哪怕那条意图中途被预算掐断，产出是真的，这一跳要留。
// 与 LootConsumingIntentState 同一条精神：如实，而不是图省事。
func TestBuildAttackChainsKeepsPartialProgress(t *testing.T) {
	edges := []db.Edge{
		{From: 10, Rel: db.RelUses, To: 1},
		{From: 10, Rel: db.RelYields, To: 2},
		{From: 20, Rel: db.RelUses, To: 2},
		{From: 20, Rel: db.RelYields, To: 3},
	}
	chains := buildAttackChains(edges,
		map[int64]string{1: "据点A", 2: "凭据B", 3: "据点C"},
		map[int64]string{10: "读配置", 20: "横到下一台"},
		map[int64]string{10: "done", 20: "exhausted"}, nil)
	if len(chains) != 1 || len(chains[0].Steps) != 2 {
		t.Fatalf("中途被掐断但确有产出的那一跳该保留：%+v", chains)
	}
}

// 一个战果被两条不同路径用到时，两条路径都要能读出来（不是二选一）。
func TestBuildAttackChainsBranches(t *testing.T) {
	edges := []db.Edge{
		{From: 10, Rel: db.RelUses, To: 1}, // 起点A 被 intent10 用
		{From: 10, Rel: db.RelYields, To: 2},
		{From: 20, Rel: db.RelUses, To: 1}, // 同一起点又被 intent20 用
		{From: 20, Rel: db.RelYields, To: 3},
	}
	states := map[int64]string{10: "done", 20: "done"}
	summaries := map[int64]string{10: "往东走", 20: "往西走"}
	chains := buildAttackChains(edges,
		map[int64]string{1: "据点A", 2: "凭据B", 3: "据点C"}, summaries, states, nil)
	if len(chains) != 1 {
		t.Fatalf("一个起点该汇总成一条路径，得到 %d 条", len(chains))
	}
	got := map[int64]bool{}
	for _, s := range chains[0].Steps {
		got[s.IntentID] = true
	}
	if !got[10] || !got[20] {
		t.Fatalf("两条分支都该在路径里：%+v", chains[0].Steps)
	}
	// 不能因为两条边都指向同一个起点就把起点重复算两条路径。
	for _, c := range chains {
		if strings.Count(c.Head, "据点A") != 1 || len(chains) != 1 {
			t.Fatalf("起点被重复了：%+v", chains)
		}
	}
}

// 环不能让它死循环（seen 去重是唯一保障）。
func TestBuildAttackChainsCycleSafe(t *testing.T) {
	edges := []db.Edge{
		{From: 10, Rel: db.RelUses, To: 1},
		{From: 10, Rel: db.RelYields, To: 2},
		{From: 20, Rel: db.RelUses, To: 2},
		{From: 20, Rel: db.RelYields, To: 1}, // 绕回起点
	}
	chains := buildAttackChains(edges,
		map[int64]string{1: "据点A", 2: "凭据B"},
		map[int64]string{10: "a", 20: "b"},
		map[int64]string{10: "done", 20: "done"}, nil)
	// 起点A 是被 intent20 产出的 → 不再是起点，所以这里没有 root，路径为空；
	// 关键是它必须**返回**，而不是转圈。
	if len(chains) != 0 {
		t.Fatalf("成环时没有「没被产出的起点」，该返回空：%+v", chains)
	}
}
