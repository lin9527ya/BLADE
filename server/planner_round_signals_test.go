package server

import "testing"

// 任务 14 的实测：4 个 worker、31 轮规划里 17 轮 0 意图，槽位长期只跑 1~2 条。
// 这两条判定就是修复的纪律：有空闲槽位却 0 意图 → 补派；连续空转 → 暂停等人工。
func TestPlannerRoundSignals(t *testing.T) {
	// 正常产出：两个计数都清零，不补派、不判停滞。
	if st, rf, refill, stall := plannerRoundSignals(3, 1, 2, 3, true, 2, 1); st != 0 || rf != 0 || refill || stall {
		t.Fatalf("made>0 应清零计数，got streak=%d refills=%d refill=%v stall=%v", st, rf, refill, stall)
	}
	// 0 意图 + 2 个空闲槽 + 无 running/claimable → 补派一次，且累计一轮。
	st, rf, refill, stall := plannerRoundSignals(0, 0, 0, 2, true, 0, 0)
	if !refill || st != 1 || rf != 1 || stall {
		t.Fatalf("应补派并计 1 轮空转，got streak=%d refills=%d refill=%v stall=%v", st, rf, refill, stall)
	}
	// 只有 1 个空闲槽位不补派（避免为一条空槽刷一轮 LLM）。
	if _, _, refill, _ = plannerRoundSignals(0, 3, 0, 1, true, 0, 0); refill {
		t.Fatal("idle<2 不应补派")
	}
	// 还有可领意图时不补派，也不计空转（已有活可干，不是产能缺口）。
	if st, _, refill, stall = plannerRoundSignals(0, 1, 1, 3, true, 2, 0); refill || stall || st != 0 {
		t.Fatalf("有可领意图时不应补派/停滞，got streak=%d refill=%v stall=%v", st, refill, stall)
	}
	// 但仍有空闲槽位、只是 planner 没派满时，补派仍然成立（running 不阻止补派）。
	if _, _, refill, _ = plannerRoundSignals(0, 1, 0, 3, true, 0, 0); !refill {
		t.Fatal("有 running 但仍有 >=2 空闲槽位时也应补派")
	}
	// 没有 open 目标（goalless/已收官）时不补派、不停滞。
	if _, _, refill, stall = plannerRoundSignals(0, 0, 0, 4, false, 2, 0); refill || stall {
		t.Fatal("目标全部了结后不应补派或停滞")
	}
	// 补派上限 2 次：第三次不再补派，但空转计数继续。
	if _, rf, refill, _ = plannerRoundSignals(0, 0, 0, 3, true, 1, 2); refill || rf != 2 {
		t.Fatalf("补派上限应为 2，got refill=%v refills=%d", refill, rf)
	}
	// 连续第 3 轮空转 → 暂停。
	if _, _, _, stall = plannerRoundSignals(0, 0, 0, 2, true, 2, 0); !stall {
		t.Fatal("连续 3 轮空转应触发停滞保护")
	}
}
