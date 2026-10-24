package db

import (
	"testing"
	"time"
)

// 串行链：前置没有结论（done/negative）之前，下游不进 frontier、也领不走。
// 这是 B6 的核心闸门——下游拿不到前置只会空转，而"靠 TodoWrite 记住"重启就没了。
func TestIntentDependenciesGateFrontier(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("intent-deps", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	exp := d.Exploration(task.ExplorationID)

	first, err := exp.AddIntent(map[string]any{"summary": "第一步：拿到入口"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	second, err := exp.AddIntent(map[string]any{"summary": "第二步：用入口的产出"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := exp.Link(second, RelDependsOn, first); err != nil {
		t.Fatal(err)
	}

	frontier := func() map[int64]bool {
		nodes, err := exp.Frontier(50)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]bool{}
		for _, n := range nodes {
			out[n.ID] = true
		}
		return out
	}
	if fr := frontier(); !fr[first] || fr[second] {
		t.Fatalf("前置未满足时下游不该进 frontier：%+v", fr)
	}
	if ok, err := exp.ClaimIntent(second, "work#1", time.Minute); err != nil || ok {
		t.Fatalf("前置未满足时下游不该被领走：ok=%v err=%v", ok, err)
	}
	deps, err := exp.IntentDependencies()
	if err != nil {
		t.Fatal(err)
	}
	if len(deps[second]) != 1 || deps[second][0] != first {
		t.Fatalf("未满足前置应报出来：%+v", deps)
	}

	// 前置卡住（blocked）同样不算有结论。
	if err := exp.SetIntentState(first, "blocked"); err != nil {
		t.Fatal(err)
	}
	if fr := frontier(); fr[second] {
		t.Fatalf("前置 blocked 时下游仍不该进 frontier：%+v", fr)
	}

	// 前置拿到结论 → 下游放行。
	if err := exp.SetIntentState(first, "negative"); err != nil {
		t.Fatal(err)
	}
	if fr := frontier(); !fr[second] {
		t.Fatalf("前置有结论后下游应进 frontier：%+v", fr)
	}
	if ok, err := exp.ClaimIntent(second, "work#1", time.Minute); err != nil || !ok {
		t.Fatalf("前置有结论后下游应可被领走：ok=%v err=%v", ok, err)
	}

	// 拆链：清掉前置后可立刻派发（planner 判定"不等了"的出口）。
	third, err := exp.AddIntent(map[string]any{"summary": "第三步：不等了"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := exp.Link(third, RelDependsOn, first); err != nil {
		t.Fatal(err)
	}
	if n, err := exp.ClearIntentDependencies(third); err != nil || n != 1 {
		t.Fatalf("拆链应删掉 1 条边：n=%d err=%v", n, err)
	}
	if fr := frontier(); !fr[third] {
		t.Fatalf("拆链后应可派发：%+v", fr)
	}
}
