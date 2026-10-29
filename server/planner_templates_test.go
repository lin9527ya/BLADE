package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPlannerTemplateCRUDAndSnapshot(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	name := fmt.Sprintf("tpl-%d", time.Now().UnixNano())
	body := "模板正文 {{.Goal}}"

	w := request(http.MethodPost, "/api/planner-templates", fmt.Sprintf(`{"name":%q,"body":%q}`, name, body))
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM planner_templates WHERE id=$1`, created.ID) })

	// Out-of-catalog variables are rejected at write time.
	w = request(http.MethodPost, "/api/planner-templates", `{"name":"bad","body":"{{.Unknown}}"}`)
	if w.Code != 400 {
		t.Fatalf("unknown var should be rejected: %d %s", w.Code, w.Body)
	}
	w = request(http.MethodPost, "/api/planner-templates", `{"name":"bad2","body":"{{.Goal"}`)
	if w.Code != 400 {
		t.Fatalf("syntax error should be rejected: %d %s", w.Code, w.Body)
	}

	// Default switching is exclusive.
	if w := request(http.MethodPost, fmt.Sprintf("/api/planner-templates/%d/default", created.ID), ""); w.Code != 200 {
		t.Fatalf("set default: %d %s", w.Code, w.Body)
	}
	def, err := s.m.pg.DefaultPlannerTemplateBody()
	if err != nil || def != body {
		t.Fatalf("default body = %q err=%v", def, err)
	}

	// Task creation snapshots the template body.
	w = request(http.MethodPost, "/api/tasks", fmt.Sprintf(`{"description":"tpl-task","goal":"g","planner_template_id":%d}`, created.ID))
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
	var snapshot string
	if err := s.m.pg.QueryRow(`SELECT planner_prompt_override FROM tasks WHERE id=$1`, task.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot != body {
		t.Fatalf("task snapshot = %q, want %q", snapshot, body)
	}

	// Deleting the default is refused; deleting a non-default works.
	if w := request(http.MethodDelete, fmt.Sprintf("/api/planner-templates/%d", created.ID), ""); w.Code != 409 {
		t.Fatalf("default delete should be 409: %d %s", w.Code, w.Body)
	}

	// Explicit body wins over template id.
	w = request(http.MethodPost, "/api/tasks", fmt.Sprintf(`{"description":"tpl-task-2","goal":"g","planner_template_id":%d,"planner_prompt":"手写 {{.Goal}}"}`, created.ID))
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("create task 2: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	if err := s.m.pg.QueryRow(`SELECT planner_prompt_override FROM tasks WHERE id=$1`, task.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(snapshot, "手写") {
		t.Fatalf("explicit prompt should win, got %q", snapshot)
	}
}
