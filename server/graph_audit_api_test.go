package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 体检端点：只读、按任务解析，坏图要把码报出来；任务不存在是 404 而不是空报告。
func TestExplorationInvariantsEndpoint(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	task, err := m.CreateTask("graph audit api", "体检端点", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	t.Cleanup(func() { _ = m.pg.DeleteTask(tid) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())

	token, err := signJWT(s.auth.signKey.get())
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}

	// 干净图：200 + 无问题。
	w := request("/api/exploration/invariants?task=" + task.ID)
	if w.Code != 200 {
		t.Fatalf("clean: %d %s", w.Code, w.Body)
	}
	var rep db.GraphAuditReport
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body)
	}
	if len(rep.Issues) != 0 || !rep.Clean() {
		t.Fatalf("干净图应无问题：%s", w.Body)
	}

	// 软删除一条前置：报告里要出现 deleted_prerequisite。
	a, err := task.Store.AddIntent(map[string]any{"summary": "下游"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	b, err := task.Store.AddIntent(map[string]any{"summary": "上游"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.Link(a, db.RelDependsOn, b); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`UPDATE exploration_nodes SET state='deleted' WHERE id=$1`, b); err != nil {
		t.Fatal(err)
	}
	w = request("/api/exploration/invariants?task=" + task.ID)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "deleted_prerequisite") {
		t.Fatalf("坏图应报 deleted_prerequisite：%d %s", w.Code, w.Body)
	}

	// 不存在的任务：404，而不是一份空报告（空报告会被误读成"图是好的"）。
	if w := request("/api/exploration/invariants?task=does-not-exist"); w.Code != 404 {
		t.Fatalf("未知任务应 404，实际 %d %s", w.Code, w.Body)
	}
}
