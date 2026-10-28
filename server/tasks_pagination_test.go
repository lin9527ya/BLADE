package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

// 任务列表服务端分页：过滤、排序、分页与总数必须自洽，且只对当页任务取指标。
func TestTaskListPagination(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	// 3 个可识别任务：两个 pin 名称，一个普通。
	makeTask := func(desc string) string {
		task, err := s.m.CreateTask(desc, "goal "+desc, nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = s.m.DeleteTask(task.ID, DeleteTaskOptions{}) })
		return task.ID
	}
	idA := makeTask("paginate-alpha")
	idB := makeTask("paginate-beta")
	idC := makeTask("paginate-gamma")

	get := func(query string) (int, struct {
		Tasks    []TaskDTO `json:"tasks"`
		Total    int       `json:"total"`
		Filtered int       `json:"filtered"`
		Page     int       `json:"page"`
		Size     int       `json:"size"`
	}) {
		w := request(http.MethodGet, "/api/tasks"+query, "")
		if w.Code != 200 {
			t.Fatalf("list %s: %d %s", query, w.Code, w.Body)
		}
		var out struct {
			Tasks    []TaskDTO `json:"tasks"`
			Total    int       `json:"total"`
			Filtered int       `json:"filtered"`
			Page     int       `json:"page"`
			Size     int       `json:"size"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v %s", err, w.Body)
		}
		return w.Code, out
	}

	// 全量（无分页参数）：保持旧行为，包含刚建的 3 个任务且无 total 字段语义。
	w := request(http.MethodGet, "/api/tasks", "")
	if w.Code != 200 {
		t.Fatalf("legacy list: %d", w.Code)
	}
	var all struct {
		Tasks []TaskDTO `json:"tasks"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &all)
	seen := map[string]bool{}
	for _, d := range all.Tasks {
		seen[d.ID] = true
	}
	for _, id := range []string{idA, idB, idC} {
		if !seen[id] {
			t.Fatalf("legacy full list missing %s", id)
		}
	}

	// q 过滤：只命中 beta。
	_, out := get("?page=1&size=20&q=paginate-beta")
	if out.Filtered != 1 || len(out.Tasks) != 1 || out.Tasks[0].ID != idB || out.Total < 3 {
		t.Fatalf("q filter = %+v (filtered=%d total=%d)", out.Tasks, out.Filtered, out.Total)
	}
	if out.Page != 1 || out.Size != 20 {
		t.Fatalf("page/size echo = %d/%d", out.Page, out.Size)
	}

	// 分页：size=2 时两页任务不重叠，且第二页只剩剩余项。
	_, p1 := get("?page=1&size=2&q=paginate-")
	_, p2 := get("?page=2&size=2&q=paginate-")
	if len(p1.Tasks) != 2 || p1.Filtered != 3 {
		t.Fatalf("page1 = %d tasks (filtered=%d), want 2/3", len(p1.Tasks), p1.Filtered)
	}
	if len(p2.Tasks) != 1 {
		t.Fatalf("page2 = %d tasks, want 1", len(p2.Tasks))
	}
	for _, a := range p1.Tasks {
		for _, b := range p2.Tasks {
			if a.ID == b.ID {
				t.Fatalf("page overlap: %s", a.ID)
			}
		}
	}

	// 状态过滤：不存在的状态 → 空页但 total 仍是全部任务数。
	_, none := get("?page=1&size=10&status=policy_blocked&q=paginate-")
	if none.Filtered != 0 || len(none.Tasks) != 0 || none.Total < 3 {
		t.Fatalf("status filter = %d tasks, filtered=%d total=%d", len(none.Tasks), none.Filtered, none.Total)
	}

	// 非法 page/size 被夹到合法范围。
	_, clamped := get("?page=-2&size=9999")
	if clamped.Page != 1 || clamped.Size != 200 {
		t.Fatalf("clamped page/size = %d/%d, want 1/200", clamped.Page, clamped.Size)
	}

	// 排序：按 id desc 时当页首个是最大 id。
	_, sorted := get("?page=1&size=200&q=paginate-&sort=id&dir=desc")
	if len(sorted.Tasks) < 3 {
		t.Fatalf("sorted rows = %d", len(sorted.Tasks))
	}
	if sorted.Tasks[0].ID != idC {
		t.Fatalf("id desc first = %s, want %s", sorted.Tasks[0].ID, idC)
	}

	// An extreme but syntactically valid page must be an empty page, not a panic
	// from overflowing (page-1)*size before slicing.
	maxInt := strconv.Itoa(int(^uint(0) >> 1))
	_, huge := get("?page=" + maxInt + "&size=200&q=paginate-")
	if len(huge.Tasks) != 0 || huge.Filtered != 3 {
		t.Fatalf("max page = %+v, want empty page with filtered=3", huge)
	}
}

func TestTaskPageBounds(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name              string
		total, page, size int
		start, end        int
	}{
		{"empty", 0, 1, 20, 0, 0},
		{"first", 3, 1, 2, 0, 2},
		{"second", 3, 2, 2, 2, 3},
		{"past end", 3, 3, 2, 3, 3},
		{"size past end", 3, 1, 200, 0, 3},
		{"max int", 3, maxInt, 200, 3, 3},
		{"invalid", 3, -1, 20, 0, 0},
	}
	for _, tc := range tests {
		start, end := taskPageBounds(tc.total, tc.page, tc.size)
		if start != tc.start || end != tc.end {
			t.Errorf("%s: got %d/%d, want %d/%d", tc.name, start, end, tc.start, tc.end)
		}
	}
}
