package db

import "testing"

func TestIntentMetricsForManyExplorations(t *testing.T) {
	d, first := intentLeaseFixture(t)
	secondTask, err := d.CreateTask("intent-metrics-second", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(secondTask.ID) })
	second := d.Exploration(secondTask.ExplorationID)

	openID, err := first.AddIntent(map[string]any{"summary": "open"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	runningID, err := first.AddIntent(map[string]any{"summary": "running"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='running' WHERE id=$1`, runningID); err != nil {
		t.Fatal(err)
	}
	if _, err := second.AddIntent(map[string]any{"summary": "second open"}, 1, nil, "planner"); err != nil {
		t.Fatal(err)
	}

	states, err := d.IntentStateCountsFor([]int64{first.expID, second.expID})
	if err != nil {
		t.Fatal(err)
	}
	if states[first.expID]["open"] != 1 || states[first.expID]["running"] != 1 || states[second.expID]["open"] != 1 {
		t.Fatalf("states = %+v", states)
	}
	claimable, err := d.ClaimableIntentCountsFor([]int64{first.expID, second.expID})
	if err != nil {
		t.Fatal(err)
	}
	if claimable[first.expID] != 1 || claimable[second.expID] != 1 {
		t.Fatalf("claimable = %+v (open id %d)", claimable, openID)
	}
}
