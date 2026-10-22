package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
)

// TestIntentsExposePerTaskWorkerSeq：会话列表的 Worker 编号来自 worker_seq（任务内
// 1-based），不是全局意图 id；两个任务的编号都从 1 开始。
func TestIntentsExposePerTaskWorkerSeq(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)

	mkTask := func(title string, n int) (string, []int64) {
		t.Helper()
		task, err := s.m.CreateTask(title, "goal", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		tid, _ := strconv.ParseInt(task.ID, 10, 64)
		st := task.Store
		ids := make([]int64, 0, n)
		for i := 0; i < n; i++ {
			id, err := st.AddIntent(map[string]any{"summary": fmt.Sprintf("%s-%d", title, i+1)}, 5, nil, "planner")
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, tid) })
		return task.ID, ids
	}
	t1, ids1 := mkTask("seq-one", 3)
	t2, ids2 := mkTask("seq-two", 2)
	if ids2[0] <= ids1[2] {
		t.Fatalf("fixture 前提不成立：第二个任务的意图 id 应更大（%v vs %v）", ids2, ids1)
	}

	check := func(taskID string, ids []int64) {
		t.Helper()
		// 分页形态（会话列表实际走的路径）。
		w := request("GET", fmt.Sprintf("/api/exploration/intents?task=%s&limit=50&before=0&page=1", taskID), "")
		if w.Code != 200 {
			t.Fatalf("intents page: %d %s", w.Code, w.Body)
		}
		var page struct {
			Items []struct {
				ID        string `json:"id"`
				WorkerSeq int64  `json:"worker_seq"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		got := map[string]int64{}
		for _, item := range page.Items {
			got[item.ID] = item.WorkerSeq
		}
		for i, id := range ids {
			if got[fmt.Sprint(id)] != int64(i+1) {
				t.Fatalf("task %s 第 %d 条意图 worker_seq=%d，want %d（items=%+v）",
					taskID, i+1, got[fmt.Sprint(id)], i+1, page.Items)
			}
		}
	}
	check(t1, ids1) // 3 条 → 1,2,3
	check(t2, ids2) // 全局 id 更大，但仍是 1,2
}
