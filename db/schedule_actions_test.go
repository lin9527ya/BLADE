package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestClaimTaskScheduleActionDisablesOneTimeAndRecordsRun(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()
	ctx := context.Background()
	scheduled := time.Now().Add(-time.Minute).Truncate(time.Second)
	s, err := d.CreateTaskScheduleFull("once-pause", true,
		json.RawMessage(`{}`), json.RawMessage(`{"kind":"once_at","date":"2026-11-20","at":"09:30"}`),
		"pause", "123", "Asia/Shanghai", "", "", &scheduled)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM task_schedules WHERE id=$1`, s.ID) })
	runID, err := d.ClaimTaskScheduleAction(ctx, s.ID, scheduled, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := d.GetTaskSchedule(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.Enabled || row.NextRunAt != nil {
		t.Fatalf("one-time action must disable the schedule: %+v", row)
	}
	if err := d.FinishTaskScheduleAction(ctx, s.ID, runID, "applied", "123", ""); err != nil {
		t.Fatal(err)
	}
	runs, err := d.ListScheduleRuns(ctx, s.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].State != "applied" || runs[0].TaskID != "123" {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	row, _ = d.GetTaskSchedule(s.ID)
	if row == nil || row.LastTaskID != "123" || row.LastRunAt == nil {
		t.Fatalf("schedule after action: %+v", row)
	}
}
