package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// 完整报告默认仍是内联 Markdown；format=md/docx 才走附件下载。
func TestReportDownloadFormats(t *testing.T) {
	s, f, request := trafficEvidenceServer(t)
	full, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || full.TaskID == nil {
		t.Fatalf("fixture finding: %+v %v", full, err)
	}
	taskID := strconv.FormatInt(*full.TaskID, 10)
	base := "/api/report?task=" + taskID

	w := request(http.MethodGet, base, "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Disposition") != "" {
		t.Fatalf("default report = %d cd=%q", w.Code, w.Header().Get("Content-Disposition"))
	}

	w = request(http.MethodGet, base+"&format=md", "")
	if w.Code != http.StatusOK {
		t.Fatalf("md report = %d %s", w.Code, w.Body)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".md") {
		t.Fatalf("md Content-Disposition = %q", cd)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("md Content-Type = %q", w.Header().Get("Content-Type"))
	}

	w = request(http.MethodGet, base+"&format=docx", "")
	if w.Code != http.StatusOK {
		t.Fatalf("docx report = %d %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "wordprocessingml") {
		t.Fatalf("docx Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".docx") {
		t.Fatalf("docx Content-Disposition = %q", cd)
	}
	if body := w.Body.Bytes(); len(body) < 2 || body[0] != 'P' || body[1] != 'K' {
		t.Fatalf("docx body is not a zip (len=%d)", len(body))
	}

	w = request(http.MethodGet, base+"&format=bogus", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bogus format = %d %s", w.Code, w.Body)
	}
}
