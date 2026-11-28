package server

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type packManifest struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	License string   `json:"license"`
	Sources []string `json:"sources"`
	Count   int      `json:"entry_count"`
	SHA     string   `json:"sha256"`
}
type packEntry struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Source string `json:"source"`
}
type companyPack struct {
	ID       int64           `json:"id"`
	Key      string          `json:"key"`
	Name     string          `json:"name"`
	Version  string          `json:"version"`
	License  string          `json:"license"`
	Sources  json.RawMessage `json:"sources"`
	Count    int             `json:"entry_count"`
	Status   string          `json:"status"`
	Reviewer string          `json:"reviewed_by,omitempty"`
	Reviewed *time.Time      `json:"reviewed_at,omitempty"`
}

func readKnowledgePack(data []byte) (m packManifest, entries []packEntry, err error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return m, nil, err
	}
	files := map[string][]byte{}
	if len(z.File) != 2 {
		return m, nil, errors.New("知识包需要 manifest.json 和 entries.jsonl")
	}
	for _, f := range z.File {
		limit := int64(16 << 20)
		switch f.Name {
		case "manifest.json":
			limit = 64 << 10
		case "entries.jsonl":
		default:
			return m, nil, errors.New("知识包文件名无效")
		}
		if _, dup := files[f.Name]; dup || f.Mode()&os.ModeType != 0 || f.UncompressedSize64 > uint64(limit) {
			return m, nil, errors.New("重复条目、文件类型或大小无效")
		}
		r, err := f.Open()
		if err != nil {
			return m, nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(r, limit+1))
		r.Close()
		if err != nil || int64(len(raw)) > limit {
			return m, nil, errors.New("文件损坏或解压超限")
		}
		if !utf8.Valid(raw) {
			return m, nil, errors.New("知识包文件必须为 UTF-8")
		}
		files[f.Name] = raw
	}
	if err = json.Unmarshal(files["manifest.json"], &m); err != nil {
		return m, nil, err
	}
	raw := files["entries.jsonl"]
	if !safeChatID.MatchString(m.ID) || len(m.ID) > 120 || strings.TrimSpace(m.Name) == "" || (strings.TrimSpace(m.Version) == "" || len(m.Version) > 128) || strings.TrimSpace(m.License) == "" || len(m.Sources) == 0 || m.Count < 1 || m.Count > 2000 {
		return m, nil, errors.New("manifest 的来源、许可证、版本或条数无效")
	}
	for _, s := range m.Sources {
		if strings.TrimSpace(s) == "" {
			return m, nil, errors.New("来源为空")
		}
	}
	if m.SHA != fmt.Sprintf("%x", sha256.Sum256(raw)) {
		return m, nil, errors.New("entries.jsonl 的 SHA-256 不匹配")
	}
	scan := bufio.NewScanner(bytes.NewReader(raw))
	scan.Buffer(make([]byte, 4096), 128<<10)
	seen := map[string]bool{}
	for scan.Scan() {
		var e packEntry
		if err = json.Unmarshal(scan.Bytes(), &e); err != nil || strings.TrimSpace(e.ID) == "" || len(e.ID) > 200 || seen[e.ID] || strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Body) == "" || strings.TrimSpace(e.Source) == "" {
			return m, nil, fmt.Errorf("第 %d 条知识缺字段、重复或格式无效", len(entries)+1)
		}
		seen[e.ID] = true
		entries = append(entries, e)
		if len(entries) > m.Count {
			return m, nil, errors.New("实际条数超出清单")
		}
	}
	if err = scan.Err(); err != nil {
		return m, nil, err
	}
	if len(entries) != m.Count {
		return m, nil, errors.New("清单条数不符")
	}
	return m, entries, nil
}
func (s *Server) companyPacksAPI(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	cid, ok := pathInt(r, "id")
	if !ok || cid < 1 {
		writeErr(w, 400, "无效企业")
		return
	}
	principal := principalFrom(r.Context())
	admin := principal.Role == "admin"
	base := r.PathValue("pack")
	if r.Method == http.MethodPost && !admin {
		writeErr(w, 403, "需要管理员权限")
		return
	}
	if r.Method == http.MethodPost && base == "" {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 12<<20))
		if err != nil {
			writeErr(w, 413, "知识包超过 12 MiB")
			return
		}
		m, entries, err := readKnowledgePack(raw)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		tx, err := pg.BeginTx(r.Context(), nil)
		if err != nil {
			writeErr(w, 500, "数据库不可用")
			return
		}
		defer tx.Rollback()
		sources, _ := json.Marshal(m.Sources)
		var id int64
		err = tx.QueryRow(`INSERT INTO company_knowledge_packs(company_id,pack_key,version,name,license,sources,sha256,entry_count)
 SELECT id,$2,$3,$4,$5,$6,$7,$8 FROM companies WHERE id=$1 ON CONFLICT DO NOTHING RETURNING id`, cid, m.ID, m.Version, m.Name, m.License, string(sources), m.SHA, m.Count).Scan(&id)
		if err == sql.ErrNoRows {
			var identical bool
			err = tx.QueryRow(`SELECT id,sha256=$4 AND license=$5 AND name=$6 AND sources=$7::jsonb FROM company_knowledge_packs WHERE company_id=$1 AND pack_key=$2 AND version=$3`, cid, m.ID, m.Version, m.SHA, m.License, m.Name, string(sources)).Scan(&id, &identical)
			if err == sql.ErrNoRows {
				writeErr(w, 404, "企业不存在")
				return
			}
			if err == nil && !identical {
				writeErr(w, 409, "同版本知识包内容或清单不同")
				return
			}
		} else if err == nil {
			items, _ := json.Marshal(entries)
			_, err = tx.Exec(`INSERT INTO company_knowledge_pack_entries(pack_id,entry_key,title,body,source) SELECT $1,id,title,body,source FROM jsonb_to_recordset($2::jsonb) AS x(id text,title text,body text,source text)`, id, string(items))
		}
		if err != nil {
			writeErr(w, 500, "知识包导入失败")
			return
		}
		if err = tx.Commit(); err != nil {
			writeErr(w, 500, "知识包提交失败")
			return
		}
		writeJSON(w, 200, map[string]int64{"id": id})
		return
	}
	if base == "" {
		rows, err := pg.QueryContext(r.Context(), `SELECT id,pack_key,name,version,license,sources,entry_count,status,reviewed_by,reviewed_at FROM company_knowledge_packs WHERE company_id=$1 AND ($2 OR status='approved') ORDER BY id DESC`, cid, admin)
		if err != nil {
			writeErr(w, 500, "读取包列表失败")
			return
		}
		defer rows.Close()
		out := []companyPack{}
		for rows.Next() {
			var p companyPack
			if err = rows.Scan(&p.ID, &p.Key, &p.Name, &p.Version, &p.License, &p.Sources, &p.Count, &p.Status, &p.Reviewer, &p.Reviewed); err != nil {
				writeErr(w, 500, "读取包失败")
				return
			}
			out = append(out, p)
		}
		if rows.Err() != nil {
			writeErr(w, 500, "读取列表失败")
			return
		}
		writeJSON(w, 200, map[string]any{"packs": out, "can_review": admin})
		return
	}
	id, err := strconv.ParseInt(base, 10, 64)
	if err != nil || id < 1 {
		writeErr(w, 400, "无效包编号")
		return
	}
	if r.Method == http.MethodPost {
		var b struct{ Status string }
		if decode(r, &b) != nil || (b.Status != "approved" && b.Status != "disabled") {
			writeErr(w, 400, "无效审批状态")
			return
		}
		res, err := pg.ExecContext(r.Context(), `UPDATE company_knowledge_packs SET status=$3,reviewed_by=$4,reviewed_at=now() WHERE company_id=$1 AND id=$2`, cid, id, b.Status, principal.Actor)
		if err != nil {
			writeErr(w, 500, "保存审批失败")
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			writeErr(w, 404, "知识包不存在")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	// Scope and review status are checked in the same query as the contents.
	var raw json.RawMessage
	before := r.URL.Query().Get("after")
	err = pg.QueryRowContext(r.Context(), `SELECT (SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]') FROM
 (SELECT entry_key AS id,title,body,source FROM company_knowledge_pack_entries WHERE pack_id=p.id AND entry_key>$4
 AND ($5='' OR strpos(lower(title||' '||body),lower($5))>0) ORDER BY entry_key LIMIT 31) t)
 FROM company_knowledge_packs p WHERE company_id=$1 AND p.id=$2 AND ($3 OR status='approved')`, cid, id, admin, before, r.URL.Query().Get("q")).Scan(&raw)
	if err == sql.ErrNoRows {
		writeErr(w, 404, "知识包不可见")
		return
	}
	if err != nil {
		writeErr(w, 500, "条目读取失败")
		return
	}
	var rows []packEntry
	if json.Unmarshal(raw, &rows) != nil {
		writeErr(w, 500, "条目读取失败")
		return
	}
	next := ""
	if len(rows) > 30 {
		rows = rows[:30]
		next = rows[29].ID
	}
	writeJSON(w, 200, map[string]any{"entries": rows, "next": next})
}
