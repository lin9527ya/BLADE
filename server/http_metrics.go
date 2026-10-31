package server

import (
	"sync"
	"sync/atomic"
	"time"
)

// httpMetrics 是进程级的 HTTP 请求统计（/api/metrics 的 "http" 段）。
//
// 为什么需要：此前只有"慢请求/错误请求写一行日志"，没有计数器——操作者无法回答
// "现在 QPS 多少 / 错误率多少 / p95 多少"，只能翻日志估。这恰好是最基本的一层。
//
// 刻意**按路径分桶用粗粒度**（第一段，如 /api/tasks），而不是按完整路径：
// 后者在带 id 的路由上会制造无界基数（每个任务一个桶），把指标本身变成内存问题。
type httpMetrics struct {
	mu       sync.Mutex
	byBucket map[string]*httpBucket
	total    atomic.Int64
	slow     atomic.Int64
}

type httpBucket struct {
	count   int64
	errors  int64 // status >= 500
	client  int64 // 400 <= status < 500
	latency latencyHist
}

// latencyHist 是固定边界的延迟直方图（单位毫秒），边界选在"人关心的档位"上。
var latencyBoundsMs = [...]float64{10, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

type latencyHist struct {
	counts  [len(latencyBoundsMs) + 1]int64
	sumMs   float64
	samples int64
}

func (h *latencyHist) observe(ms float64) {
	h.sumMs += ms
	h.samples++
	for i, b := range latencyBoundsMs {
		if ms <= b {
			h.counts[i]++
			return
		}
	}
	h.counts[len(latencyBoundsMs)]++
}

// quantile 返回近似分位数（按直方图桶的上界给出，属"档位内"的保守估计）。
func (h *latencyHist) quantile(q float64) float64 {
	if h.samples == 0 {
		return 0
	}
	target := int64(float64(h.samples) * q)
	if target < 1 {
		target = 1
	}
	var seen int64
	for i, c := range h.counts {
		seen += c
		if seen >= target {
			if i < len(latencyBoundsMs) {
				return latencyBoundsMs[i]
			}
			return latencyBoundsMs[len(latencyBoundsMs)-1]
		}
	}
	return latencyBoundsMs[len(latencyBoundsMs)-1]
}

var httpStats = &httpMetrics{byBucket: map[string]*httpBucket{}}

// httpBucketOf 把路径收敛成一个有界的分桶键：/api/tasks/123/activity → /api/tasks。
// 非 /api 路径统一落 "/"（静态资源与页面，不逐个统计）。
func httpBucketOf(path string) string {
	const prefix = "/api/"
	if len(path) < len(prefix) || path[:len(prefix)] != prefix {
		return "/"
	}
	rest := path[len(prefix):]
	// 取前两段：/api/notify/channels/3 → /api/notify/channels。
	seg1 := rest
	if i := indexByte(rest, '/'); i >= 0 {
		seg1 = rest[:i]
	}
	key := "/api/" + seg1
	if i := indexByte(rest, '/'); i >= 0 && i+1 < len(rest) {
		tail := rest[i+1:]
		if j := indexByte(tail, '/'); j >= 0 {
			tail = tail[:j]
		}
		if tail != "" && !isAllDigits(tail) {
			key = key + "/" + tail
		}
	}
	return key
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// observe 记录一次请求。SSE 长连接由调用方跳过（它的"耗时"不是延迟）。
func (m *httpMetrics) observe(path string, status int, d time.Duration) {
	m.total.Add(1)
	ms := float64(d.Microseconds()) / 1000
	if ms >= float64(slowRequestThreshold.Milliseconds()) {
		m.slow.Add(1)
	}
	key := httpBucketOf(path)
	m.mu.Lock()
	b := m.byBucket[key]
	if b == nil {
		b = &httpBucket{}
		m.byBucket[key] = b
	}
	b.count++
	switch {
	case status >= 500:
		b.errors++
	case status >= 400:
		b.client++
	}
	b.latency.observe(ms)
	m.mu.Unlock()
}

// observeRequest 记录到进程级统计（中间件用）。
func observeRequest(path string, status int, d time.Duration) {
	httpStats.observe(path, status, d)
}

// snapshot 返回给 /api/metrics 用的快照（按请求数降序，取前 40 个桶）。
func (m *httpMetrics) snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	type row struct {
		key      string
		count    int64
		errors   int64
		client   int64
		p50, p95 float64
	}
	rows := make([]row, 0, len(m.byBucket))
	for k, b := range m.byBucket {
		rows = append(rows, row{k, b.count, b.errors, b.client, b.latency.quantile(0.5), b.latency.quantile(0.95)})
	}
	// 简单的插入排序前 40（桶数量有界，不值得引入 sort 的分配）。
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].count > rows[j-1].count; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	const maxRows = 40
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"path": r.key, "count": r.count, "errors_5xx": r.errors, "errors_4xx": r.client,
			"p50_ms": r.p50, "p95_ms": r.p95,
		})
	}
	return map[string]any{
		"total":      m.total.Load(),
		"slow_total": m.slow.Load(),
		"slow_ms":    slowRequestThreshold.Milliseconds(),
		"by_path":    out,
		"note":       "按路径前两段分桶（有界基数）；p50/p95 是直方图档位上的近似值",
	}
}
