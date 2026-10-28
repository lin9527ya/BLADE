package db

import (
	"fmt"
	"testing"
	"time"
)

func TestDashboardTaskTokens(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	stamp := time.Now().UnixNano()
	profileID, err := d.SaveProfile(&LLMProfile{
		Name: fmt.Sprintf("dashboard-profile-%d", stamp), Format: "openai", Model: "test", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteProfile(profileID) })

	mk := func(name string) *Task {
		task, err := d.CreateTaskWithOptions(name, "goal", TaskCreateOptions{LLMProfileIDs: []int64{profileID}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
		return task
	}
	one := mk(fmt.Sprintf("dashboard-one-%d", stamp))
	two := mk(fmt.Sprintf("dashboard-two-%d", stamp))
	if _, err := d.Exec(`
INSERT INTO activity(exploration_id, kind, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
VALUES ($1,'result',10,3,2,1), ($2,'result',20,4,5,6)`, one.ExplorationID, two.ExplorationID); err != nil {
		t.Fatal(err)
	}

	got, err := d.DashboardTaskTokens()
	if err != nil {
		t.Fatal(err)
	}
	var profileRow *DashboardTaskTokenProfile
	for i := range got.ByProfile {
		if got.ByProfile[i].ProfileID != nil && *got.ByProfile[i].ProfileID == profileID {
			profileRow = &got.ByProfile[i]
			break
		}
	}
	if profileRow == nil {
		t.Fatalf("profile %d missing from %+v", profileID, got.ByProfile)
	}
	if profileRow.InputTokens != 30 || profileRow.OutputTokens != 7 ||
		profileRow.CacheRead != 7 || profileRow.CacheWrite != 7 || profileRow.TaskCount != 2 {
		t.Fatalf("profile aggregate = %+v", profileRow)
	}

	var date string
	if err := d.QueryRow(`SELECT to_char(created_at, 'YYYY-MM-DD') FROM tasks WHERE id=$1`, one.ID).Scan(&date); err != nil {
		t.Fatal(err)
	}
	var day *DashboardTaskTokenDay
	for i := range got.Daily {
		row := &got.Daily[i]
		if row.Date == date && row.ProfileID != nil && *row.ProfileID == profileID {
			day = row
			break
		}
	}
	if day == nil || day.InputTokens != 30 || day.OutputTokens != 7 {
		t.Fatalf("daily aggregate for %s = %+v", date, got.Daily)
	}
}
