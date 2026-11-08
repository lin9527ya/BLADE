package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/transcript"
)

func TestCacheDiagnosticPrivacyWindowAndCopies(t *testing.T) {
	t.Setenv(CacheTraceEnv, "")
	SetCacheTraceWindow(0)
	t.Cleanup(func() { SetCacheTraceWindow(0) })
	if CacheTraceState().Enabled {
		t.Fatal("capture defaults on")
	}
	SetCacheTraceWindow(time.Minute)
	req, _ := http.NewRequest("POST", "https://example.invalid/SECRET_URL", strings.NewReader(`{"model":"m","system":"SECRET_SYSTEM","tools":[],"messages":[{"role":"user","content":"SECRET_TARGET"}]}`))
	req = req.WithContext(transcript.WithSessionID(context.Background(), "exp7-planner"))
	id := traceModelRequest(req)
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":20},\"content\":\"SECRET_RESPONSE\"}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"cache_read_input_tokens\":80,\"output_tokens\":8,\"credential\":\"SECRET_KEY\"}}\n\n"))}
	wrapCacheTraceResponse(req, resp, id)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	rows := CacheDiagnosticSnapshot("7", "", 100)
	if len(rows) != 1 || !rows[0].Complete || len(rows[0].Frames) != 2 || rows[0].Status != 200 {
		t.Fatalf("capture=%+v", rows)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "SECRET_") {
		t.Fatal("diagnostic retained content/header/url")
	}
	cacheTraceHistory.Lock()
	for key := range cacheTraceHistory.last {
		if strings.Contains(key, "SECRET_") {
			t.Error("history key retained private endpoint")
		}
	}
	cacheTraceHistory.Unlock()
	rows[0].Frames[0].Values["input_tokens"] = 999
	again := CacheDiagnosticSnapshot("7", "", 100)
	if again[0].Frames[0].Values["input_tokens"] == 999 {
		t.Fatal("snapshot aliases shared state")
	}
	if len(CacheDiagnosticSnapshot("70", "", 100)) != 0 {
		t.Fatal("task filter matched a prefix collision")
	}
	SetCacheTraceWindow(0)
	if CacheTraceState().Enabled || len(CacheDiagnosticSnapshot("", "", 100)) != 0 {
		t.Fatal("disable did not clear")
	}
}

func TestCacheDiagnosticBoundsAndExpiry(t *testing.T) {
	t.Setenv(CacheTraceEnv, "")
	SetCacheTraceWindow(time.Minute)
	t.Cleanup(func() { SetCacheTraceWindow(0) })
	for i := 0; i < cacheDiagnosticLimit+20; i++ {
		addCacheDiagnostic(CacheDiagnostic{ID: uint64(i + 1), At: time.Now(), Session: "exp1-planner"})
	}
	cacheDiagnostics.Lock()
	count := len(cacheDiagnostics.rows)
	cacheDiagnostics.Unlock()
	if count != cacheDiagnosticLimit {
		t.Fatalf("stored %d rows", count)
	}
	if len(CacheDiagnosticSnapshot("", "", 100)) != 100 {
		t.Fatal("snapshot not bounded")
	}
	cacheDiagnosticUntil.Store(time.Now().Add(-time.Second).UnixNano())
	if CacheTraceState().Enabled {
		t.Fatal("expired capture remains enabled")
	}
}

func TestCacheSignatureOtherProtocols(t *testing.T) {
	a, err := requestCacheSignature(`{"model":"m","messages":[{"role":"system","content":"system"},{"role":"user","content":"one"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := requestCacheSignature(`{"model":"m","messages":[{"role":"system","content":"changed"},{"role":"user","content":"one"}]}`)
	if a.System == b.System || len(a.Messages) != 1 {
		t.Fatal("chat system not isolated")
	}
	c, _ := requestCacheSignature(`{"model":"m","instructions":"system","input":[{"role":"user","content":"one"}]}`)
	d, _ := requestCacheSignature(`{"model":"m","instructions":"changed","input":[{"role":"user","content":"one"}]}`)
	if c.System == d.System || len(c.Messages) != 1 {
		t.Fatal("responses instructions not isolated")
	}
}
