package server

import (
	"log"
	"strings"
	"testing"
)

// 桥必须让 slog 的记录**进日志环**，且级别与 tag 由调用点给定——这正是迁 slog 的
// 理由：log.Printf 那条路要靠 parseLog 猜级别，猜错过（成功消息判成 error、
// 500 判成 info）。
func TestSlogBridgeLandsInRing(t *testing.T) {
	newLines := installSlogBridge(t)

	slogFor("storage").Info("清理完成", "deleted", 3, "failed", 0)
	slogFor("worker").Warn("稍后重试", "attempt", 2)
	slogFor("exec").Error("落库失败", "task", 42)

	lines := newLines()
	if len(lines) != 3 {
		t.Fatalf("应记 3 条，得到 %d 条：%v", len(lines), lines)
	}

	want := []struct{ level, tag, text string }{
		{"info", "storage", "[storage] 清理完成 deleted=3 failed=0"},
		{"warn", "worker", "[worker] 稍后重试 attempt=2"},
		{"error", "exec", "[exec] 落库失败 task=42"},
	}
	for i, w := range want {
		if lines[i].Level != w.level || lines[i].Tag != w.tag || lines[i].Text != w.text {
			t.Errorf("第 %d 条：得到 level=%q tag=%q text=%q，want level=%q tag=%q text=%q",
				i, lines[i].Level, lines[i].Tag, lines[i].Text, w.level, w.tag, w.text)
		}
	}
}

// 关键回归：这条在旧的关键词启发式下会被判成 error（文案里有「失败」），实际是成功。
func TestSlogBridgeSuccessNotMarkedError(t *testing.T) {
	newLines := installSlogBridge(t)

	slogFor("storage").Info("清理完成", "deleted", 5, "failed", 0)

	lines := newLines()
	if len(lines) != 1 {
		t.Fatalf("应记 1 条，得到 %d 条", len(lines))
	}
	if lines[0].Level != "info" {
		t.Fatalf("成功汇总该是 info，得到 %q（旧启发式会因文案含「失败」判成 error）", lines[0].Level)
	}
}

// 含空白或为空的字符串要加引号，否则 `summary=两个词` 读不出是一个字段还是两个。
func TestSlogBridgeQuotesAmbiguousStrings(t *testing.T) {
	newLines := installSlogBridge(t)

	slogFor("x").Info("m", "spaced", "两个 词", "empty", "", "plain", "ok")

	lines := newLines()
	if len(lines) != 1 {
		t.Fatalf("应记 1 条，得到 %d 条", len(lines))
	}
	text := lines[0].Text
	if !strings.Contains(text, `spaced="两个 词"`) {
		t.Errorf("含空白的值该加引号，得到 %q", text)
	}
	if !strings.Contains(text, `empty=""`) {
		t.Errorf("空值该加引号，得到 %q", text)
	}
	if !strings.Contains(text, "plain=ok") {
		t.Errorf("普通值不该加引号，得到 %q", text)
	}
}

// 低于 min 级别的不该进环（桥默认 LevelInfo，Debug 丢弃）。
func TestSlogBridgeDropsBelowMin(t *testing.T) {
	newLines := installSlogBridge(t)

	slogFor("x").Debug("调试细节")
	if lines := newLines(); len(lines) != 0 {
		t.Fatalf("Debug 不该进日志环，得到 %v", lines)
	}
}

// 回归：装桥**不得**劫持 log 包。
//
// Go 的 slog.SetDefault 在 handler 不是默认类型时会顺手执行 log.SetOutput(&handleWriter{l})，
// 把整个 log 包的输出接进 slog。那样 log.Printf 的 `[engine]` / `[pg]` 等真 tag 会被桥的
// 默认 tag 覆盖，parseLog 的 tag 提取也被绕过——实测踩到过：所有 log.Printf 行都变成了
// `[app] [engine] …`。所以桥必须用自有的 appLogger，绝不碰 slog.SetDefault。
func TestSlogBridgeDoesNotHijackLogPackage(t *testing.T) {
	before := log.Writer()
	newLines := installSlogBridge(t)

	if log.Writer() != before {
		t.Fatal("装桥改了 log 包的输出目标：log.Printf 的真 tag 会被桥的默认 tag 覆盖")
	}

	log.Printf("[engine] 这行必须保留自己的 tag")
	for _, l := range newLines() {
		if l.Tag == "app" {
			t.Fatalf("log.Printf 的输出不该经过桥，得到 %v", l)
		}
	}
}
