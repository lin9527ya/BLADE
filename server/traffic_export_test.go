package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/traffic"
)

func TestTrafficCurlAndHARMaskSensitiveHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "https://example.test/api?q=1", strings.NewReader("a=1"))
	req.Header.Set("Cookie", "sid=secret")
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("X-Test", "ok")
	entry := &trafficExportEntry{
		meta:     &traffic.ExchangeMeta{URL: "https://example.test/api?q=1", Method: "POST", Status: 201},
		req:      req,
		reqBody:  []byte("a=1"),
		resp:     nil,
		respBody: []byte(`{"ok":true}`),
	}

	curl := trafficCurl(entry, true)
	for _, want := range []string{"curl -X 'POST'", "Cookie: REDACTED", "Authorization: REDACTED", "X-Test: ok", "--data-raw 'a=1'"} {
		if !strings.Contains(curl, want) {
			t.Fatalf("curl missing %q: %s", want, curl)
		}
	}
	if strings.Contains(curl, "sid=secret") || strings.Contains(curl, "Bearer token") {
		t.Fatalf("curl leaked secrets: %s", curl)
	}
	raw := trafficCurl(entry, false)
	if !strings.Contains(raw, "sid=secret") {
		t.Fatalf("unmasked curl should preserve cookie: %s", raw)
	}

	har := trafficHAR([]*trafficExportEntry{entry}, true)
	log, _ := har["log"].(map[string]any)
	entries, _ := log["entries"].([]harEntry)
	if len(entries) != 1 {
		t.Fatalf("har entries = %+v", log)
	}
	var sawMasked bool
	for _, h := range entries[0].Request.Headers {
		if h.Name == "Cookie" && h.Value == trafficExportMask {
			sawMasked = true
		}
	}
	if !sawMasked {
		t.Fatalf("HAR did not mask cookie: %+v", entries[0].Request.Headers)
	}
}
