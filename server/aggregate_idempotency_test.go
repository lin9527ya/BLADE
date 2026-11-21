package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestAggregateEndpointReplaysAndConflictsByOperationKey(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	companyID, _, _, _, _, err := s.m.pg.Companies().CreateCompanyWithScope(fmt.Sprintf("aggregate-company-%d", time.Now().UnixNano()), "", nil, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.Companies().DeleteCompany(companyID) })
	task, err := s.m.CreateTaskWithOptions("aggregate fixture", "report only", db.TaskCreateOptions{CompanyIDs: []int64{companyID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.engine.StopTask(task.ID); _, _ = s.m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	body := fmt.Sprintf(`{"scope_kind":"company","scope_id":%d,"name":"aggregate idempotency","kind":"pentest","note":"fixture","operation_key":"qa-same-key"}`, companyID)
	w := request(http.MethodPost, "/api/deliverables/aggregate", body)
	if w.Code != 201 {
		t.Fatalf("first %d %s", w.Code, w.Body)
	}
	var first db.DeliverableDetail
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.m.pg.DeleteDeliverable(first.Deliverable.ID)
		_, _ = s.m.pg.Exec(`DELETE FROM aggregate_deliverable_requests WHERE operation_key='qa-same-key'`)
	})
	w = request(http.MethodPost, "/api/deliverables/aggregate", body)
	if w.Code != 200 {
		t.Fatalf("replay %d %s", w.Code, w.Body)
	}
	var replay struct {
		*db.DeliverableDetail
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Deliverable.ID != first.Deliverable.ID {
		t.Fatalf("replay=%+v first=%d", replay, first.Deliverable.ID)
	}
	changed := fmt.Sprintf(`{"scope_kind":"company","scope_id":%d,"name":"changed","kind":"pentest","operation_key":"qa-same-key"}`, companyID)
	w = request(http.MethodPost, "/api/deliverables/aggregate", changed)
	if w.Code != 409 {
		t.Fatalf("conflict %d %s", w.Code, w.Body)
	}
}
