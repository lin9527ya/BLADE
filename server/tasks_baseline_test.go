package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestTaskListBaseline is a manual, fixed-size measurement harness:
//
//	BLADE_BASELINE_TASKS=2000 BLADE_TEST_PG_DSN=... BLADE_REQUIRE_TESTS=1 \
//	  go test ./server -run TestTaskListBaseline -v
//
// It is skipped by default because it creates real task rows. Use it to compare
// in-memory pagination with a future PostgreSQL pushdown before changing the
// query path.
func TestTaskListBaseline(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("BLADE_BASELINE_TASKS"))
	if n <= 0 {
		t.Skip("set BLADE_BASELINE_TASKS to run the task-list baseline")
	}
	s, _, request := trafficEvidenceServer(t)
	stamp := time.Now().UnixNano()
	for i := 0; i < n; i++ {
		task, err := s.m.CreateTask(fmt.Sprintf("baseline-%d-%d", stamp, i), "baseline", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		id := task.ID
		t.Cleanup(func() { _, _ = s.m.DeleteTask(id, DeleteTaskOptions{}) })
	}

	for _, path := range []string{
		"/api/tasks?page=1&size=20&sort=id",
		"/api/tasks?page=1&size=20&sort=id",
		"/api/tasks?page=1&size=20&sort=duration",
		"/api/tasks?page=1&size=20&sort=duration",
		"/api/dashboard",
		"/api/dashboard",
	} {
		start := time.Now()
		w := request(http.MethodGet, path, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		t.Logf("%s: %s (%d bytes)", path, time.Since(start), w.Body.Len())
	}

	// Component breakdown: this distinguishes CPU sorting/serialization from DB
	// wait when deciding whether to push the task list down to PostgreSQL.
	all := s.m.List()
	q := taskListQuery{Page: 1, Size: 20, Sort: "id", Dir: "desc"}
	start := time.Now()
	filtered := s.filterAndSortTasks(context.Background(), all, q, time.Now().Unix())
	afterFilter := time.Since(start)
	start = time.Now()
	page := filtered[:min(20, len(filtered))]
	dtos := s.taskDTOsFor(context.Background(), page)
	afterDTO := time.Since(start)
	start = time.Now()
	payload, _ := json.Marshal(dtos)
	afterJSON := time.Since(start)
	t.Logf("components: list=%s filter/sort=%s pageDTO+metrics=%s marshal=%s payload=%d",
		time.Since(start), afterFilter, afterDTO, afterJSON, len(payload))
}
