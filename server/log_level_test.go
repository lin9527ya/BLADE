package server

import (
	"io"
	"testing"
)

func TestLogLevelFiltersBeforeRing(t *testing.T) {
	sink := &logSinkT{cap: 10, subs: map[chan LogLine]struct{}{}, out: io.Discard}
	old := logThreshold.Load()
	t.Cleanup(func() { logThreshold.Store(old) })

	SetLogLevel("info")
	sink.add(LogLine{Level: "debug", Text: "verbose"})
	sink.add(LogLine{Level: "info", Text: "normal"})
	if len(sink.ring) != 1 || sink.ring[0].Text != "normal" {
		t.Fatalf("ring = %+v, want only the info line", sink.ring)
	}

	SetLogLevel("debug")
	sink.add(LogLine{Level: "debug", Text: "verbose-again"})
	if len(sink.ring) != 2 {
		t.Fatalf("hot switch to debug did not take effect: %+v", sink.ring)
	}

	SetLogLevel("error")
	sink.add(LogLine{Level: "warn", Text: "hidden"})
	if len(sink.ring) != 2 {
		t.Fatalf("warn line should be filtered at error threshold: %+v", sink.ring)
	}
}

func TestParseLogPrefersExplicitLevelPrefix(t *testing.T) {
	line := parseLog("2026/10/29 10:00:00 [DEBUG] [exec] 探针失败但继续")
	if line.Level != "debug" {
		t.Fatalf("level = %q, want debug (keyword must not override prefix)", line.Level)
	}
	if line.Tag != "exec" {
		t.Fatalf("tag = %q, want exec", line.Tag)
	}
	if line.Text != "[exec] 探针失败但继续" {
		t.Fatalf("text = %q", line.Text)
	}
	fallback := parseLog("2026/10/29 10:00:00 [pg] 连接失败")
	if fallback.Level != "error" {
		t.Fatalf("keyword fallback = %q, want error", fallback.Level)
	}
}

func TestSetLogLevelRejectsUnknown(t *testing.T) {
	if SetLogLevel("trace") {
		t.Fatal("unknown level must be rejected")
	}
}
