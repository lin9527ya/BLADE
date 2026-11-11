package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
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
