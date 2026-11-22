package db

import (
	"context"
	"testing"
)

// Delta changes carry a compact semantic patch for node updates: the planner can
// tell what changed without a detail round-trip, and high-value changes are
// marked detail_required. Edge changes never get a node summary.
func TestBlackboardChangesCarryNodeSemanticPatch(t *testing.T) {
	d, st := intentLeaseFixture(t)
	fact, err := st.AddNode(KindFact, map[string]any{
		"summary": "旧摘要", "polarity": "pos", "confidence": "observed",
	}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := st.BlackboardVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes
SET payload = payload || '{"summary":"新摘要"}'::jsonb, state='confirmed', content_version=content_version+1
WHERE id=$1`, fact); err != nil {
		t.Fatal(err)
	}
	changes, err := st.BlackboardChanges(context.Background(), base.Cursor(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var nodeChange *BlackboardChange
	for i := range changes.Changes {
		if changes.Changes[i].Entity == "node" && changes.Changes[i].NodeID != nil && *changes.Changes[i].NodeID == fact {
			nodeChange = &changes.Changes[i]
			break
		}
	}
	if nodeChange == nil {
		t.Fatalf("node change missing: %+v", changes.Changes)
	}
	if nodeChange.Summary != "新摘要" || nodeChange.Polarity != "pos" || nodeChange.Confidence != "observed" {
		t.Fatalf("semantic patch=%+v", nodeChange)
	}
	if !nodeChange.DetailRequired {
		t.Fatalf("payload/state change must require detail: %+v", nodeChange)
	}
	if nodeChange.After != "confirmed" {
		t.Fatalf("state transition lost: %+v", nodeChange)
	}

	// Edge changes have no node payload to enrich; they must remain untouched.
	goal, err := st.AddGoal(map[string]any{"text": "目标"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	base, err = st.BlackboardVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Link(fact, RelProves, goal); err != nil {
		t.Fatal(err)
	}
	changes, err = st.BlackboardChanges(context.Background(), base.Cursor(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes.Changes {
		if c.Entity == "edge" {
			if c.Summary != "" || c.DetailRequired {
				t.Fatalf("edge change must not carry node patch: %+v", c)
			}
			return
		}
	}
	t.Fatalf("edge change missing: %+v", changes.Changes)
}

func TestBlackboardChangeNeedsDetail(t *testing.T) {
	for _, tc := range []struct {
		fields []string
		want   bool
	}{
		{[]string{"payload"}, true},
		{[]string{"state"}, true},
		{[]string{"confidence"}, true},
		{[]string{"evidence_version"}, true},
		{[]string{"priority"}, false},
		{[]string{"attrs"}, false},
	} {
		if got := changeNeedsDetail(BlackboardChange{Fields: tc.fields}); got != tc.want {
			t.Fatalf("fields=%v got=%v want=%v", tc.fields, got, tc.want)
		}
	}
}

// 漏洞处置状态的变化同样要能在 delta 里看懂：新状态、摘要与置信度直接给，
// 而不是只给一个 changed_fields=[status,confidence]。
func TestBlackboardChangesEnrichFindingStatus(t *testing.T) {
	d, st := intentLeaseFixture(t)
	nodeID, err := st.AddNode(KindFinding, map[string]any{"summary": "SQL 注入"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO findings(node_id,vulnclass,summary,status,confidence)
VALUES($1,'sqli','SQL 注入','pending','')`, nodeID); err != nil {
		t.Fatal(err)
	}
	base, err := st.BlackboardVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE findings SET status='confirmed', confidence='high' WHERE node_id=$1`, nodeID); err != nil {
		t.Fatal(err)
	}
	changes, err := st.BlackboardChanges(context.Background(), base.Cursor(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes.Changes {
		if c.Entity == "finding" && c.NodeID != nil && *c.NodeID == nodeID {
			if c.Summary != "SQL 注入" || c.Status != "confirmed" || c.Confidence != "high" {
				t.Fatalf("finding patch=%+v", c)
			}
			if !c.DetailRequired {
				t.Fatalf("status/confidence change must require detail: %+v", c)
			}
			return
		}
	}
	t.Fatalf("finding change missing: %+v", changes.Changes)
}
