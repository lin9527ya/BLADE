package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/report"
)

func (s *Server) deliverableStore() bool {
	return s.m != nil && s.m.PG() != nil
}

func (s *Server) listDeliverables(w http.ResponseWriter, r *http.Request) {
	if !s.deliverableStore() {
		writeErr(w, 503, "database unavailable")
		return
	}
	items, err := s.m.PG().ListDeliverables()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) createDeliverable(w http.ResponseWriter, r *http.Request) {
	if !s.deliverableStore() {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req struct {
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		TaskID     *int64 `json:"task_id"`
		CompanyID  *int64 `json:"company_id"`
		CampaignID *int64 `json:"campaign_id"`
		ReportMD   string `json:"report_md"`
		Note       string `json:"note"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	detail, err := s.m.PG().CreateDeliverable(db.DeliverableInput{
		Name: req.Name, Kind: req.Kind, TaskID: req.TaskID, CompanyID: req.CompanyID,
		CampaignID: req.CampaignID, ReportMD: req.ReportMD, Note: req.Note, CreatedBy: "operator",
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, detail)
}

func (s *Server) getDeliverable(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad deliverable id")
		return
	}
	detail, err := s.m.PG().GetDeliverable(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if detail == nil {
		writeErr(w, 404, "deliverable not found")
		return
	}
	writeJSON(w, 200, detail)
}

func (s *Server) addDeliverableVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad deliverable id")
		return
	}
	var req struct {
		ReportMD string `json:"report_md"`
		Note     string `json:"note"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	version, err := s.m.PG().AddDeliverableVersion(id, req.ReportMD, req.Note, "operator")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, version)
}

func (s *Server) acceptDeliverable(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad deliverable id")
		return
	}
	var req struct {
		VersionID  int64  `json:"version_id"`
		AcceptedBy string `json:"accepted_by"`
		Note       string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.VersionID <= 0 {
		writeErr(w, 400, "version_id required")
		return
	}
	acceptance, err := s.m.PG().AcceptDeliverable(id, req.VersionID, req.AcceptedBy, req.Note)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, acceptance)
}

func (s *Server) deliverableDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad deliverable id")
		return
	}
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if from <= 0 || to <= 0 {
		writeErr(w, 400, "from/to version ids required")
		return
	}
	diff, err := s.m.PG().DeliverableDiff(id, from, to)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"diff": diff})
}

func (s *Server) exportDeliverableVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad deliverable id")
		return
	}
	versionID, ok := pathInt(r, "version_id")
	if !ok || versionID <= 0 {
		writeErr(w, 400, "bad version id")
		return
	}
	version, err := s.m.PG().GetDeliverableVersion(id, versionID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if version == nil {
		writeErr(w, 404, "deliverable version not found")
		return
	}
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	if format == "" {
		format = "md"
	}
	base := fmt.Sprintf("deliverable-%d-v%d", id, version.Version)
	switch format {
	case "md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", base+".md"))
		_, _ = w.Write([]byte(version.ReportMD))
	case "docx":
		data, err := report.FullDOCX(version.ReportMD)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", base+".docx"))
		_, _ = w.Write(data)
	case "zip":
		data, err := deliverableZIP(base, version)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", base+".zip"))
		_, _ = w.Write(data)
	default:
		writeErr(w, 400, "format 必须是 md、docx 或 zip")
	}
}

