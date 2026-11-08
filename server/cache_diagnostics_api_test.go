package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

func TestCacheDiagnosticsAPIOptInAndLedger(t *testing.T) {
	t.Setenv(agent.CacheTraceEnv, "")
	agent.SetCacheTraceWindow(0)
	t.Cleanup(func() { agent.SetCacheTraceWindow(0) })
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	task, err := m.CreateTask("diagnostics", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.pg.DeleteTask(mustTaskID(t, task.ID))
		m.pg.Exec(`DELETE FROM llm_usage WHERE task_id=$1`, task.ID)
	})
	if err := m.pg.InsertLLMUsage(&db.LLMUsage{TaskID: task.ID, Worker: "planner", SessionID: "exp" + task.ID + "-planner", InputTokens: 100, CacheRead: 80, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m}
	get := httptest.NewRecorder()
	s.getCacheDiagnostics(get, httptest.NewRequest("GET", "/api/diagnostics/cache?task="+task.ID, nil))
	var result struct {
		State agent.CacheTraceStatus `json:"state"`
		Calls []db.UsageDiagnostic   `json:"calls"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if get.Code != 200 || result.State.Enabled || len(result.Calls) != 1 || result.Calls[0].CacheRead != 80 {
		t.Fatalf("default=%s", get.Body)
	}
	for _, body := range []string{`{"enabled":true,"seconds":1}`, `{"enabled":true,"seconds":1801}`} {
		w := httptest.NewRecorder()
		s.setCacheDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/cache", bytes.NewBufferString(body)))
		if w.Code != 400 {
			t.Fatalf("bad window=%d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.setCacheDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/cache", bytes.NewBufferString(`{"enabled":true,"seconds":30}`)))
	if w.Code != 200 || !agent.CacheTraceState().Enabled {
		t.Fatal("enable failed")
	}
	w = httptest.NewRecorder()
	s.setCacheDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/cache", bytes.NewBufferString(`{"enabled":false}`)))
	if w.Code != 200 || agent.CacheTraceState().Enabled {
		t.Fatal("disable failed")
	}
}
