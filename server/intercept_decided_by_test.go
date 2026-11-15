package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestBatchDecideRecordsActorAndChannel(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	id, err := m.PG().CreateInterceptPending(0, 0, "batch-decided-by", "test", "Bash", []byte(`{"command":"id"}`), "review")
	if err != nil {
		t.Fatal(err)
	}
	defer m.PG().Exec(`DELETE FROM intercept_pending WHERE id=$1`, id) //nolint:errcheck

	s := &Server{m: m, engine: &Engine{}}
	body, _ := json.Marshal(map[string]any{"ids": []int64{id}, "decision": "allowed"})
	req := httptest.NewRequest(http.MethodPost, "/api/intercept/pending/batch-decide", bytes.NewReader(body))
	req = withActor(req, "token:automation")
	rec := httptest.NewRecorder()
	s.interceptBatchDecide(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	detail, err := m.PG().GetInterceptDetail(id)
	if err != nil || detail == nil || detail.Audit == nil {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	if detail.Audit.DecidedBy != "token:automation" || detail.Audit.DecisionChannel != "batch" {
		t.Fatalf("audit = %+v", detail.Audit)
	}
	var _ = db.InterceptAudit{}
}
