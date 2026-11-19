package db

import "testing"

// 显式 depends_on 现在参与领取门槛：前置没到 done/negative 时下游不进入
// frontier；前置完成后自动 ready。没有依赖边的意图仍然立即可领。
func TestFrontierHonorsExplicitDependencies(t *testing.T) {
	_, s := intentLeaseFixture(t)
	upstream, err := s.AddIntent(map[string]any{"summary": "upstream"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetIntentState(upstream, "exhausted"); err != nil {
		t.Fatal(err)
	}
	downstream, err := s.AddIntent(map[string]any{"summary": "downstream", "requires": []IntentRequirement{{Type: "capability", Key: "missing"}}}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Link(downstream, RelDependsOn, upstream); err != nil {
		t.Fatal(err)
	}
	frontier, err := s.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, n := range frontier {
		if n.ID == downstream {
			seen = true
		}
	}
	if seen {
		t.Fatal("前置 exhausted 时下游不应进入 frontier")
	}
	if n, err := s.CountClaimableIntents(); err != nil || n != 0 {
		t.Fatalf("claimable=%d err=%v, want 0", n, err)
	}
	if err := s.SetIntentState(upstream, "done"); err != nil {
		t.Fatal(err)
	}
	frontier, err = s.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	seen = false
	for _, n := range frontier {
		if n.ID == downstream {
			seen = true
		}
	}
	if !seen {
		t.Fatal("前置 done 后下游应进入 frontier")
	}
}
