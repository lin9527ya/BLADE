package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONETagAnd304(t *testing.T) {
	payload := map[string]any{"items": []string{"a", "b"}, "total": 2}
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	writeJSONETag(rec, req, http.StatusOK, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("If-None-Match", "W/"+etag)
	rec = httptest.NewRecorder()
	writeJSONETag(rec, req, http.StatusOK, payload)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 body = %q", rec.Body.String())
	}
}

func TestETagMatchesList(t *testing.T) {
	if !etagMatches(`"a", W/"b"`, `"b"`) {
		t.Fatal("weak/list match failed")
	}
	if !etagMatches("*", `"a"`) {
		t.Fatal("* match failed")
	}
	if etagMatches(`"a"`, `"b"`) {
		t.Fatal("unexpected match")
	}
}
