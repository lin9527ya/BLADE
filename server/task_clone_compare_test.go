package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestTaskCloneTemplateAndCompare(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()

	stamp := time.Now().UnixNano()
	source, err := m.CreateTaskWithOptions("clone source "+fmt.Sprint(stamp), "source goal", db.TaskCreateOptions{Name: "Clone Source"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(source.ID, DeleteTaskOptions{}) }()
	sourceID, _ := strconv.ParseInt(source.ID, 10, 64)
	child, err := m.CreateTaskWithOptions("relation child "+fmt.Sprint(stamp), "child goal", db.TaskCreateOptions{SourceTaskIDs: []int64{sourceID}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(child.ID, DeleteTaskOptions{}) }()
	relations, err := m.PG().TaskRelations(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	var sawDependent bool
	for _, relation := range relations {
		if strconv.FormatInt(relation.ID, 10) == child.ID && relation.Direction == "dependent" {
			sawDependent = true
		}
	}
	if !sawDependent {
		t.Fatalf("task relation missing dependent child: %+v", relations)
	}

	s := &Server{m: m, engine: &Engine{}}
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+source.ID+"/clone", nil)
	req.SetPathValue("id", source.ID)
	rec := httptest.NewRecorder()
	s.cloneTask(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("clone status=%d body=%s", rec.Code, rec.Body.String())
	}
	var clone TaskDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &clone); err != nil {
		t.Fatal(err)
	}
	if clone.ID == source.ID || clone.Name != "Clone Source（副本）" {
		t.Fatalf("clone = %+v", clone)
	}
	defer func() { _, _ = m.DeleteTask(clone.ID, DeleteTaskOptions{}) }()

	rec = httptest.NewRecorder()
	treq := httptest.NewRequest(http.MethodGet, "/api/tasks/compare?ids="+source.ID+","+clone.ID, nil)
	s.compareTasks(rec, treq)
	if rec.Code != http.StatusOK {
		t.Fatalf("compare status=%d body=%s", rec.Code, rec.Body.String())
	}
	var compare struct {
		Items []TaskCompareRow `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &compare); err != nil {
		t.Fatal(err)
	}
	if len(compare.Items) != 2 {
		t.Fatalf("compare items=%d", len(compare.Items))
	}

	rec = httptest.NewRecorder()
	treq = httptest.NewRequest(http.MethodPost, "/api/tasks/"+source.ID+"/template", nil)
	treq.SetPathValue("id", source.ID)
	s.taskToTemplate(rec, treq)
	if rec.Code != http.StatusCreated {
		t.Fatalf("template status=%d body=%s", rec.Code, rec.Body.String())
	}
	var template db.TaskTemplate
	if err := json.Unmarshal(rec.Body.Bytes(), &template); err != nil {
		t.Fatal(err)
	}
	if template.ID == 0 || template.Name != "Clone Source 模板" {
		t.Fatalf("template = %+v", template)
	}
	_, _ = m.PG().DeleteTaskTemplate(template.ID)

}
