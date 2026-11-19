package db

import "testing"

// ARTEX 的 frontier 不做依赖硬过滤。BLADE 保留 depends_on，但默认 advisory：
// 所有 open 都可领；只有逐意图 strict 才在活跃前置期间暂缓，且前置进入任何终态
// 都会 fail-open。这里固定这条与 ARTEX 对齐、又不产生永久死锁的语义。
func TestFrontierHonorsExplicitDependencies(t *testing.T) {
	_, s := intentLeaseFixture(t)
	upstream, err := s.AddIntent(map[string]any{"summary": "upstream"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetIntentState(upstream, "exhausted"); err != nil {
		t.Fatal(err)
	}
	advisory, err := s.AddIntentGraph(map[string]any{
		"summary": "advisory downstream", "depends_on": []int64{upstream},
	}, 5, nil, "planner", []IntentEdge{{From: 0, To: upstream, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	strict, err := s.AddIntentGraph(map[string]any{
		"summary": "strict downstream", "depends_on": []int64{upstream}, "depends_on_mode": "strict",
	}, 5, nil, "planner", []IntentEdge{{From: 0, To: upstream, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	frontier, err := s.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, n := range frontier {
		seen[n.ID] = true
	}
	if !seen[advisory] || !seen[strict] {
		t.Fatal("前置 exhausted 时 advisory 与 strict 下游都应 fail-open 可领")
	}
	if n, err := s.CountClaimableIntents(); err != nil || n != 2 {
		t.Fatalf("claimable=%d err=%v, want 2", n, err)
	}

	// 活跃前置仍能挡住 strict，这是显式串行选择；advisory 不挡。
	active, err := s.AddIntent(map[string]any{"summary": "active upstream"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := s.AddIntentGraph(map[string]any{
		"summary": "strict waiting", "depends_on": []int64{active}, "depends_on_mode": "strict",
	}, 5, nil, "planner", []IntentEdge{{From: 0, To: active, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	frontier, err = s.Frontier(20)
	if err != nil {
		t.Fatal(err)
	}
	seen = map[int64]bool{}
	for _, n := range frontier {
		seen[n.ID] = true
	}
	if !seen[active] || seen[waiting] {
		t.Fatalf("前置 open 时 strict 下游不应可领：frontier=%v active=%d waiting=%d", seen, active, waiting)
	}
	if err := s.SetIntentState(active, "done"); err != nil {
		t.Fatal(err)
	}
	frontier, err = s.Frontier(20)
	if err != nil {
		t.Fatal(err)
	}
	seen = map[int64]bool{}
	for _, n := range frontier {
		seen[n.ID] = true
	}
	if !seen[waiting] {
		t.Fatal("前置 done 后 strict 下游应进入 frontier")
	}
}
