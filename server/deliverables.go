package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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
	default:
		writeErr(w, 400, "format 必须是 md 或 docx")
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
