package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 复核记录端点只返回看门狗自己的结论，不把整条活动流倒出来——
// 任务跑到上百轮之后，人要看的是"谁拦下了收工、为什么"。
func TestExhaustionReviewsEndpoint(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("exhaustion api", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(tid)

	emit := func(kind, summary string) {
		if _, err := task.Store.AppendActivity(db.Activity{Worker: "system", Kind: kind, Summary: summary}); err != nil {
			t.Fatal(err)
		}
	}
	emit("system", "穷尽复核未通过：证据不足以支撑「已无可行方向」，任务继续")
	emit("system", "穷尽复核通过：证据支持「策略内已无可行方向」")
	emit("system", "无关的系统留言")
	emit("tool_use", "穷尽复核未通过：这是工具调用记录，不该出现")

	s := &Server{m: m}
	req := httptest.NewRequest(http.MethodGet, "/api/exploration/exhaustion-reviews?task="+task.ID, nil)
	rec := httptest.NewRecorder()
	s.exhaustionReviews(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "穷尽复核未通过") || !strings.Contains(body, "穷尽复核通过") {
		t.Fatalf("应返回两条复核记录：%s", body)
	}
	if strings.Contains(body, "无关的系统留言") {
		t.Fatalf("不该把无关系统留言带出来：%s", body)
	}
}
