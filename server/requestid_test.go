package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var reHexID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// captureLog 把标准日志临时接到缓冲区，返回取内容的函数。
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf.String
}

func TestRequestIDAssignedAndEchoed(t *testing.T) {
	var seen string
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestID(r.Context())
		w.WriteHeader(200)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))

	got := rr.Header().Get("X-Request-Id")
	if !reHexID.MatchString(got) {
		t.Fatalf("没生成 16 位十六进制 id，得到 %q", got)
	}
	if seen != got {
		t.Fatalf("ctx 里的 id (%q) 与响应头 (%q) 不一致", seen, got)
	}
}

// 上游带合法 id 时要沿用——反代/网关串链路靠它。
func TestRequestIDFromUpstream(t *testing.T) {
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("X-Request-Id", "upstream-abc_123")
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Request-Id"); got != "upstream-abc_123" {
		t.Fatalf("上游 id 未被沿用，得到 %q", got)
	}
}

// 注入防护：非法上游 id 一律丢弃、改用自己生成的。日志会进环形缓冲、SSE 与
// server_logs 再显示到日志页，放行一个带换行的 id 等于允许伪造整行日志。
func TestRequestIDRejectsInjection(t *testing.T) {
	cases := map[string]string{
		"换行注入": "abc\n[fake] 伪造的一行",
		"回车注入": "abc\r\n[fake] 伪造的一行",
		"含空格":  "abc def",
		"含方括号": "[fake]abc",
		"超长":   strings.Repeat("a", 65),
		"含中文":  "标识",
		"含制表符": "abc\tdef",
	}
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for name, bad := range cases {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		req.Header.Set("X-Request-Id", bad)
		h.ServeHTTP(rr, req)

		got := rr.Header().Get("X-Request-Id")
		if got == bad {
			t.Errorf("%s：非法 id 被原样放行（%q）", name, got)
			continue
		}
		if !reHexID.MatchString(got) {
			t.Errorf("%s：应换成自己生成的 id，得到 %q", name, got)
		}
	}
}

// 边界：恰好 64 位合法 id 应当沿用（别把上限写成了 <64）。
func TestRequestIDAcceptsMaxLength(t *testing.T) {
	id := strings.Repeat("a", 64)
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("X-Request-Id", id)
	h.ServeHTTP(rr, req)
	if got := rr.Header().Get("X-Request-Id"); got != id {
		t.Fatalf("64 位合法 id 应沿用，得到 %q", got)
	}
}

// 出错必记；正常且快的请求不记（前端十几处轮询，全量记会把环形缓冲冲掉）。
func TestRequestIDLogsErrorsNotFastSuccess(t *testing.T) {
	read := captureLog(t)

	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/boom") {
			writeErr(w, 500, "炸了")
			return
		}
		w.WriteHeader(200)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/ok", nil))
	if out := read(); out != "" {
		t.Errorf("正常且快的请求不该记访问日志，得到 %q", out)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/boom", nil))
	out := read()
	if !strings.Contains(out, "[req]") || !strings.Contains(out, "500") || !strings.Contains(out, "/api/boom") {
		t.Fatalf("出错请求应记一条带状态码与路径的访问日志，得到 %q", out)
	}
	if id := rr.Header().Get("X-Request-Id"); !strings.Contains(out, id) {
		t.Fatalf("访问日志里的 id 应与响应头一致（%q），得到 %q", id, out)
	}
}

// 跨源 SSE 把凭据放在 ?token= 里：日志只记 path，绝不能把 query 落盘。
func TestRequestIDLogOmitsQuery(t *testing.T) {
	read := captureLog(t)
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, 401, "未授权")
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/logs/stream?token=super-secret", nil))
	out := read()
	if strings.Contains(out, "super-secret") || strings.Contains(out, "token=") {
		t.Fatalf("访问日志泄漏了 query（含凭据），得到 %q", out)
	}
	if !strings.Contains(out, "/api/logs/stream") {
		t.Fatalf("访问日志应保留路径，得到 %q", out)
	}
}

// 预检不该产生访问日志（浏览器每次跨源请求都会发）。
func TestRequestIDSkipsPreflight(t *testing.T) {
	read := captureLog(t)
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, "/api/tasks", nil))
	if out := read(); out != "" {
		t.Errorf("OPTIONS 预检不该记访问日志，得到 %q", out)
	}
	if rr.Header().Get("X-Request-Id") == "" {
		t.Error("预检也该带上 X-Request-Id")
	}
}

// SSE 是长连接，断开时耗时必然「很长」——那不是慢，不该被记成慢请求。
func TestRequestIDSkipsSSE(t *testing.T) {
	read := captureLog(t)
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/logs/stream", nil))
	if out := read(); out != "" {
		t.Errorf("SSE 响应不该记访问日志，得到 %q", out)
	}
}

// 包了记录器之后 SSE 端点仍要能拿到 http.Flusher——拿不到就等于把流式变成缓冲。
func TestStatusRecorderKeepsFlusher(t *testing.T) {
	flushed := false
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("包了 statusRecorder 之后拿不到 http.Flusher")
			return
		}
		f.Flush()
		flushed = true
	}))

	rr := httptest.NewRecorder() // httptest.ResponseRecorder 实现了 Flush
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/logs/stream", nil))
	if !flushed {
		t.Fatal("handler 未走到 Flush")
	}
	if !rr.Flushed {
		t.Fatal("Flush 没有透传到底层 writer")
	}
}
