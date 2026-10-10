package db

import "testing"

// TestIntentGoalLink pins the 目标分解 axis at the storage boundary:
// SingleOpenGoal answers only when the choice is unambiguous (exactly one open
// goal), and LinkIntentToGoal refuses non-open goals while writing an idempotent
// goal --spawns--> intent edge.
func TestIntentGoalLink(t *testing.T) {
	d, _, expID := coverageMatrixFixture(t)
	st := d.Exploration(expID)
	intent, err := st.AddIntent(map[string]any{"summary": "目标轴用例"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if only, err := st.SingleOpenGoal(); err != nil || only != 0 {
		t.Fatalf("no goals → single=0: %d err=%v", only, err)
	}
	g1, err := st.AddNode(KindGoal, map[string]any{"text": "目标一"}, 0, "open", "goals", nil)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := st.AddNode(KindGoal, map[string]any{"text": "目标二"}, 0, "open", "goals", nil)
	if err != nil {
		t.Fatal(err)
	}
	if only, err := st.SingleOpenGoal(); err != nil || only != 0 {
		t.Fatalf("two open goals must be ambiguous: %d err=%v", only, err)
	}
	if err := st.SetNodeState(g2, "met"); err != nil {
		t.Fatal(err)
	}
	if only, err := st.SingleOpenGoal(); err != nil || only != g1 {
		t.Fatalf("one open goal left: %d want %d err=%v", only, g1, err)
	}
	if ok, _ := st.LinkIntentToGoal(g2, intent); ok {
		t.Fatal("met goal must be refused")
	}
	if ok, err := st.LinkIntentToGoal(g1, intent); err != nil || !ok {
		t.Fatalf("open goal must link: ok=%v err=%v", ok, err)
	}
	if ok, err := st.LinkIntentToGoal(g1, intent); err != nil || !ok {
		t.Fatalf("relink must be idempotent: ok=%v err=%v", ok, err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='spawns' AND dst_id=$3`, expID, g1, intent).Scan(&n); err != nil || n != 1 {
		t.Fatalf("spawns edge: n=%d err=%v", n, err)
	}
	if ok, err := st.LinkIntentToGoal(0, intent); err != nil || ok {
		t.Fatalf("zero goal id is a no-op: ok=%v err=%v", ok, err)
	}
	if ok, _ := st.LinkIntentToGoal(999999, intent); ok {
		t.Fatal("unknown goal must be refused")
	}
}
