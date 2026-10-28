package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestFireTaskScheduleRunIsTransactional(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	ctx := context.Background()
	spec := json.RawMessage(fmt.Sprintf(`{"description":"schedule-run-%d","goal":"goal"}`, time.Now().UnixNano()))
	freq := json.RawMessage(`{"kind":"every_hours","every_hours":1}`)
	scheduled := time.Now().Add(-time.Minute).Truncate(time.Second)
	next := scheduled.Add(time.Hour)
	sched, err := d.CreateTaskSchedule("schedule-run-test", true, spec, freq, &scheduled)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM task_schedules WHERE id=$1`, sched.ID) })

	task, err := d.FireTaskScheduleRun(ctx, sched.ID, scheduled, next, "schedule-run-task", "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	row, err := d.GetTaskSchedule(sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.NextRunAt == nil || !row.NextRunAt.Equal(next) || row.LastTaskID != fmt.Sprint(task.ID) || row.LastError != "" {
		t.Fatalf("schedule after success = %+v", row)
	}
	runs, err := d.ListScheduleRuns(ctx, sched.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].State != "created" || runs[0].TaskID != fmt.Sprint(task.ID) {
		t.Fatalf("runs after success = %+v", runs)
	}
	if _, err := d.FireTaskScheduleRun(ctx, sched.ID, scheduled, next, "duplicate", "goal", TaskCreateOptions{}); !errors.Is(err, ErrScheduleRunDuplicate) {
		t.Fatalf("duplicate occurrence error = %v", err)
	}

	// A task-creation failure must roll back the task but commit a failed ledger
	// entry and the advanced schedule, so the next tick does not hot-loop.
	bad, err := d.CreateTaskSchedule("schedule-run-bad", true, spec, json.RawMessage(`{"kind":"every_hours","every_hours":1}`), &scheduled)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM task_schedules WHERE id=$1`, bad.ID) })
	badCategory := int64(999999999)
	if _, err := d.FireTaskScheduleRun(ctx, bad.ID, scheduled, next, "bad-category", "goal", TaskCreateOptions{CategoryID: &badCategory}); err == nil {
		t.Fatal("invalid category should fail task creation")
	}
	badRow, err := d.GetTaskSchedule(bad.ID)
	if err != nil {
		t.Fatal(err)
	}
	if badRow == nil || badRow.NextRunAt == nil || !badRow.NextRunAt.Equal(next) || badRow.LastTaskID != "" || badRow.LastError == "" {
		t.Fatalf("schedule after failure = %+v", badRow)
	}
	badRuns, err := d.ListScheduleRuns(ctx, bad.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(badRuns) != 1 || badRuns[0].State != "failed" || badRuns[0].Error == "" {
		t.Fatalf("runs after failure = %+v", badRuns)
	}
	var created int
	if err := d.QueryRow(`SELECT count(*) FROM tasks WHERE description='bad-category'`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("failed run left %d tasks behind", created)
	}
}
