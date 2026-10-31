package server

import (
	"strings"
	"testing"
	"time"
)

// TestHTTPBucketOfIsBounded 钉住分桶键的基数上限：带 id 的路径必须收敛到按资源的桶，
// 否则指标本身会变成内存泄漏（每个任务/会话一个桶）。
func TestHTTPBucketOfIsBounded(t *testing.T) {
	cases := map[string]string{
		"/api/tasks":                 "/api/tasks",
		"/api/tasks/123":             "/api/tasks",
		"/api/tasks/123/activity":    "/api/tasks",
		"/api/notify/channels/7":     "/api/notify/channels",
		"/api/notify/channels":       "/api/notify/channels",
		"/api/exploration/activity":  "/api/exploration/activity",
		"/api/exploration/123/nodes": "/api/exploration",
		"/index.html":                "/",
		"/":                          "/",
	}
	for path, want := range cases {
		if got := httpBucketOf(path); got != want {
			t.Fatalf("httpBucketOf(%q) = %q，期望 %q", path, got, want)
		}
	}
}

// TestObserveRequestCountsAndLatency 钉住计数与延迟直方图的语义：
// 5xx 记错误、4xx 记客户端错误、慢请求单独计数、分位数落在档位上。
func TestObserveRequestCountsAndLatency(t *testing.T) {
	m := &httpMetrics{byBucket: map[string]*httpBucket{}}
	record := func(path string, status int, d time.Duration) { m.observe(path, status, d) }
	record("/api/tasks", 200, 5*time.Millisecond)
	record("/api/tasks/1", 200, 20*time.Millisecond)
	record("/api/tasks/2", 500, 1200*time.Millisecond)
	record("/api/tasks/3", 404, 2*time.Millisecond)

	snap := m.snapshot()
	if snap["total"].(int64) != 4 {
		t.Fatalf("total = %v", snap["total"])
	}
	rows := snap["by_path"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("应按 /api/tasks 收敛成一个桶，得到 %d 个：%v", len(rows), rows)
	}
	row := rows[0]
	if row["path"] != "/api/tasks" || row["count"].(int64) != 4 {
		t.Fatalf("桶不对：%v", row)
	}
	if row["errors_5xx"].(int64) != 1 || row["errors_4xx"].(int64) != 1 {
		t.Fatalf("错误计数不对：%v", row)
	}
	// 1.2s 那条必须落进慢请求计数（slowRequestThreshold 是 1s）。
	if snap["slow_total"].(int64) != 1 {
		t.Fatalf("慢请求计数 = %v", snap["slow_total"])
	}
	if p := row["p95_ms"].(float64); p < 1000 {
		t.Fatalf("p95 应落在 1200ms 那一档或更高，得到 %v", p)
	}
	if !strings.Contains(snap["note"].(string), "近似") {
		t.Fatal("快照应如实说明分位数是直方图近似值")
	}
}
