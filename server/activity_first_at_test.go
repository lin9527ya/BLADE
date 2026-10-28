package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 会话历史是倒序分页的：limit 小于总行数时，响应里的 first_activity 仍必须是
// 整个会话的首步时间，前端「运行时长（首步 → 末步）」才不会因分页而偏短。
func TestActivityHistoryFirstActivity(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	tasks := s.m.List()
	if len(tasks) == 0 {
		t.Fatal("no task in manager")
	}
	task := tasks[0]
	es := task.Store
	intent, err := es.AddIntent(map[string]any{"summary": "first-at test"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.Exec(`UPDATE exploration_nodes SET state='running', owner='work#1' WHERE id=$1`, intent); err != nil {
		t.Fatal(err)
	}
	slot := 1
	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := es.AppendActivity(db.Activity{
			NodeID: &intent, Worker: "work#1", Kind: "text",
			Summary: "step", SlotNo: &slot,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// 显式错开时间，避免同毫秒写入导致无法区分首步与窗口首步。
	base := time.Now().Add(-time.Hour).UTC()
	for i, id := range ids {
		if _, err := s.m.pg.Exec(`UPDATE activity SET created_at=$2 WHERE id=$1`, id, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	get := func(limit int) struct {
		Items         []map[string]any `json:"items"`
		FirstActivity string           `json:"first_activity"`
		HasMore       bool             `json:"has_more"`
	} {
		var out struct {
			Items         []map[string]any `json:"items"`
			FirstActivity string           `json:"first_activity"`
			HasMore       bool             `json:"has_more"`
		}
		w := request(http.MethodGet, "/api/exploration/activity/history?task="+task.ID+"&session=slot:1&limit="+strconv.Itoa(limit), "")
		if w.Code != 200 {
			t.Fatalf("history limit=%d: %d %s", limit, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	full := get(50)
	if len(full.Items) < 3 || full.FirstActivity == "" {
		t.Fatalf("full history = %d items, first=%q", len(full.Items), full.FirstActivity)
	}
	want, err := time.Parse(time.RFC3339, full.FirstActivity)
	if err != nil {
		t.Fatalf("first_activity not RFC3339: %q", full.FirstActivity)
	}
	// first_activity 是 RFC3339（秒精度）；库里写入的时间带纳秒，比较前截断。
	if !want.Equal(base.Truncate(time.Second)) {
		t.Fatalf("first_activity = %s, want %s", want, base.Truncate(time.Second))
	}

	// 只取最近 1 条：窗口首步是最后一步，first_activity 仍应是最早那一步。
	window := get(1)
	if len(window.Items) != 1 || !window.HasMore {
		t.Fatalf("window = %d items hasMore=%v", len(window.Items), window.HasMore)
	}
	if window.FirstActivity != full.FirstActivity {
		t.Fatalf("window first_activity = %q, want %q", window.FirstActivity, full.FirstActivity)
	}
}
