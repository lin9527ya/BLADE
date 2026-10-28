package db

import (
	"testing"
)

func TestAddIntentGraphAtomic(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	st := d.Exploration(expID)
	goal, err := st.AddGoal(map[string]any{"text": "g"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := st.AddNode(KindFact, map[string]any{"summary": "p"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	loot, err := st.AddNode(KindFoothold, map[string]any{"summary": "l"}, 5, StateLootActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.AddIntentGraph(map[string]any{"summary": "atomic"}, 5, nil, "planner", []IntentEdge{
		{From: goal, Rel: RelSpawns, To: 0},
		{From: parent, Rel: RelDerivedFrom, To: 0},
		{From: 0, Rel: RelUses, To: loot},
	})
	if err != nil {
		t.Fatal(err)
	}
	edges, err := st.Edges(100)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, e := range edges {
		if e.From == id || e.To == id {
			found[e.Rel] = true
		}
	}
	if !found[RelSpawns] || !found[RelDerivedFrom] || !found[RelUses] {
		t.Fatalf("missing edges for intent %d: %+v", id, edges)
	}

	// A bad edge must roll back the node as well: no open intent may become
	// claimable before its lineage exists.
	bad, err := st.AddIntentGraph(map[string]any{"summary": "bad"}, 5, nil, "planner", []IntentEdge{
		{From: 9223372036854775807, Rel: RelDerivedFrom, To: 0},
	})
	if err == nil || bad != 0 {
		t.Fatalf("bad edge should fail: id=%d err=%v", bad, err)
	}
	if n, err := st.CountClaimableIntents(); err != nil || n != 1 { // only the valid atomic intent
		t.Fatalf("claimable intents = %d,%v; want 1", n, err)
	}
	_ = taskID
}
