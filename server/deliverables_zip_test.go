package server

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestDeliverableZIPIncludesManifestAndSums(t *testing.T) {
	version := &db.DeliverableVersion{
		ID: 7, DeliverableID: 3, Version: 2, ReportMD: "# Report\n\nhello\n",
		SHA256: "abc123", CreatedBy: "admin", CreatedAt: time.Unix(1700000000, 0).UTC(),
	}
	data, err := deliverableZIP("deliverable-3-v2", version)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(body)
	}
	for _, name := range []string{"deliverable-3-v2.md", "manifest.json", "SHA256SUMS"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("missing %s in %v", name, files)
		}
	}
	if !strings.Contains(files["manifest.json"], `"report_sha256": "abc123"`) {
		t.Fatalf("manifest = %s", files["manifest.json"])
	}
	if !strings.Contains(files["SHA256SUMS"], "deliverable-3-v2.md") || !strings.Contains(files["SHA256SUMS"], "manifest.json") {
		t.Fatalf("sums = %s", files["SHA256SUMS"])
	}
}
