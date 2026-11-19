package db

import (
	"fmt"
	"testing"
	"time"
)

// 显式 depends_on 只影响被依赖的意图；没有依赖边的方向必须保持立即可领。
func TestFrontierHonorsOptionalIntentDependencies(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("dep-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	pre, err := ts.AddIntent(map[string]any{"summary": "先拿凭据"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := ts.AddIntentGraph(map[string]any{"summary": "用凭据横向", "depends_on": []int64{pre}}, 5, nil, "planner", []IntentEdge{{From: 0, To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	// 无依赖的意图也必须 ready（不能因为别的意图有依赖而被一起挡住）。
	parallel, err := ts.AddIntent(map[string]any{"summary": "平行侦察"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	frontier, err := ts.Frontier(20)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, n := range frontier {
		got[n.ID] = true
	}
	if !got[pre] || !got[parallel] {
		t.Fatalf("无依赖意图应可领：frontier=%v pre=%d parallel=%d", got, pre, parallel)
	}
	if got[dependent] {
		t.Fatalf("前置未完成时依赖意图不应可领：%v", got)
	}
	if n, err := ts.CountClaimableIntents(); err != nil || n != 2 {
		t.Fatalf("claimable 应只数 ready 的 2 条，got %d err=%v", n, err)
	}

	if _, err := ts.CompareAndSetIntentState(pre, "open", "done"); err != nil {
		t.Fatal(err)
	}
	frontier, err = ts.Frontier(20)
	if err != nil {
		t.Fatal(err)
	}
	got = map[int64]bool{}
	for _, n := range frontier {
		got[n.ID] = true
	}
	if !got[dependent] {
		t.Fatalf("前置完成后依赖意图应可领：frontier=%v dependent=%d", got, dependent)
	}
}

// 黑板过滤查询按 route/kind/state 返回窗口外节点，并给出精确总数。
func TestBlackboardQueryFiltersRouteAndKind(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("bbq-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	want, err := ts.AddIntent(map[string]any{"summary": "route fact", "contract": map[string]any{"route": "auth-chain"}}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ts.AddIntent(map[string]any{"summary": "other route", "contract": map[string]any{"route": "upload-chain"}}, 5, nil, "planner")

	nodes, total, err := ts.QueryBlackboard(BlackboardQuery{Route: "auth-chain", Kind: "intent"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(nodes) != 1 || nodes[0].ID != want {
		t.Fatalf("route 过滤结果不符: total=%d nodes=%+v", total, nodes)
	}
}

// 软调度：同优先级时， underrepresented route 应获得一定排序提升，但 priority 仍主导。
func TestFrontierForSlotPrefersUnderrepresentedRoute(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("sched-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	for i := 0; i < 4; i++ {
		if _, err := ts.AddIntent(map[string]any{"summary": fmt.Sprintf("same route %d", i), "contract": map[string]any{"route": "wide-route"}}, 8, nil, "planner"); err != nil {
			t.Fatal(err)
		}
	}
	rare, err := ts.AddIntent(map[string]any{"summary": "rare route", "contract": map[string]any{"route": "rare-route"}}, 8, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fr, err := ts.FrontierForSlot("work#1", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr) == 0 || fr[0].ID != rare {
		t.Fatalf("underrepresented route 应排前：rare=%d frontier=%+v", rare, idsOfNodes(fr))
	}
}

func idsOfNodes(ns []*Node) []int64 {
	out := make([]int64, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}