func (s *Server) deleteDeliverable(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad deliverable id")
		return
	}
	n, err := s.m.PG().DeleteDeliverable(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if n == 0 {
		writeErr(w, 404, "deliverable not found")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// deliverableZIP packages the version report with a manifest and SHA256SUMS so a
// customer can verify what they received. Evidence-attachment inclusion is left
// to a later batch; this keeps the first ZIP format stable and auditable.
func deliverableZIP(base string, version *db.DeliverableVersion) ([]byte, error) {
	reportName := base + ".md"
	reportBytes := []byte(version.ReportMD)
	reportSum := fmt.Sprintf("%x", sha256.Sum256(reportBytes))
	manifest := map[string]any{
		"format":        "blade-deliverable-v1",
		"deliverable":   version.DeliverableID,
		"version":       version.Version,
		"version_id":    version.ID,
		"created_at":    version.CreatedAt,
		"created_by":    version.CreatedBy,
		"report_sha256": version.SHA256,
		"files": []map[string]any{{
			"path": reportName, "size": len(reportBytes), "sha256": reportSum,
		}},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	manifestSum := fmt.Sprintf("%x", sha256.Sum256(manifestBytes))
	sums := fmt.Sprintf("%s  %s\n%s  manifest.json\n", reportSum, reportName, manifestSum)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, file := range []struct {
		name string
		data []byte
	}{{reportName, reportBytes}, {"manifest.json", manifestBytes}, {"SHA256SUMS", []byte(sums)}} {
		fw, err := zw.Create(file.name)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// exportScopeDeliverables packages the latest version of every deliverable in one
// campaign or company. The per-version ZIP keeps the same manifest/SHA256SUMS
// discipline; this scope ZIP is for handing a whole engagement to a customer.
func (s *Server) exportScopeDeliverables(w http.ResponseWriter, r *http.Request) {
	if !s.deliverableStore() {
		writeErr(w, 503, "database unavailable")
		return
	}
	campaignID, _ := strconv.ParseInt(r.URL.Query().Get("campaign_id"), 10, 64)
	companyID, _ := strconv.ParseInt(r.URL.Query().Get("company_id"), 10, 64)
	if (campaignID > 0) == (companyID > 0) {
		writeErr(w, 400, "campaign_id 或 company_id 必须且只能提供一个")
		return
	}
	all, err := s.m.PG().ListDeliverables()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type exportItem struct {
		d    *db.Deliverable
		file string
		data []byte
		sum  string
	}
	items := []exportItem{}
	for _, d := range all {
		if campaignID > 0 && (d.CampaignID == nil || *d.CampaignID != campaignID) {
			continue
		}
		if companyID > 0 && (d.CompanyID == nil || *d.CompanyID != companyID) {
			continue
		}
		if d.LatestVersion <= 0 {
			continue
		}
		versions, err := s.m.PG().ListDeliverableVersions(d.ID, true)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if len(versions) == 0 {
			continue
		}
		v := versions[0]
		data := []byte(v.ReportMD)
		items = append(items, exportItem{
			d: d, file: fmt.Sprintf("deliverable-%d-v%d.md", d.ID, v.Version), data: data,
			sum: fmt.Sprintf("%x", sha256.Sum256(data)),
		})
	}
	if len(items) == 0 {
		writeErr(w, 404, "该范围没有可导出的交付物版本")
		return
	}
	manifestItems := make([]map[string]any, 0, len(items))
	sumLines := make([]string, 0, len(items)+1)
	for _, item := range items {
		manifestItems = append(manifestItems, map[string]any{
			"id": item.d.ID, "name": item.d.Name, "kind": item.d.Kind, "status": item.d.Status,
			"version": item.d.LatestVersion, "acceptance_count": item.d.AcceptanceCount,
			"file": item.file, "sha256": item.sum,
		})
		sumLines = append(sumLines, fmt.Sprintf("%s  %s", item.sum, item.file))
	}
	scopeType, scopeID := "campaign", campaignID
	if companyID > 0 {
		scopeType, scopeID = "company", companyID
	}
	manifest := map[string]any{
		"format": "blade-deliverables-scope-v1", "scope_type": scopeType, "scope_id": scopeID,
		"generated_at": time.Now().UTC(), "deliverables": manifestItems,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	sumLines = append(sumLines, fmt.Sprintf("%x  manifest.json", sha256.Sum256(manifestBytes)))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, item := range items {
		fw, err := zw.Create(item.file)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if _, err := fw.Write(item.data); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"manifest.json", manifestBytes}, {"SHA256SUMS", []byte(strings.Join(sumLines, "\n") + "\n")}} {
		fw, err := zw.Create(file.name)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if _, err := fw.Write(file.data); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := zw.Close(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	filename := fmt.Sprintf("%s-%d-deliverables.zip", scopeType, scopeID)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	_, _ = w.Write(buf.Bytes())
}
