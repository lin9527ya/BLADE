package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestDashboardSummaryUsesWindowAndAggregates(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	pg := s.m.pg
	stamp := time.Now().UnixNano()
	profileID, err := pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("dashboard-api-%d", stamp), Format: "openai", Model: "test", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteProfile(profileID) })

	mk := func(name string) *Task {
		task, err := s.m.CreateTaskWithOptions(name, "goal", db.TaskCreateOptions{LLMProfileIDs: []int64{profileID}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = s.m.DeleteTask(task.ID, DeleteTaskOptions{}) })
		return task
	}
	one := mk(fmt.Sprintf("dashboard-api-one-%d", stamp))
	two := mk(fmt.Sprintf("dashboard-api-two-%d", stamp))
	if _, err := pg.Exec(`
INSERT INTO activity(exploration_id, kind, input_tokens, output_tokens)
VALUES ($1,'result',10,3), ($2,'result',20,4)`, one.ExpID, two.ExpID); err != nil {
		t.Fatal(err)
	}

	w := request(http.MethodGet, "/api/dashboard", "")
	if w.Code != 200 {
		t.Fatalf("dashboard status=%d body=%s", w.Code, w.Body)
	}
	var out struct {
		TaskTotal      int                            `json:"task_total"`
		TasksByStatus  map[string]int                 `json:"tasks_by_status"`
		RecentTasks    []TaskDTO                      `json:"recent_tasks"`
		TokenByProfile []db.DashboardTaskTokenProfile `json:"token_by_profile"`
		DailyTokens    []db.DashboardTaskTokenDay     `json:"daily_tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v %s", err, w.Body)
	}
	if out.TaskTotal < 2 || len(out.RecentTasks) == 0 || out.TasksByStatus == nil {
		t.Fatalf("summary = %+v", out)
	}
	var profileRow *db.DashboardTaskTokenProfile
	for i := range out.TokenByProfile {
		if out.TokenByProfile[i].ProfileID != nil && *out.TokenByProfile[i].ProfileID == profileID {
			profileRow = &out.TokenByProfile[i]
			break
		}
	}
	if profileRow == nil || profileRow.InputTokens != 30 || profileRow.TaskCount != 2 {
		t.Fatalf("profile aggregate = %+v", profileRow)
	}
	if len(out.DailyTokens) == 0 {
		t.Fatal("daily token aggregate empty")
	}
}
