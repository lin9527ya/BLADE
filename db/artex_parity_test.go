package db

import "testing"

// ARTEX has no structural dependency/requires gating: legacy edges/payloads remain
// readable but every open intent is claimable in priority/id order.
func TestFrontierIgnoresLegacyDependenciesAndRequires(t *testing.T) {
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
	if !seen {
		t.Fatal("legacy depends_on still gates the frontier")
	}
	if n, err := s.CountClaimableIntents(); err != nil || n != 1 {
		t.Fatalf("claimable=%d err=%v, want 1", n, err)
	}
	if claimed, err := s.ClaimNode(t.Context(), downstream, "work#1", "open", IntentLeaseTTL, nil, false); err != nil || claimed == nil {
		t.Fatalf("legacy requires still blocks claim: %v %v", claimed, err)
	}
}
