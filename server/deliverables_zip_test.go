package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestExportScopeDeliverablesZIP(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	companyID, _, err := m.PG().Companies().UpsertCompany("scope-zip-company", "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.PG().Companies().DeleteCompany(companyID) //nolint:errcheck
	campaign, err := m.PG().CreateCampaign("scope-zip-campaign", "", &companyID)
	if err != nil {
		t.Fatal(err)
	}
	defer m.PG().DeleteCampaign(campaign.ID) //nolint:errcheck
	detail, err := m.PG().CreateDeliverable(db.DeliverableInput{
		Name: "scope zip deliverable", Kind: "pentest", CampaignID: &campaign.ID, CompanyID: &companyID,
		ReportMD: "# Scope ZIP\n\nhello\n", Note: "v1", CreatedBy: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.PG().DeleteDeliverable(detail.Deliverable.ID) //nolint:errcheck

	s := &Server{m: m, engine: &Engine{}}
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/deliverables/export.zip?campaign_id=%d", campaign.ID), nil)
	rec := httptest.NewRecorder()
	s.exportScopeDeliverables(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["manifest.json"] || !names["SHA256SUMS"] {
		t.Fatalf("zip files = %v", names)
	}
	foundReport := false
	for name := range names {
		if strings.HasPrefix(name, "deliverable-") && strings.HasSuffix(name, ".md") {
			foundReport = true
		}
	}
	if !foundReport {
		t.Fatalf("report file missing: %v", names)
	}
}
