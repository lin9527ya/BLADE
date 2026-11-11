package server

import (
	"encoding/csv"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 全局战果/凭据台账：只读、跨任务。凭据明文永远不经过这里；需要时仍走受控
// reveal 接口并留痕。筛选条件对列表与导出共用一份解析。

func lootFilterFromQuery(r *http.Request) db.LootFilter {
	q := r.URL.Query()
	return db.LootFilter{
		Kind:       strings.TrimSpace(q.Get("kind")),
		State:      strings.TrimSpace(q.Get("state")),
		TaskID:     parseIntDefault(q.Get("task")),
		CompanyID:  parseIntDefault(q.Get("company")),
		CampaignID: parseIntDefault(q.Get("campaign")),
	}
}

func (s *Server) listLoot(w http.ResponseWriter, r *http.Request) {
	page := int(parseIntDefault(r.URL.Query().Get("page")))
	size := int(parseIntDefault(r.URL.Query().Get("size")))
	items, total, err := s.m.pg.ListLootLedger(r.Context(), lootFilterFromQuery(r), page, size)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	page, size = normalizePage(page, size, 50, 200)
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": page, "size": size})
}

func (s *Server) exportLoot(w http.ResponseWriter, r *http.Request) {
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		writeErr(w, 400, "bad format: "+format)
		return
	}
	filter := lootFilterFromQuery(r)
	name := `战果台账-` + time.Now().Format("20060102-150405") + "." + format
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte("["))
		first := true
		err := s.m.pg.IterateLootLedger(r.Context(), filter, func(it db.LootItem) error {
			b, err := json.Marshal(it)
			if err != nil {
				return err
			}
			if !first {
				_, _ = w.Write([]byte(","))
			}
			first = false
			_, err = w.Write(b)
			return err
		})
		if err != nil {
			log.Printf("[loot] export json: %v", err)
		}
		_, _ = w.Write([]byte("]"))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{
		"kind", "state", "task_id", "task_name", "campaign_id", "summary", "host", "locator", "url",
		"category", "exposure", "username", "fingerprint", "access", "privilege", "protocol", "port",
		"where", "signals", "cleanup", "created_at",
	})
	err := s.m.pg.IterateLootLedger(r.Context(), filter, func(it db.LootItem) error {
		return cw.Write([]string{
			it.Kind, it.State, strconv.FormatInt(it.TaskID, 10), it.TaskName, strconv.FormatInt(it.CampaignID, 10),
			it.Summary, it.Host, it.Locator, it.URL, it.Category, it.Exposure, it.Username, it.Fingerprint,
			it.Access, it.Privilege, it.Protocol, strconv.Itoa(it.Port), it.Where, it.Signals, it.Cleanup,
			it.CreatedAt.Format(time.RFC3339),
		})
	})
	if err != nil {
		log.Printf("[loot] export csv: %v", err)
	}
}

func credentialAttemptFilterFromQuery(r *http.Request) db.CredentialAttemptFilter {
	q := r.URL.Query()
	return db.CredentialAttemptFilter{
		TaskID:  parseIntDefault(q.Get("task")),
		Host:    strings.TrimSpace(q.Get("host")),
		Account: strings.TrimSpace(q.Get("account")),
	}
}

func (s *Server) listCredentialAttempts(w http.ResponseWriter, r *http.Request) {
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	filter := credentialAttemptFilterFromQuery(r)
	if format == "csv" || format == "json" {
		name := `凭据尝试-` + time.Now().Format("20060102-150405") + "." + format
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
		if format == "json" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte("["))
			first := true
			err := s.m.pg.IterateCredentialAttempts(r.Context(), filter, func(a db.CredentialAttempt) error {
				b, err := json.Marshal(a)
				if err != nil {
					return err
				}
				if !first {
					_, _ = w.Write([]byte(","))
				}
				first = false
				_, err = w.Write(b)
				return err
			})
			if err != nil {
				log.Printf("[loot] credential attempts json: %v", err)
			}
			_, _ = w.Write([]byte("]"))
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		cw := csv.NewWriter(w)
		defer cw.Flush()
		_ = cw.Write([]string{"id", "task_id", "host", "account", "tool", "command", "spray", "at"})
		err := s.m.pg.IterateCredentialAttempts(r.Context(), filter, func(a db.CredentialAttempt) error {
			return cw.Write([]string{
				strconv.FormatInt(a.ID, 10), strconv.FormatInt(a.TaskID, 10), a.Host, a.Account, a.Tool,
				a.Command, strconv.FormatBool(a.Spray), a.At.Format(time.RFC3339),
			})
		})
		if err != nil {
			log.Printf("[loot] credential attempts csv: %v", err)
		}
		return
	}
	page := int(parseIntDefault(r.URL.Query().Get("page")))
	size := int(parseIntDefault(r.URL.Query().Get("size")))
	items, total, err := s.m.pg.ListCredentialAttemptsPage(r.Context(), filter, page, size)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	page, size = normalizePage(page, size, 50, 200)
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": page, "size": size})
}

func normalizePage(page, size, def, max int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = def
	}
	if size > max {
		size = max
	}
	return page, size
}
