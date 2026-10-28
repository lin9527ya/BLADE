package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// 对话侧栏服务端分页：窗口、总数、按 agent 过滤与分 agent 计数必须自洽；
// 不带分页参数时保持全量行为；单项接口用于深链接兜底。
func TestConversationsPagination(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	pg := s.m.pg
	// 独立的 agent key，避免与库里已有对话互相影响计数断言。
	const keyA, keyB = "page-test-a", "page-test-b"
	mk := func(key, title string) int64 {
		c, err := pg.CreateConversation(key, title, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM conversations WHERE id=$1`, c.ID) })
		return c.ID
	}
	a1 := mk(keyA, "a-1")
	a2 := mk(keyA, "a-2")
	mk(keyB, "b-1")

	get := func(query string) (int, struct {
		Conversations []map[string]any `json:"conversations"`
		Total         int              `json:"total"`
		AgentCounts   map[string]int   `json:"agent_counts"`
	}) {
		var out struct {
			Conversations []map[string]any `json:"conversations"`
			Total         int              `json:"total"`
			AgentCounts   map[string]int   `json:"agent_counts"`
		}
		w := request(http.MethodGet, "/api/conversations"+query, "")
		if w.Code != 200 {
			t.Fatalf("list %s: %d %s", query, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v %s", query, err, w.Body)
		}
		return w.Code, out
	}
	ids := func(rows []map[string]any) map[int]bool {
		set := map[int]bool{}
		for _, row := range rows {
			if id, ok := row["id"].(float64); ok {
				set[int(id)] = true
			}
		}
		return set
	}

	// 全量（无分页参数）：三条都在，且没有 total/agent_counts。
	w := request(http.MethodGet, "/api/conversations", "")
	if w.Code != 200 {
		t.Fatalf("legacy list: %d", w.Code)
	}
	var legacy struct {
		Conversations []map[string]any `json:"conversations"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &legacy)
	all := ids(legacy.Conversations)
	for _, id := range []int64{a1, a2} {
		if !all[int(id)] {
			t.Fatalf("legacy full list missing %d", id)
		}
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	if _, ok := raw["total"]; ok {
		t.Fatal("legacy response must not change shape")
	}

	// 按 agent 过滤：只回该 agent 的两条，total 也是 2。
	_, byA := get("?offset=0&limit=100&agent=" + keyA)
	got := ids(byA.Conversations)
	if len(got) != 2 || !got[int(a1)] || !got[int(a2)] {
		t.Fatalf("agent filter rows = %v", got)
	}
	if byA.Total != 2 {
		t.Fatalf("agent filter total = %d, want 2", byA.Total)
	}
	if byA.AgentCounts[keyA] != 2 || byA.AgentCounts[keyB] != 1 {
		t.Fatalf("agent counts = %v", byA.AgentCounts)
	}

	// 窗口：limit=1 只回一条，total 仍是 2；offset=1 回另一条，不重叠。
	_, first := get("?offset=0&limit=1&agent=" + keyA)
	_, second := get(fmt.Sprintf("?offset=1&limit=1&agent=%s", keyA))
	if len(first.Conversations) != 1 || len(second.Conversations) != 1 {
		t.Fatalf("window sizes = %d/%d", len(first.Conversations), len(second.Conversations))
	}
	if first.Total != 2 || second.Total != 2 {
		t.Fatalf("window totals = %d/%d", first.Total, second.Total)
	}
	if ids(first.Conversations)[int(a1)] && ids(second.Conversations)[int(a1)] {
		t.Fatal("window overlap")
	}

	// limit 夹取：非法值走默认，超大值不超过 500。
	_, clamped := get("?offset=-5&limit=9999&agent=" + keyA)
	if len(clamped.Conversations) != 2 {
		t.Fatalf("clamped rows = %d", len(clamped.Conversations))
	}

	// 单项接口：深链接兜底。
	w = request(http.MethodGet, fmt.Sprintf("/api/conversations/%d", a1), "")
	if w.Code != 200 {
		t.Fatalf("get one: %d %s", w.Code, w.Body)
	}
	var one map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &one)
	if int(one["id"].(float64)) != int(a1) || one["agent_key"] != keyA {
		t.Fatalf("get one body = %v", one)
	}
	if w := request(http.MethodGet, "/api/conversations/999999999", ""); w.Code != 404 {
		t.Fatalf("missing conversation = %d, want 404", w.Code)
	}
}
