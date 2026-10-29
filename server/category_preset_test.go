package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestCategoryPresetFeedsTaskCreation(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	pg := s.m.pg
	name := fmt.Sprintf("preset-%d", time.Now().UnixNano())
	created, err := pg.CreateTaskCategoryWithPreset(name, db.TaskCategoryPreset{DefaultCoverageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM task_categories WHERE id=$1`, created.ID) })

	w := request(http.MethodPatch, fmt.Sprintf("/api/task-categories/%d/preset", created.ID),
		`{"seed_first_intent":true,"default_coverage_enabled":false}`)
	if w.Code != 200 {
		t.Fatalf("preset update: %d %s", w.Code, w.Body)
	}
	var category struct {
		SeedFirstIntent       bool `json:"seed_first_intent"`
		DefaultCoverageEnable bool `json:"default_coverage_enabled"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &category); err != nil {
		t.Fatal(err)
	}
	if !category.SeedFirstIntent || category.DefaultCoverageEnable {
		t.Fatalf("preset not stored: %+v", category)
	}

	// Task creation inherits the category defaults when the request omits them.
	w = request(http.MethodPost, "/api/tasks", fmt.Sprintf(`{"description":"preset-task","goal":"g","category_id":%d}`, created.ID))
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("create task: %d %s", w.Code, w.Body)
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	var coverage bool
	if err := pg.QueryRow(`SELECT coverage_enabled FROM tasks WHERE id=$1`, task.ID).Scan(&coverage); err != nil {
		t.Fatal(err)
	}
	if coverage {
		t.Fatal("category default_coverage_enabled=false was not applied")
	}
	var seeded int
	if err := pg.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND kind='intent'`, task.ID).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	if seeded == 0 {
		t.Fatal("category seed_first_intent=true did not seed an intent")
	}
}
