package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestStorageTasksInspectAndScopedClean(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { m.Close() })
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)

	taskIdleHandle, err := m.CreateTaskWithOptions("idle-clean-target", "test scoped clean", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	taskActiveHandle, err := m.CreateTaskWithOptions("active-protected-target", "test active guard", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	idleID := taskIdleHandle.ID
	activeID := taskActiveHandle.ID
	idleNum, _ := strconv.ParseInt(idleID, 10, 64)
	activeNum, _ := strconv.ParseInt(activeID, 10, 64)
	taskIdle, err := m.pg.GetTask(idleNum)
	if err != nil || taskIdle == nil {
		t.Fatalf("get idle task: %v", err)
	}
	taskActive, err := m.pg.GetTask(activeNum)
	if err != nil || taskActive == nil {
		t.Fatalf("get active task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.DeleteTaskCascade(taskIdle.ID, true, true, true)
		_, _ = m.pg.DeleteTaskCascade(taskActive.ID, true, true, true)
	})

	// Make taskIdle paused so it is loaded & idle, and keep taskActive running/queued.
	if _, err := m.pg.Exec(`UPDATE tasks SET status='paused' WHERE id=$1`, taskIdle.ID); err != nil {
		t.Fatal(err)
	}
	taskIdleHandle.updateLifecycle(func(st *taskLifecycleState) {
		st.Status = db.TaskStatusPaused
		st.Paused = true
	})
	if _, err := m.pg.Exec(`UPDATE tasks SET status='running' WHERE id=$1`, taskActive.ID); err != nil {
		t.Fatal(err)
	}
	taskActiveHandle.updateLifecycle(func(st *taskLifecycleState) {
		st.Status = db.TaskStatusRunning
		st.Queued = true
	})

	// Seed disk workspace + transcript + orphan workspace + orphan transcript.
	idleDir := filepath.Join(s.m.dir, "tasks", idleID, "i1")
	if err := os.MkdirAll(idleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(idleDir, "dump.bin"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	transcriptsDir := filepath.Join(s.m.dir, "transcripts")
	if err := os.MkdirAll(transcriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	idleTranscript := filepath.Join(transcriptsDir, "exp"+strconv.FormatInt(taskIdle.ExplorationID, 10)+"-planner.jsonl")
	activeTranscript := filepath.Join(transcriptsDir, "exp"+strconv.FormatInt(taskActive.ExplorationID, 10)+"-planner.jsonl")
	orphanTranscript := filepath.Join(transcriptsDir, "exp999999-planner.jsonl")
	for _, p := range []string{idleTranscript, activeTranscript, orphanTranscript} {
		if err := os.WriteFile(p, make([]byte, 512), 0o644); err != nil {
			t.Fatal(err)
		}
		longAgo := time.Now().Add(-60 * 24 * time.Hour)
		_ = os.Chtimes(p, longAgo, longAgo)
	}
	orphanDir := filepath.Join(s.m.dir, "tasks", "999998")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "old.dat"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(s.m.dir, "tasks", idleID))
		_ = os.RemoveAll(orphanDir)
		_ = os.RemoveAll(idleTranscript)
		_ = os.RemoveAll(activeTranscript)
		_ = os.RemoveAll(orphanTranscript)
	})

	// Seed DB activity, side_question, and preserved finding for idle task.
	if _, err := m.pg.Exec(`INSERT INTO activity(exploration_id, worker, kind, summary, created_at) VALUES ($1,'worker','step','s1',now())`, taskIdle.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.AddFinding(taskIdle.ID, 0, "xss", "preserve-me", "medium", "sum", "ev", "worker", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`INSERT INTO side_question_sessions(session_key, task_id, exploration_id, run_id, version, snapshot) VALUES ($1,$2,$3,1,1,'{"k":"v"}')`,
		"sq-task-"+idleID, taskIdle.ID, taskIdle.ExplorationID); err != nil {
		t.Fatal(err)
	}

	// Verify global storageReport now marks active task transcript as Protected.
	rep, err := s.storageReport(t.Context())
	if err != nil {
		t.Fatalf("storageReport: %v", err)
	}
	foundProtectedTranscript := false
	foundSideQCategory := false
	for _, c := range rep.Categories {
		if c.Key == "db_side_question_sessions" {
			foundSideQCategory = true
		}
		if c.Key == "transcripts" {
			for _, u := range c.Items {
				if strings.HasPrefix(u.Name, "exp"+strconv.FormatInt(taskActive.ExplorationID, 10)+"-") && u.Protected != "" {
					foundProtectedTranscript = true
				}
			}
		}
	}
	if !foundProtectedTranscript {
		t.Fatal("active task transcript must be marked Protected in storageReport")
	}
	if !foundSideQCategory {
		t.Fatal("db_side_question_sessions must appear in global storageReport")
	}

	// Verify GET /api/storage/tasks sees idle task, orphan task dir, and orphan transcript.
	req := httptest.NewRequest(http.MethodGet, "/api/storage/tasks", nil)
	rec := httptest.NewRecorder()
	s.getStorageTasks(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("getStorageTasks status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Report storageTasksReport `json:"report"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	var gotIdle, gotOrphanDir, gotOrphanTranscript bool
	for _, it := range listResp.Report.Tasks {
		switch it.TaskID {
		case idleID:
			gotIdle = it.FilesBytes == 2048 && it.TranscriptsBytes == 512 && it.ActivityRows == 1 && it.SideQuestionRows == 1 && !it.ActiveRunning
		case "999998":
			gotOrphanDir = it.Orphan && it.FilesBytes == 1024
		case "exp-999999":
			gotOrphanTranscript = it.Orphan && it.TranscriptsBytes == 512
		}
	}
	if !gotIdle || !gotOrphanDir || !gotOrphanTranscript {
		t.Fatalf("missing expected task storage entries: idle=%v orphanDir=%v orphanTx=%v tasks=%+v",
			gotIdle, gotOrphanDir, gotOrphanTranscript, listResp.Report.Tasks)
	}

	// Clean idle + orphan + attempt active task: active must fail cleanly while idle & orphans are removed.
	cleanBody := `{"task_ids":["` + idleID + `","` + activeID + `","999998","exp-999999"],"scopes":["files","transcripts","activity","side_questions"]}`
	creq := httptest.NewRequest(http.MethodPost, "/api/storage/tasks/clean", strings.NewReader(cleanBody))
	crec := httptest.NewRecorder()
	s.cleanStorageTasks(crec, creq)
	if crec.Code != http.StatusOK {
		t.Fatalf("cleanStorageTasks status=%d body=%s", crec.Code, crec.Body.String())
	}
	var cleanResp struct {
		Cleaned    []storageTaskCleanEntry `json:"cleaned"`
		FreedBytes int64                   `json:"freed_bytes"`
		Failures   []string                `json:"failures"`
	}
	if err := json.Unmarshal(crec.Body.Bytes(), &cleanResp); err != nil {
		t.Fatal(err)
	}
	if len(cleanResp.Failures) != 1 || !strings.Contains(cleanResp.Failures[0], "#"+activeID) {
		t.Fatalf("expected active task refusal failure, got %v", cleanResp.Failures)
	}
	if _, err := os.Stat(filepath.Join(s.m.dir, "tasks", idleID)); !os.IsNotExist(err) {
		t.Fatalf("idle task files should be removed: %v", err)
	}
	if _, err := os.Stat(idleTranscript); !os.IsNotExist(err) {
		t.Fatalf("idle transcript should be removed: %v", err)
	}
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("orphan task dir should be removed: %v", err)
	}
	if _, err := os.Stat(orphanTranscript); !os.IsNotExist(err) {
		t.Fatalf("orphan transcript should be removed: %v", err)
	}
	if _, err := os.Stat(activeTranscript); err != nil {
		t.Fatalf("active task transcript must stay untouched: %v", err)
	}

	// Core finding and task row for idle task must survive scoped clean.
	keptTask, err := m.pg.GetTask(taskIdle.ID)
	if err != nil || keptTask == nil {
		t.Fatalf("idle task row must survive: task=%+v err=%v", keptTask, err)
	}
	var findingCnt, activityCnt, sqCnt int
	_ = m.pg.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, taskIdle.ID).Scan(&findingCnt)
	_ = m.pg.QueryRow(`SELECT count(*) FROM activity WHERE exploration_id=$1`, taskIdle.ExplorationID).Scan(&activityCnt)
	_ = m.pg.QueryRow(`SELECT count(*) FROM side_question_sessions WHERE task_id=$1`, taskIdle.ID).Scan(&sqCnt)
	if findingCnt != 1 || activityCnt != 0 || sqCnt != 0 {
		t.Fatalf("expected finding=1 activity=0 sq=0, got %d/%d/%d", findingCnt, activityCnt, sqCnt)
	}

	// Verify POST /api/storage/vacuum rejects bad table names and accepts valid table names.
	vbad := httptest.NewRequest(http.MethodPost, "/api/storage/vacuum", strings.NewReader(`{"tables":["users"]}`))
	vbadRec := httptest.NewRecorder()
	s.vacuumStorage(vbadRec, vbad)
	if vbadRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid vacuum table, got %d", vbadRec.Code)
	}
	vok := httptest.NewRequest(http.MethodPost, "/api/storage/vacuum", strings.NewReader(`{"tables":["server_logs"],"full":false}`))
	vokRec := httptest.NewRecorder()
	s.vacuumStorage(vokRec, vok)
	if vokRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on vacuum server_logs, got %d body=%s", vokRec.Code, vokRec.Body.String())
	}
}
