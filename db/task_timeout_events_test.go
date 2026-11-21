package db

import (
	"testing"
)

func timeoutTasks(t *testing.T) (*DB, []int64) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	var ids []int64
	for i := 0; i < 2; i++ {
		task, err := d.CreateTask("timeout event fixture", "fixture", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
		t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	}
	return d, ids
}

func TestTimeoutEventsFollowTransitionsNotEntityWatermarks(t *testing.T) {
	d, ids := timeoutTasks(t)
	if _, err := d.Exec(`UPDATE tasks SET status='timeout' WHERE id=$1`, ids[1]); err != nil {
		t.Fatal(err)
	}
	first, err := d.ClaimTaskTimeoutEvents(t.Context(), 10, ids...)
	if err != nil || len(first) != 1 || first[0].TaskID != ids[1] {
		t.Fatalf("first: %+v %v", first, err)
	}
	if err := d.FinishTaskTimeoutEvent(t.Context(), first[0], true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET status='timeout' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	late, err := d.ClaimTaskTimeoutEvents(t.Context(), 10, ids...)
	if err != nil || len(late) != 1 || late[0].TaskID != ids[0] {
		t.Fatalf("older task timeout lost: %+v %v", late, err)
	}
	if err := d.FinishTaskTimeoutEvent(t.Context(), late[0], true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET status='timeout' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if evs, err := d.ClaimTaskTimeoutEvents(t.Context(), 10, ids...); err != nil || len(evs) != 0 {
		t.Fatal("same timeout state created duplicate")
	}
	if _, err := d.Exec(`UPDATE tasks SET status='running' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET status='timeout' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	again, err := d.ClaimTaskTimeoutEvents(t.Context(), 10, ids...)
	if err != nil || len(again) != 1 || again[0].ID == late[0].ID {
		t.Fatal("revival did not create a new transition")
	}
}

func TestTimeoutEventPendingFrontierSurvivesLateCommitAndRestart(t *testing.T) {
	d, ids := timeoutTasks(t)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tasks SET status='timeout' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET status='timeout' WHERE id=$1`, ids[1]); err != nil {
		t.Fatal(err)
	}
	first, err := d.ClaimTaskTimeoutEvents(t.Context(), 10, ids...)
	if err != nil || len(first) != 1 {
		t.Fatal(err)
	}
	if err := d.FinishTaskTimeoutEvent(t.Context(), first[0], true); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	late, err := d.ClaimTaskTimeoutEvents(t.Context(), 10, ids...)
	if err != nil || len(late) != 1 || late[0].ID >= first[0].ID {
		t.Fatal("late committed lower event ID was missed")
	}
	if evs, err := d.ClaimTaskTimeoutEvents(t.Context(), 10, ids...); err != nil || len(evs) != 0 {
		t.Fatal("active claim was duplicated")
	}
	if _, err := d.Exec(`UPDATE task_timeout_events SET lease_until=now()-interval '1 second' WHERE id=$1`, late[0].ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := d.ClaimTaskTimeoutEvents(t.Context(), 10, ids...)
	if err != nil || len(resumed) != 1 || resumed[0].ClaimToken == late[0].ClaimToken {
		t.Fatal("restart claim did not resume")
	}
	if ok, err := d.AckTaskTimeoutTrigger(t.Context(), late[0], 9); err != nil || ok {
		t.Fatal("stale claim was acknowledged")
	}
	if err := d.FinishTaskTimeoutEvent(t.Context(), resumed[0], true); err != nil {
		t.Fatal(err)
	}
}
