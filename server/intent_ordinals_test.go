package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
)

// TestIntentsExposeGlobalIDs：全站统一使用全局递增意图 id，不再额外计算任务内
// worker_seq 局部序号，确保会话列表、播报板与工具寻址始终同一套编号。
func TestIntentsExposeGlobalIDs(t *testing.T) {
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

	assertGlobalItems := func(taskID string, rawItems []map[string]any, ids []int64) {
		t.Helper()
		seen := map[string]bool{}
		for _, item := range rawItems {
			id, _ := item["id"].(string)
			seen[id] = true
			if _, hasSeq := item["worker_seq"]; hasSeq {
				t.Fatalf("task %s id=%s 不应再返回 worker_seq: %+v", taskID, id, item)
			}
		}
		for _, id := range ids {
			if !seen[fmt.Sprint(id)] {
				t.Fatalf("task %s 缺少全局意图 id=%d（items=%+v）", taskID, id, rawItems)
			}
		}
	}

	check := func(taskID string, ids []int64) {
		t.Helper()
		// 分页形态（会话列表实际走的路径）。
		w := request("GET", fmt.Sprintf("/api/exploration/intents?task=%s&limit=50&before=0&page=1", taskID), "")
		if w.Code != 200 {
			t.Fatalf("intents page: %d %s", w.Code, w.Body)
		}
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		assertGlobalItems(taskID, page.Items, ids)

		// 非分页形态同样不应返回 worker_seq。
		wBare := request("GET", fmt.Sprintf("/api/exploration/intents?task=%s&limit=50", taskID), "")
		if wBare.Code != 200 {
			t.Fatalf("intents list: %d %s", wBare.Code, wBare.Body)
		}
		var bare []map[string]any
		if err := json.Unmarshal(wBare.Body.Bytes(), &bare); err != nil {
			t.Fatal(err)
		}
		assertGlobalItems(taskID, bare, ids)
	}
	check(t1, ids1)
	check(t2, ids2)

	// 验证 exclude_deleted=1 过滤已假删除的意图（不影响默认不带参数的全量返回）。
	t1Task := s.m.ResolveTask(t1)
	if t1Task == nil {
		t.Fatal("resolve t1 failed")
	}
	deletedID := ids1[len(ids1)-1]
	if _, _, err := t1Task.Store.SoftDeleteIntent(deletedID, "noise"); err != nil {
		t.Fatalf("SoftDeleteIntent: %v", err)
	}
	wFiltered := request("GET", fmt.Sprintf("/api/exploration/intents?task=%s&limit=50&before=0&page=1&exclude_deleted=1", t1), "")
	if wFiltered.Code != 200 {
		t.Fatalf("intents exclude_deleted=1: %d %s", wFiltered.Code, wFiltered.Body)
	}
	var filtered struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(wFiltered.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	assertGlobalItems(t1, filtered.Items, ids1[:len(ids1)-1])
	for _, item := range filtered.Items {
		if item["id"] == fmt.Sprint(deletedID) {
			t.Fatalf("exclude_deleted=1 不应再返回已删除意图 #%d", deletedID)
		}
	}
}
