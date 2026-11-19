package db

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestSchedulingComponentsAndDispatchShareOrder(t *testing.T) {
	d, s := traceStore(t)
	pre, err := s.AddIntent(map[string]any{"summary": "prerequisite", "contract": map[string]any{"route": "common"}}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := s.AddIntentGraph(map[string]any{"summary": "downstream", "depends_on": []int64{pre}, "depends_on_mode": "advisory", "attempts": 2, "resume_after_restart": true, "contract": map[string]any{"route": "rare"}}, 8, nil, "planner", []IntentEdge{{To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := s.FrontierForSlot("work#1", nil, DispatchBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.ScheduleReport(100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr) != len(report.Rows) {
		t.Fatal("preview changed the candidate window")
	}
	for i, n := range fr {
		if report.Rows[i].IntentID != n.ID {
			t.Fatal("display ranks differ from dispatch")
		}
	}
	r := fr[0].Schedule
	if r.IntentID != down || r.Components.Priority != 800 || r.Components.Dependency != -100 || r.Components.Restart != 100000 || r.Components.Retries != -24 {
		t.Fatalf("incorrect components: %+v", r)
	}
	sum := r.Components.Priority + r.Components.Dependency + r.Components.Restart + r.Components.Route + r.Components.Aging + r.Components.Retries
	if math.Abs(sum-r.Score) > 0.00001 {
		t.Fatal("total not equal to displayed components")
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET created_at=now()-interval '16 minutes' WHERE id=$1`, pre); err != nil {
		t.Fatal(err)
	}
	fr, err = s.FrontierForSlot("work#1", nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	if fr[0].ID != pre || fr[0].Schedule.SelectionReason != "overdue_oldest_first" {
		t.Fatal("fairness not explained independently of numeric score")
	}
}

func TestSchedulingSnapshotPersistsActualPreClaimScore(t *testing.T) {
	_, s := traceStore(t)
	id, err := s.AddIntent(map[string]any{"summary": "snapshot"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fr, err := s.FrontierForSlot("work#1", nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimNode(t.Context(), id, "work#1", "open", IntentLeaseTTL, nil, true, fr[0].Schedule)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	report, err := s.ScheduleReport(20, id)
	if err != nil {
		t.Fatal(err)
	}
	if report.LastDispatch == nil || report.LastDispatch.Score != fr[0].Schedule.Score || report.Rows[0].Evaluated {
		t.Fatal("history recomputed as current rank")
	}
	var p struct {
		Claimed time.Time `json:"first_claimed_at"`
	}
	if json.Unmarshal(n.Payload, &p) != nil || p.Claimed.IsZero() {
		t.Fatal("first claim time missing")
	}
}

func TestStrictBlockedAndWindowExclusionAreNotZeroScore(t *testing.T) {
	_, s := traceStore(t)
	pre, err := s.AddIntent(map[string]any{"summary": "pre"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := s.AddIntentGraph(map[string]any{"summary": "strict", "depends_on_mode": "strict"}, 10, nil, "planner", []IntentEdge{{To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.ScheduleReport(20, down)
	if err != nil {
		t.Fatal(err)
	}
	r := report.Rows[0]
	if r.Claimable || r.Evaluated || r.WaitReason != "strict_dependency" {
		t.Fatalf("strict wait shown as low priority: %+v", r)
	}
}
