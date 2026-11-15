package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/sched"
)

// 计划任务运维台读取 schedule_runs：按页返回触发历史，任务 id/错误字段原样透出。
func TestTaskScheduleRunsHistory(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	freq := json.RawMessage(`{"kind":"every_hours","every_hours":1}`)
	sched, err := s.m.pg.CreateTaskSchedule("ops-history-test", true,
		json.RawMessage(`{"description":"历史测试","goal":"验证"}`), freq, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM task_schedules WHERE id=$1`, sched.ID) })
	if _, err := s.m.pg.Exec(`
INSERT INTO schedule_runs(schedule_id, scheduled_at, state, task_id, error) VALUES
($1, now() - interval '2 hours', 'created', '101', ''),
($1, now() - interval '1 hour', 'failed', '', '模型配置失效'),
($1, now(), 'created', '102', '')`, sched.ID); err != nil {
		t.Fatal(err)
	}

	w := request(http.MethodGet, "/api/task-schedules/"+strconv.FormatInt(sched.ID, 10)+"/runs?page=1&size=2", "")
	if w.Code != http.StatusOK {
		t.Fatalf("runs = %d %s", w.Code, w.Body)
	}
	var got struct {
		Runs []struct {
			State  string `json:"state"`
			TaskID string `json:"task_id"`
			Error  string `json:"error"`
		} `json:"runs"`
		Total int `json:"total"`
		Page  int `json:"page"`
		Size  int `json:"size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || got.Page != 1 || got.Size != 2 || len(got.Runs) != 2 {
		t.Fatalf("page1 = %+v", got)
	}
	if got.Runs[0].TaskID != "102" || got.Runs[1].State != "failed" || got.Runs[1].Error == "" {
		t.Fatalf("runs order/content = %+v", got.Runs)
	}

	w = request(http.MethodGet, "/api/task-schedules/"+strconv.FormatInt(sched.ID, 10)+"/runs?page=2&size=2", "")
	if w.Code != http.StatusOK || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("page2 = %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 1 || got.Runs[0].TaskID != "101" {
		t.Fatalf("page2 = %+v", got)
	}

	w = request(http.MethodGet, "/api/task-schedules/999999999/runs", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing schedule = %d %s", w.Code, w.Body)
	}
}

func TestCreateOneTimePauseScheduleWithBeijingTimezone(t *testing.T) {
	s, f, request := trafficEvidenceServer(t)
	full, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || full.TaskID == nil {
		t.Fatalf("fixture finding: %+v %v", full, err)
	}
	taskID := strconv.FormatInt(*full.TaskID, 10)
	if _, ok := s.m.Task(taskID); !ok {
		t.Fatalf("fixture task %s not loaded", taskID)
	}
	date := time.Now().Add(24 * time.Hour).Format("2006-01-02")
	body := `{"name":"一次性暂停","action":"pause","target_task_id":"` + taskID + `",` +
		`"timezone":"Asia/Shanghai","frequency":{"kind":"once_at","date":"` + date + `","at":"09:30"}}`
	w := request(http.MethodPost, "/api/task-schedules", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	var got struct {
		ID            int64  `json:"id"`
		Action        string `json:"action"`
		ActionText    string `json:"action_text"`
		TargetTaskID  string `json:"target_task_id"`
		Timezone      string `json:"timezone"`
		NextRunAt     string `json:"next_run_at"`
		FrequencyText string `json:"frequency_text"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != "pause" || got.TargetTaskID != taskID || got.Timezone != "Asia/Shanghai" || got.NextRunAt == "" {
		t.Fatalf("created schedule = %+v", got)
	}
	if !strings.Contains(got.FrequencyText, "仅一次") {
		t.Fatalf("frequency_text=%q", got.FrequencyText)
	}
	_, _ = s.m.pg.Exec(`DELETE FROM task_schedules WHERE id=$1`, got.ID)
}

func TestDueSchedulePausesTargetTask(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("scheduled pause target", "be paused by schedule", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	if err := m.SetTaskStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	next := time.Now().Add(-time.Minute).Truncate(time.Second)
	sched, err := m.pg.CreateTaskScheduleFull("pause-due", true,
		json.RawMessage(`{}`), json.RawMessage(`{"kind":"once_at","date":"2020-01-01","at":"09:30"}`),
		"pause", task.ID, "Asia/Shanghai", "", &next)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM task_schedules WHERE id=$1`, sched.ID) })
	s := newAdmissionTestServer(m, nil)
	sc := newScheduler(s)
	sc.runDueSchedules(context.Background())
	taskNum, _ := strconv.ParseInt(task.ID, 10, 64)
	row, err := m.pg.GetTask(taskNum)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || !row.Paused {
		t.Fatalf("scheduled pause did not pause target: %+v", row)
	}
	runs, err := m.pg.ListScheduleRuns(context.Background(), sched.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].State != "applied" {
		t.Fatalf("schedule runs=%+v err=%v", runs, err)
	}
}

func TestCreateTaskScheduleDerivesAutoPause(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("auto pause target", "will be paused", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	s := &Server{m: m}
	parent := &db.TaskSchedule{Name: "daily scan", Timezone: "Asia/Shanghai", AutoPauseAt: "23:59"}
	s.createAutoPauseSchedule(parent, task, time.Now())
	var (
		id       int64
		action   string
		target   string
		freqRaw  []byte
		nextRun  *time.Time
		timezone string
	)
	err = m.pg.QueryRow(`SELECT id, action, target_task_id, frequency, next_run_at, timezone
FROM task_schedules WHERE target_task_id=$1 AND action='pause' ORDER BY id DESC LIMIT 1`, task.ID).
		Scan(&id, &action, &target, &freqRaw, &nextRun, &timezone)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM task_schedules WHERE id=$1`, id) })
	if action != "pause" || target != task.ID || nextRun == nil || timezone != "Asia/Shanghai" {
		t.Fatalf("child=%d action=%s target=%s next=%v tz=%s", id, action, target, nextRun, timezone)
	}
	var freq sched.Frequency
	if err := json.Unmarshal(freqRaw, &freq); err != nil || freq.Kind != sched.KindOnceAt || freq.At != "23:59" {
		t.Fatalf("child frequency=%+v err=%v", freq, err)
	}
}
