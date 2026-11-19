package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// strict 依赖只影响被依赖的意图；没有依赖边的方向必须保持立即可领。
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
	dependent, err := ts.AddIntentGraph(map[string]any{"summary": "用凭据横向", "depends_on": []int64{pre}, "depends_on_mode": "strict"}, 5, nil, "planner", []IntentEdge{{From: 0, To: pre, Rel: RelDependsOn}})
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

// advisory 是默认：依赖只排序，不阻塞领取；前置 open 时下游仍可领。
func TestAdvisoryDependencyNeverBlocks(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("dep-adv-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	pre, err := ts.AddIntent(map[string]any{"summary": "先拿凭据"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := ts.AddIntentGraph(map[string]any{"summary": "用凭据横向", "depends_on": []int64{pre}}, 5, nil, "planner", []IntentEdge{{From: 0, To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := ts.Frontier(20)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, n := range fr {
		seen[n.ID] = true
	}
	if !seen[pre] || !seen[down] {
		t.Fatalf("advisory 下前置 open 时下游仍应可领：frontier=%v pre=%d down=%d", seen, pre, down)
	}
	if n, err := ts.CountClaimableIntents(); err != nil || n != 2 {
		t.Fatalf("claimable 应为 2，got %d err=%v", n, err)
	}
}

// strict 前置进入死状态后必须 fail-open，不能让下游永久卡死。
func TestStrictDependencyFailOpensOnDeadPrerequisite(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("dep-dead-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	for _, state := range []string{"blocked", "exhausted", "stopped", "out-of-scope", "no_output"} {
		pre, err := ts.AddIntent(map[string]any{"summary": "前置 " + state}, 5, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		down, err := ts.AddIntentGraph(map[string]any{"summary": "下游 " + state, "depends_on": []int64{pre}, "depends_on_mode": "strict"}, 5, nil, "planner", []IntentEdge{{From: 0, To: pre, Rel: RelDependsOn}})
		if err != nil {
			t.Fatal(err)
		}
		if err := ts.SetIntentState(pre, state); err != nil {
			t.Fatal(err)
		}
		fr, err := ts.Frontier(50)
		if err != nil {
			t.Fatal(err)
		}
		seen := false
		for _, n := range fr {
			if n.ID == down {
				seen = true
			}
		}
		if !seen {
			t.Fatalf("前置 %s 已终态，strict 下游应 fail-open 可领", state)
		}
	}
}

// strict 依赖在活跃前置期间仍会暂缓派发，这是串行链的显式选择。
func TestStrictDependencyWaitsForActivePrerequisite(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("dep-strict-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	pre, err := ts.AddIntent(map[string]any{"summary": "先拿凭据"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := ts.AddIntentGraph(map[string]any{"summary": "用凭据横向", "depends_on": []int64{pre}, "depends_on_mode": "strict"}, 5, nil, "planner", []IntentEdge{{From: 0, To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := ts.Frontier(20)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range fr {
		if n.ID == down {
			t.Fatal("前置仍 open 时 strict 下游不应可领")
		}
	}
	snap, err := ts.IntentDispatchSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Open != 2 || snap.Claimable != 1 || snap.Blocked != 1 {
		t.Fatalf("snapshot=%+v，want open=2 claimable=1 blocked=1", snap)
	}
	if err := ts.SetIntentState(pre, "done"); err != nil {
		t.Fatal(err)
	}
	fr, err = ts.Frontier(20)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, n := range fr {
		if n.ID == down {
			seen = true
		}
	}
	if !seen {
		t.Fatal("前置 done 后 strict 下游应可领")
	}
}

// 依赖编辑：改模式与拆边必须同时更新图边和 payload。
func TestEditIntentDependencyModeAndDropEdge(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("dep-edit-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	pre, err := ts.AddIntent(map[string]any{"summary": "前置"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := ts.AddIntentGraph(map[string]any{"summary": "下游", "depends_on": []int64{pre}, "depends_on_mode": "strict"}, 5, nil, "planner", []IntentEdge{{From: 0, To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := ts.SetIntentDependencyMode(down, DependencyModeAdvisory, "test mode"); err != nil || !changed {
		t.Fatalf("set mode changed=%v err=%v", changed, err)
	}
	n, err := ts.GetNode(down)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	if p["depends_on_mode"] != DependencyModeAdvisory {
		t.Fatalf("mode not persisted: %+v", p)
	}
	if changed, err := ts.DropIntentDependency(down, pre, "test drop"); err != nil || !changed {
		t.Fatalf("drop dependency changed=%v err=%v", changed, err)
	}
	n, err = ts.GetNode(down)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(n.Payload, &p)
	if deps, _ := p["depends_on"].([]any); len(deps) != 0 {
		t.Fatalf("depends_on payload not cleaned: %+v", p["depends_on"])
	}
	fr, err := ts.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, n := range fr {
		if n.ID == down {
			seen = true
		}
	}
	if !seen {
		t.Fatal("拆依赖后下游应可领")
	}
}

// 依赖阻塞可见性：列出 open strict 意图与仍活跃的前置状态。
func TestListBlockedOpenIntentsExposesBlockers(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("dep-list-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	pre, err := ts.AddIntent(map[string]any{"summary": "前置"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := ts.AddIntentGraph(map[string]any{"summary": "下游", "depends_on": []int64{pre}, "depends_on_mode": "strict"}, 5, nil, "planner", []IntentEdge{{From: 0, To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	list, err := ts.ListBlockedOpenIntents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].IntentID != down || list[0].Summary != "下游" || len(list[0].Blockers) != 1 || list[0].Blockers[0].ID != pre || list[0].Blockers[0].State != "open" {
		t.Fatalf("blocked list mismatch: %+v", list)
	}
	if err := ts.SetIntentState(pre, "done"); err != nil {
		t.Fatal(err)
	}
	list, err = ts.ListBlockedOpenIntents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("前置 done 后不应再列为 blocked：%+v", list)
	}
}

