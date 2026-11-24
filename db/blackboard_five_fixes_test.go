package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestDropDependencyRejectsAlreadyRunningSource(t *testing.T) {
	_, s := traceStore(t)
	pre, err := s.AddIntent(map[string]any{"summary": "pre"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := s.AddIntentGraph(map[string]any{"summary": "down", "depends_on": []int64{pre}}, 5, nil, "planner", []IntentEdge{{To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.ClaimNode(t.Context(), down, "worker#down", "open", IntentLeaseTTL, nil, false); err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	before, err := s.GetNode(down)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.DropIntentDependency(down, pre, "stale pre-read")
	if changed || !errors.Is(err, ErrIntentStateConflict) {
		t.Fatalf("running source must refuse without side effects: changed=%v err=%v", changed, err)
	}
	after, err := s.GetNode(down)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.Payload, after.Payload) || before.ContentVersion != after.ContentVersion {
		t.Fatal("refused dependency edit changed payload/version")
	}
	assertDependencyEdgeCount(t, s, down, pre, 1)
}

func TestDropDependencySerializesWithConcurrentClaim(t *testing.T) {
	d, s := traceStore(t)
	pre, err := s.AddIntent(map[string]any{"summary": "pre"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := s.AddIntentGraph(map[string]any{"summary": "down", "depends_on": []int64{pre}}, 5, nil, "planner", []IntentEdge{{To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	// Hold the same source row lock that ClaimNode takes, then publish the claim
	// while the edit is waiting. The older implementation deleted the edge first
	// and committed it even though its later UPDATE no longer matched open.
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRow(`SELECT id FROM exploration_nodes WHERE id=$1 FOR UPDATE`, down).Scan(&id); err != nil {
		t.Fatal(err)
	}
	type result struct {
		changed bool
		err     error
	}
	started := make(chan struct{})
	done := make(chan result, 1)
	go func() {
		close(started)
		changed, err := s.DropIntentDependency(down, pre, "race with claim")
		done <- result{changed, err}
	}()
	<-started
	select {
	case got := <-done:
		t.Fatalf("edit should wait for the claim row lock: %+v", got)
	case <-time.After(40 * time.Millisecond):
	}
	if _, err := tx.Exec(`UPDATE exploration_nodes SET state='running',owner='worker#claim',
logical_session_id=$2,execution_generation=execution_generation+1,content_version=content_version+1
WHERE id=$1`, down, fmt.Sprintf("exp%d-worker-i%d", s.expID, down)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.changed || !errors.Is(got.err, ErrIntentStateConflict) {
			t.Fatalf("edit after the claim must roll back: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dependency edit did not finish after releasing the claim lock")
	}
	assertDependencyEdgeCount(t, s, down, pre, 1)
	n, err := s.GetNode(down)
	if err != nil || n == nil || n.State != "running" {
		t.Fatalf("claimed state lost: %v (%v)", n, err)
	}
	var payload struct {
		DependsOn []int64 `json:"depends_on"`
	}
	if json.Unmarshal(n.Payload, &payload) != nil || !reflect.DeepEqual(payload.DependsOn, []int64{pre}) {
		t.Fatal("payload no longer agrees with the preserved dependency edge")
	}
}

func TestDropDependencyNoopAndLegacyPayloadPreservation(t *testing.T) {
	_, s := traceStore(t)
	pre, err := s.AddIntent(map[string]any{"summary": "pre"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := s.AddIntentGraph(map[string]any{
		"summary": "legacy down", "depends_on": []any{pre, fmt.Sprintf(" %d ", pre), nil, "not-an-id", map[string]any{"other": true}},
		"large_integer": json.Number("9223372036854775807"),
	}, 5, nil, "planner", []IntentEdge{{To: pre, Rel: RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := s.DropIntentDependency(down, pre, "remove only matching ids"); err != nil || !changed {
		t.Fatalf("legacy dependency edit: changed=%v err=%v", changed, err)
	}
	assertDependencyEdgeCount(t, s, down, pre, 0)
	n, err := s.GetNode(down)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload["large_integer"]) != "9223372036854775807" {
		t.Fatal("unrelated integer payload was rounded during dependency repair")
	}
	var deps []json.RawMessage
	if err := json.Unmarshal(payload["depends_on"], &deps); err != nil {
		t.Fatal(err)
	}
	if len(deps) != 3 || string(deps[0]) != "null" || string(deps[1]) != `"not-an-id"` {
		t.Fatalf("unmatched legacy entries were not preserved: %s", payload["depends_on"])
	}
	before, err := s.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	version := n.ContentVersion
	if changed, err := s.DropIntentDependency(down, pre, "duplicate request"); err != nil || changed {
		t.Fatalf("duplicate dependency drop should be a no-op: changed=%v err=%v", changed, err)
	}
	after, err := s.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n, err = s.GetNode(down)
	if err != nil || n.ContentVersion != version || before.Cursor() != after.Cursor() {
		t.Fatal("no-op dependency edit published a change")
	}
}

func assertDependencyEdgeCount(t *testing.T, s *ExplorationStore, src, dst int64, want int) {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM exploration_edges
WHERE exploration_id=$1 AND src_id=$2 AND dst_id=$3 AND rel='depends_on'`, s.expID, src, dst).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("dependency edge count=%d want %d", count, want)
	}
}

func TestDispatchAndRouteDistributionUseSameBuckets(t *testing.T) {
	_, s := traceStore(t)
	firstParent, err := s.AddNode(KindFact, map[string]any{"summary": "first source"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	secondParent, err := s.AddNode(KindFact, map[string]any{"summary": "second source"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		payload map[string]any
		parents []int64
		want    string
	}{
		{"contract", map[string]any{"contract": map[string]any{"route": " \tcontract-route\n "}, "risk_area": "ignored"}, []int64{firstParent}, "contract-route"},
		{"risk", map[string]any{"contract": map[string]any{"route": " \t\n "}, "risk_area": " auth ", "route": "ignored"}, nil, "auth"},
		{"legacy route", map[string]any{"route": " legacy "}, nil, "legacy"},
		{"numeric asset", map[string]any{"asset_ids": []int64{42}}, []int64{firstParent}, "asset:42"},
		{"string asset", map[string]any{"asset_ids": []string{" +0042 "}}, nil, "asset:42"},
		{"earliest parent", map[string]any{}, []int64{secondParent, firstParent}, fmt.Sprintf("parent:%d", firstParent)},
		{"second parent", map[string]any{}, []int64{secondParent}, fmt.Sprintf("parent:%d", secondParent)},
		{"invalid asset", map[string]any{"asset_ids": []string{"not-an-id"}}, []int64{firstParent}, fmt.Sprintf("parent:%d", firstParent)},
		{"oversized asset", map[string]any{"asset_ids": []string{"9999999999999999999"}}, []int64{firstParent}, fmt.Sprintf("parent:%d", firstParent)},
		{"no route", map[string]any{}, nil, "unrouted"},
	}
	wantByID := map[int64]string{}
	wantCounts := map[string]int{}
	ids := make([]int64, 0, len(cases))
	for _, c := range cases {
		c.payload["summary"] = c.name
		edges := make([]IntentEdge, 0, len(c.parents))
		for _, parent := range c.parents {
			edges = append(edges, IntentEdge{From: parent, Rel: RelDerivedFrom})
		}
		id, err := s.AddIntentGraph(c.payload, 5, nil, "planner", edges)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		wantByID[id] = c.want
		wantCounts[c.want]++
	}
	routes, err := s.IntentRoutes(ids)
	if err != nil || !reflect.DeepEqual(routes, wantByID) {
		t.Fatalf("route resolution: %v want %v (%v)", routes, wantByID, err)
	}
	fr, err := s.FrontierForSlot("", nil, DispatchBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr) != len(cases) {
		t.Fatalf("dispatch lost candidates: %d want %d", len(fr), len(cases))
	}
	for _, node := range fr {
		if node.Schedule == nil || node.Schedule.Route != wantByID[node.ID] {
			t.Fatalf("dispatch uses a different route for %d: %+v", node.ID, node.Schedule)
		}
	}
	stats, err := s.RouteDistribution(100)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, r := range stats {
		counts[r.Route] = r.Intents
	}
	if !reflect.DeepEqual(counts, wantCounts) {
		t.Fatalf("full aggregate disagrees with dispatch: %v want %v", counts, wantCounts)
	}
	parents, err := s.DerivedParentsForNodes(ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range fr {
		if wantByID[c.ID] == fmt.Sprintf("parent:%d", firstParent) && len(parents[c.ID]) == 0 {
			t.Fatal("lightweight parent projection lost route ancestry")
		}
	}
}
