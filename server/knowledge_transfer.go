package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

func (s *Server) exportAllCVEs() ([]*db.CVE, error) {
	as := s.m.PG().Assets()
	out := []*db.CVE{}
	for offset := 0; offset < 10000; {
		page, _, err := as.ListCVEs(db.CVEFilter{Status: "all", Limit: 200, Offset: offset})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < 200 {
			break
		}
		offset += len(page)
	}
	return out, nil
}

func (s *Server) exportAllPlaybooks() ([]*db.Playbook, error) {
	as := s.m.PG().Assets()
	out := []*db.Playbook{}
	for offset := 0; offset < 10000; {
		page, _, err := as.ListPlaybooks(db.PlaybookFilter{Status: "all", Limit: 200, Offset: offset})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < 200 {
			break
		}
		offset += len(page)
	}
	return out, nil
}

func (s *Server) exportCVEs(w http.ResponseWriter, r *http.Request) {
	items, err := s.exportAllCVEs()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "cves-"+time.Now().Format("20060102")+".json"))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{"version": 1, "items": items})
}

func (s *Server) importCVEs(w http.ResponseWriter, r *http.Request) {
	as := s.m.PG().Assets()
	var req struct {
		Items []db.CVE `json:"items"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if len(req.Items) > 500 {
		writeErr(w, 400, "单次最多导入 500 条 CVE")
		return
	}
	created, updated := 0, 0
	var failures []string
	for _, item := range req.Items {
		item.CVEID = db.NormalizeCVEID(item.CVEID)
		existing, err := as.GetCVEByCVEID(item.CVEID)
		if err != nil {
			failures = append(failures, item.CVEID+": "+err.Error())
			continue
		}
		if existing != nil {
			item.Status = existing.Status
			if _, err := as.UpdateCVE(existing.ID, item); err != nil {
				failures = append(failures, item.CVEID+": "+err.Error())
			} else {
				updated++
			}
			continue
		}
		if _, err := as.CreateCVE(item); err != nil {
			failures = append(failures, item.CVEID+": "+err.Error())
		} else {
			created++
		}
	}
	writeJSON(w, 200, map[string]any{"created": created, "updated": updated, "failed": len(failures), "errors": failures})
}

func (s *Server) exportPlaybooks(w http.ResponseWriter, r *http.Request) {
	items, err := s.exportAllPlaybooks()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "playbooks-"+time.Now().Format("20060102")+".json"))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{"version": 1, "items": items})
}

func (s *Server) importPlaybooks(w http.ResponseWriter, r *http.Request) {
	as := s.m.PG().Assets()
	var req struct {
		Items []db.Playbook `json:"items"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if len(req.Items) > 500 {
		writeErr(w, 400, "单次最多导入 500 张打法卡")
		return
	}
	created, updated := 0, 0
	var failures []string
	for _, item := range req.Items {
		item.Status = ""
		out, inserted, err := as.UpsertPlaybook(item)
		if err != nil {
			failures = append(failures, strings.TrimSpace(item.Title)+": "+err.Error())
			continue
		}
		if inserted {
			created++
		} else if out != nil {
			updated++
		}
	}
	writeJSON(w, 200, map[string]any{"created": created, "updated": updated, "failed": len(failures), "errors": failures})
}