// 看门狗签名：活跃前置/状态变化要改变签名，前置终结后签名清空。
func TestBlockedIntentSignatureTracksActivePrerequisite(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("dep-sig-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	pre, err := ts.AddIntent(map[string]any{"summary": "前置"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := ts.AddIntentGraph(map[string]any{"summary": "下游", "depends_on": []int64{pre}, "depends_on_mode": "strict"}, 5, nil, "planner", []IntentEdge{{From: 0, To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	sig1, err := ts.BlockedIntentSignature()
	if err != nil {
		t.Fatal(err)
	}
	if sig1 == "" || !strings.Contains(sig1, fmt.Sprintf("%d=", down)) || !strings.Contains(sig1, fmt.Sprintf("%d:open", pre)) {
		t.Fatalf("签名应包含 blocked 下游与前置状态：%q", sig1)
	}
	if err := ts.SetIntentState(pre, "running"); err != nil {
		t.Fatal(err)
	}
	sig2, err := ts.BlockedIntentSignature()
	if err != nil {
		t.Fatal(err)
	}
	if sig2 == sig1 || !strings.Contains(sig2, fmt.Sprintf("%d:running", pre)) {
		t.Fatalf("前置状态变化应改变签名：before=%q after=%q", sig1, sig2)
	}
	if err := ts.SetIntentState(pre, "done"); err != nil {
		t.Fatal(err)
	}
	sig3, err := ts.BlockedIntentSignature()
	if err != nil {
		t.Fatal(err)
	}
	if sig3 != "" {
		t.Fatalf("前置 done 后下游已可领，签名应为空：%q", sig3)
	}
}

// advisory 依赖只排序：前置仍活跃时下游不消失，但会排到就绪工作之后。
func TestFrontierForSlotDeprioritizesAdvisoryDownstream(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("dep-order-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	pre, err := ts.AddIntent(map[string]any{"summary": "前置"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := ts.AddIntent(map[string]any{"summary": "就绪的平行方向"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := ts.AddIntentGraph(map[string]any{"summary": "下游", "depends_on": []int64{pre}}, 9, nil, "planner", []IntentEdge{{From: 0, To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := ts.FrontierForSlot("work#1", []string{"work#1"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr) != 3 {
		t.Fatalf("advisory 下游不应被剔除：%d", len(fr))
	}
	if fr[len(fr)-1].ID != down {
		t.Fatalf("advisory 下游应排最后：got %v (pre=%d ready=%d down=%d)", nodeIDs(fr), pre, ready, down)
	}
}

func nodeIDs(ns []*Node) []int64 {
	out := make([]int64, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.ID)
	}
	return out
}

// strict 依赖成环时没有可领意图，但必须能被 snapshot 看见（首轮 kick/看门狗据此
// 唤醒 planner 解阻），不能静默当成"没活"。
func TestStrictDependencyCycleIsBlockedAndVisible(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("dep-cycle-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	a, err := ts.AddIntent(map[string]any{"summary": "A", "depends_on_mode": "strict"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ts.AddIntent(map[string]any{"summary": "B", "depends_on_mode": "strict"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(a, RelDependsOn, b); err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(b, RelDependsOn, a); err != nil {
		t.Fatal(err)
	}
	snap, err := ts.IntentDispatchSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Open != 2 || snap.Claimable != 0 || snap.Blocked != 2 {
		t.Fatalf("cycle snapshot=%+v，want open=2 claimable=0 blocked=2", snap)
	}
	if ok, err := ts.HasDispatchableIntent(); err != nil || ok {
		t.Fatalf("strict cycle 不应可派：ok=%v err=%v", ok, err)
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
