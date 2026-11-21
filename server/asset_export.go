package server

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lin9527ya/blade/db"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Generate privately first. No successful status or partial JSON/CSV is sent
// until every query/scan/write succeeded and the MVCC transaction is closed.
func (s *Server) exportAssets(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	filter, err := assetFilterFromQuery(r)
	if err != nil {
		writeErr(w, 400, err.Error())
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
	servePreparedAssetExport(w, r, format, func(output io.Writer) error {
		return pg.ReadSnapshot(r.Context(), func(view *db.DB) error {
			names := map[int64]string{}
			rows, err := view.QueryContext(r.Context(), `SELECT id,name FROM companies`)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id int64
				var name string
				if err := rows.Scan(&id, &name); err != nil {
					rows.Close()
					return err
				}
				names[id] = name
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			return writeAssetExportRows(output, format, names, func(consume func(*db.Asset) error) error {
				return view.Assets().IterateFiltered(r.Context(), filter, consume)
			})
		})
	})
}

func servePreparedAssetExport(w http.ResponseWriter, r *http.Request, format string, generate func(io.Writer) error) {
	file, err := os.CreateTemp("", "blade-assets-export-*")
	if err != nil {
		writeErr(w, 500, "无法创建临时导出文件")
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := generate(file); err != nil {
		log.Printf("[assets] 导出生成失败，未发送下载响应: %v", err)
		if r.Context().Err() == nil {
			writeErr(w, 500, "资产导出未完成，请重试或查看服务端日志")
		}
		return
	}
	if err := r.Context().Err(); err != nil {
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeErr(w, 500, "导出文件读取失败")
		return
	}
	filename := fmt.Sprintf("assets-%s.%s", time.Now().Format("2006-01-02"), format)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	}
	// Dynamic snapshots must not use second-resolution If-Modified-Since caching.
	http.ServeContent(w, r, filename, time.Time{}, file)
}

func writeAssetExportRows(output io.Writer, format string, names map[int64]string, iterate func(func(*db.Asset) error) error) error {
	csvWriter := csv.NewWriter(output)
	encoder := json.NewEncoder(output)
	first := true
	if format == "csv" {
		if _, err := io.WriteString(output, "\ufeff"); err != nil {
			return err
		}
		if err := csvWriter.Write([]string{"host", "ip", "url", "type", "title", "company", "company_id", "status", "provider", "first_seen", "last_seen"}); err != nil {
			return err
		}
	} else {
		if _, err := io.WriteString(output, "["); err != nil {
			return err
		}
	}
	err := iterate(func(a *db.Asset) error {
		if format == "json" {
			if !first {
				if _, err := io.WriteString(output, ","); err != nil {
					return err
				}
			}
			first = false
			return encoder.Encode(a)
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
		companyID, company := "", ""
		if a.CompanyID != nil {
			companyID = fmt.Sprint(*a.CompanyID)
			company = names[*a.CompanyID]
		}
		return csvWriter.Write(csvSafeRow([]string{host, ip, a.URL, a.Type, title, company, companyID, a.Status, a.Provider, a.FirstSeen, a.LastSeen}))
	})
	if err != nil {
		return err
	}
	if format == "json" {
		_, err = io.WriteString(output, "]")
		return err
	}
	csvWriter.Flush()
	return csvWriter.Error()
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
