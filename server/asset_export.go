package server

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const assetExportPageSize = 1000

func (s *Server) companyNameMap() map[int64]string {
	out := map[int64]string{}
	pg := s.m.PG()
	if pg == nil {
		return out
	}
	rows, err := pg.Query(`SELECT id, name FROM companies`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if rows.Scan(&id, &name) == nil {
			out[id] = name
		}
	}
	return out
}

// exportAssets streams the current filter without the old frontend 5000-row cap.
// CSV keeps the import-compatible leading columns; lifecycle fields are appended.
func (s *Server) exportAssets(w http.ResponseWriter, r *http.Request) {
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	filter, err := assetFilterFromQuery(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	total, err := as.CountFiltered(filter)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		writeErr(w, 400, "format 必须是 csv 或 json")
		return
	}
	companyNames := s.companyNameMap()
	filename := fmt.Sprintf("assets-%s.%s", time.Now().Format("2006-01-02"), format)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)

	cw := csv.NewWriter(w)
	if format == "csv" {
		_, _ = w.Write([]byte("\ufeff"))
		_ = cw.Write([]string{"host", "ip", "url", "type", "title", "company", "company_id", "status", "provider", "first_seen", "last_seen"})
	} else {
		_, _ = w.Write([]byte("["))
	}
	first := true
	offset := 0
	for offset < total {
		page, err := as.QueryFiltered(filter, assetExportPageSize, offset)
		if err != nil {
			if format == "json" && !first {
				_, _ = w.Write([]byte("]"))
			}
			return
		}
		if len(page) == 0 {
			break
		}
		for _, a := range page {
			if format == "json" {
				if !first {
					_, _ = w.Write([]byte(","))
				}
				first = false
				body, _ := json.Marshal(a)
				_, _ = w.Write(body)
				continue
			}
			host := a.Domain
			if host == "" {
				host = a.RootDomain
			}
			ip := a.IP
			if ip == "" {
				ip = a.CSegment
			}
			title := a.PageTitle
			if title == "" {
				title = a.AppName
			}
			if title == "" {
				title = a.ServiceName
			}
			companyID := ""
			company := ""
			if a.CompanyID != nil {
				companyID = fmt.Sprint(*a.CompanyID)
				company = companyNames[*a.CompanyID]
			}
			if err := cw.Write(csvSafeRow([]string{host, ip, a.URL, a.Type, title, company, companyID, a.Status, a.Provider, a.FirstSeen, a.LastSeen})); err != nil {
				return
			}
		}
		offset += len(page)
	}
	if format == "json" {
		_, _ = w.Write([]byte("]"))
		return
	}
	cw.Flush()
}

func (s *Server) updateAssetLifecycle(w http.ResponseWriter, r *http.Request) {
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad asset id")
		return
	}
	var req struct {
		Status   *string `json:"status"`
		Provider *string `json:"provider"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	status, provider := "", ""
	if req.Status != nil {
		status = strings.TrimSpace(*req.Status)
		if status != "active" && status != "stale" && status != "archived" {
			writeErr(w, 400, "status 必须是 active/stale/archived")
			return
		}
	}
	if req.Provider != nil {
		provider = strings.TrimSpace(*req.Provider)
		if len(provider) > 64 {
			writeErr(w, 400, "provider 最长 64 字符")
			return
		}
	}
	if status == "" && provider == "" {
		writeErr(w, 400, "status 或 provider 至少提供一个")
		return
	}
	if err := as.UpdateAssetLifecycle(id, status, provider); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, 404, "asset not found")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
