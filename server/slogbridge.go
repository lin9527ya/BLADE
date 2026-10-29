package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// slog 桥：把 slog 接到既有的日志管道上。
//
// 为什么需要它：日志页（环形缓冲 + SSE + server_logs）认的是 **log 包**的输出——
// logsink 靠 log.SetOutput 接管。slog 的默认 handler 直接写 stderr，绕过这条管道，
// 于是新写的日志在界面上会凭空消失。所以这里给 slog 装一个 handler，把记录交给
// logSink.emit：**级别与 tag 都由调用点给定**，不再走 parseLog 的关键词猜测。
//
// 为什么要迁 slog：现有级别是猜出来的——parseLog 见「失败」判 error、见「重试」判
// warn。于是「清理完成：删除 N 项…失败 0 项」这种**成功**消息被标成红色 error，而一条
// 500 的访问日志因为不含任何关键词被判成 info。slog 让级别由调用点写明。
//
// 迁移是渐进的：log.Printf 照旧工作（继续走关键词兜底），新代码用 slogFor(tag)。

// slogTagKey 是承载 tag 的属性名。tag 决定日志页上的分类（pg / planner / exec …）。
const slogTagKey = "tag"

// appLogger 是本桥专用的 logger。
//
// **刻意不用 slog.SetDefault**：Go 的 SetDefault 在 handler 不是默认类型时会顺手执行
// `log.SetOutput(&handleWriter{l})`，把整个 log 包的输出劫持到 slog——那样一来
// log.Printf 的 `[engine]` / `[pg]` 等真 tag 会被本桥的默认 tag 覆盖，parseLog 的
// tag 提取也被绕过（实测踩到过：所有 log.Printf 行都变成了 `[app] [engine] …`）。
// 两条路各走各的：log.Printf 照旧经 logsink，显式 slog 走这里。
var appLogger = slog.New(&logSinkHandler{min: slog.LevelInfo})

// slogFor 返回带 tag 的 logger，渲染成既有的 `[tag] 消息 k=v` 形状。
func slogFor(tag string) *slog.Logger {
	return appLogger.With(slogTagKey, tag)
}

// logSinkHandler 把 slog 记录渲染成一行文本交给 logSink。
type logSinkHandler struct {
	min   slog.Level
	attrs []slog.Attr
}

func (h *logSinkHandler) Enabled(_ context.Context, l slog.Level) bool {
	threshold := logThreshold.Load()
	// Map the record level onto the same scale as the ring threshold so the
	// settings hot switch applies to both paths.
	var mapped int32
	switch {
	case l >= slog.LevelError:
		mapped = int32(logLevelError)
	case l >= slog.LevelWarn:
		mapped = int32(logLevelWarn)
	case l >= slog.LevelInfo:
		mapped = int32(logLevelInfo)
	default:
		mapped = int32(logLevelDebug)
	}
	return mapped >= threshold
}

func (h *logSinkHandler) Handle(_ context.Context, r slog.Record) error {
	tag := "app"
	var b strings.Builder
	collect := func(a slog.Attr) bool {
		if a.Key == slogTagKey {
			tag = a.Value.String()
			return true
		}
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString("=")
		b.WriteString(slogValueString(a.Value))
		return true
	}
	for _, a := range h.attrs {
		collect(a)
	}
	r.Attrs(collect)

	logSink.emit(slogLevelName(r.Level), tag, "["+tag+"] "+r.Message+b.String())
	return nil
}

func (h *logSinkHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &logSinkHandler{min: h.min, attrs: make([]slog.Attr, 0, len(h.attrs)+len(attrs))}
	next.attrs = append(next.attrs, h.attrs...)
	next.attrs = append(next.attrs, attrs...)
	return next
}

// WithGroup 不保留分组：本桥把属性平铺成 k=v，分组只会让键名变长而无处体现。
func (h *logSinkHandler) WithGroup(string) slog.Handler { return h }

func slogLevelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	default:
		return "info"
	}
}

// slogValueString 把属性值渲染成一行里可读的形状：含空白或为空的字符串加引号，
// 免得 `summary=两个词` 读不出到底是一个字段还是两个。
func slogValueString(v slog.Value) string {
	v = v.Resolve()
	if v.Kind() == slog.KindString {
		s := v.String()
		if s == "" || strings.ContainsAny(s, " \t\n\"") {
			return fmt.Sprintf("%q", s)
		}
		return s
	}
	return fmt.Sprint(v.Any())
}
