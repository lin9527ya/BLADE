package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestTaskDetailMetricsMatchList(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	createTask := func(name string) *Task {
		t.Helper()
		task, err := m.CreateTask(name, "fixture", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		id := mustTaskID(t, task.ID)
		t.Cleanup(func() {
			if _, err := m.pg.Exec(`DELETE FROM findings WHERE task_id=$1`, id); err != nil {
				t.Error(err)
			}
			if err := m.pg.DeleteTask(id); err != nil {
				t.Error(err)
			}
		})
		return task
	}
	task := createTask("detail metrics")
	other := createTask("other task metrics")
	if _, err := m.pg.Exec(`INSERT INTO activity(exploration_id,kind,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens)
VALUES ($1,'result',999,888,777,111)`, other.ExpID); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m, engine: NewEngine(m), ctx: t.Context()}
	mux := http.NewServeMux()
	s.registerTaskRoutes(mux)
	mux.HandleFunc("GET /api/exploration/tokens", s.tokenStats)
	request := func(t *testing.T, path string, out any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	check := func(t *testing.T, want TaskDTO) {
		t.Helper()
		var detail TaskDTO
		request(t, "/api/tasks/"+task.ID, &detail)
		if detail.Tokens != want.Tokens || detail.LastActivity != want.LastActivity ||
			detail.GoalsTotal != want.GoalsTotal || detail.GoalsMet != want.GoalsMet ||
			detail.InFlight != want.InFlight || detail.Findings != want.Findings {
			t.Fatalf("detail metrics=%+v, want=%+v", detail, want)
		}
		for _, path := range []string{"/api/tasks", "/api/tasks?page=1&size=50"} {
			var list struct {
				Tasks []TaskDTO `json:"tasks"`
			}
			request(t, path, &list)
			found := false
			for _, item := range list.Tasks {
				if item.ID != task.ID {
					continue
				}
				found = true
				if !reflect.DeepEqual(detail, item) {
					t.Fatalf("detail and %s differ: detail=%+v list=%+v", path, detail, item)
				}
			}
			if !found {
				t.Fatalf("task %s missing from %s", task.ID, path)
			}
		}
		var snapshot db.TokenSnapshot
		request(t, "/api/exploration/tokens?task="+task.ID, &snapshot)
		if detail.Tokens != (TokenTotalDTO{snapshot.Total.InputTokens, snapshot.Total.OutputTokens, snapshot.Total.CacheReadTokens, snapshot.Total.CacheWriteTokens}) {
			t.Fatalf("detail tokens=%+v, snapshot total=%+v", detail.Tokens, snapshot.Total)
		}
	}

	t.Run("empty task does not inherit another task metrics", func(t *testing.T) {
		check(t, TaskDTO{})
	})

	latestAt := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	if _, err := m.pg.Exec(`INSERT INTO activity(exploration_id,worker,slot_no,kind,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,created_at)
VALUES ($1,'planner',NULL,'result',11,7,3,2,$2),
       ($1,'work#1',1,'result',17,5,6,1,$2),
       ($1,'work#1',1,'usage',200,100,50,20,$2),
       ($1,'work#1',1,'tool_result',999,999,999,999,$2)`, task.ExpID, latestAt); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state)
VALUES ($1,'goal','{}','met'),($1,'goal','{}','open'),
       ($1,'intent','{}','running'),($1,'intent','{}','open')`, task.ExpID); err != nil {
		t.Fatal(err)
	}
	for _, severity := range []string{"critical", "high", "medium", "low"} {
		if _, err := m.pg.AddFinding(mustTaskID(t, task.ID), 0, "sqli", "fixture", severity, "fixture", "fixture", "test", nil); err != nil {
			t.Fatal(err)
		}
	}
	want := TaskDTO{
		Tokens:       TokenTotalDTO{InputTokens: 28, OutputTokens: 12, CacheReadTokens: 9, CacheWriteTokens: 3},
		LastActivity: latestAt.Unix(),
		GoalsTotal:   2,
		GoalsMet:     1,
		InFlight:     1,
		Findings:     FindingSeverityDTO{Critical: 1, High: 1, Medium: 1, Low: 1},
	}
	t.Run("recorded metrics exclude usage and tool frames", func(t *testing.T) {
		check(t, want)
	})
	t.Run("live heartbeat uses the same preference", func(t *testing.T) {
		want.LastActivity = latestAt.Add(time.Minute).Unix()
		s.engine.lastAct.Store(task.ID, want.LastActivity)
		check(t, want)
	})
	t.Run("missing task remains not found", func(t *testing.T) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/tasks/-1", nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("missing task status=%d, want 404", w.Code)
		}
	})
}
