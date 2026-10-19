package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// request-id：让「用户看到的那次失败」和「后端记的那几行日志」能对上。
//
// 此前一条请求与它的日志之间没有任何联系——用户说「我点某个按钮报错了」，只能靠时间
// 戳去猜是哪条。而且大量 handler 的失败路径**根本不打日志**（只 writeErr 回前端，例如
// server/assets.go 里十来个 500），所以访问日志往往是这类失败**唯一**的记录点。
//
// 做法：每个 /api/ 请求拿一个 id（上游带了 X-Request-Id 就用，过滤后），回写响应头
// 供前端/用户报出来，同时放进 ctx 供 handler 取（requestID(r.Context())）。
// 访问日志只记**出错**与**慢**两种请求——前端有十几处轮询（3s/5s/20s），全量记会把
// 3000 条的环形缓冲和 server_logs 一起冲掉，而排障真正要看的就是这两类。

type requestIDKey struct{}

// requestID 取当前请求的 id；没有时返回 ""。handler 侧用 requestID(r.Context())。
func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// newRequestID 生成 16 位十六进制 id。
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源不可用是极端情况：退回时间戳，宁可 id 不随机也不要卡住请求。
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// sanitizeRequestID 只接受 [A-Za-z0-9_-]{1,64} 的上游 id，不合规就当作没带。
//
// 必须过滤：日志会进环形缓冲、SSE 与 server_logs 表，再显示到日志页——一个带换行的
// X-Request-Id 能凭空伪造出整行日志（换行注入）。
func sanitizeRequestID(v string) string {
	if v == "" || len(v) > 64 {
		return ""
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return ""
		}
	}
	return v
}

// slowRequestThreshold 之上算「慢」，进访问日志。
const slowRequestThreshold = time.Second

// statusRecorder 记下状态码与响应字节数，供访问日志用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush 透传：日志/更新等 SSE 端点在包了记录器之后仍要能立刻把数据推出去
// （它们会断言 w.(http.Flusher)，不实现这一层就等于把流式响应变成缓冲）。
func (w *statusRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withRequestID 给每个请求分配 id 并记访问日志。包在最外层，这样 CORS 预检与
// requireAuth 的 401 也带上 id。
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(strings.TrimSpace(r.Header.Get("X-Request-Id")))
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)

		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		next.ServeHTTP(rec, r)

		if r.Method == http.MethodOptions {
			return
		}
		// SSE 是长连接，断开时耗时必然「很长」——那不是慢，不记。
		if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
			return
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		// 只记 r.URL.Path，**不记 RawQuery**：跨源 SSE 会把凭据放在 ?token= 里
		// （见前端 sseUrl），记进日志等于把 token 落进 server_logs 并显示在日志页。
		if elapsed := time.Since(start); status >= 400 || elapsed >= slowRequestThreshold {
			// 级别由状态码给出。以前走关键词兜底，一条 500 因为不含「失败」被判成 info。
			level := slog.LevelWarn
			if status >= 500 {
				level = slog.LevelError
			}
			slogFor("req").Log(r.Context(), level, "请求",
				"id", requestID(r.Context()), "method", r.Method, "path", r.URL.Path,
				"status", status, "ms", elapsed.Milliseconds(), "bytes", rec.bytes)
		}
	})
}
