package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// CVE 库 HTTP 面：人工维护 CVE 详情、产品/版本、指纹与利用脚本。
// 所有路由在 /api/ 下，走统一 JWT 中间件；脚本执行不在这里，agent 侧 cve_exploit
// 会先经过现有 guard（范围/时间窗/规则/审批），再走执行台账。

const (
	maxCVEBodyBytes   = 2 << 20 // create/update：脚本最多 1MiB + 其他字段
	maxCVEScriptBytes = 1 << 20
)

func (s *Server) registerCVERoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/cves", s.listCVEs)
	mux.HandleFunc("POST /api/cves", s.createCVE)
	mux.HandleFunc("POST /api/cves/import/preview", s.previewCVEImport)
	mux.HandleFunc("GET /api/cves/{id}", s.getCVE)
	mux.HandleFunc("PUT /api/cves/{id}", s.updateCVE)
	mux.HandleFunc("PATCH /api/cves/{id}", s.patchCVE)
	mux.HandleFunc("POST /api/cves/delete/batch", s.deleteCVEsBatch)
	mux.HandleFunc("DELETE /api/cves/{id}", s.deleteCVE)
	mux.HandleFunc("POST /api/cves/{id}/script", s.uploadCVEScript)
	mux.HandleFunc("DELETE /api/cves/{id}/script", s.clearCVEScript)
}

func (s *Server) cveStore(w http.ResponseWriter) *db.AssetStore {
	as := s.m.Assets()
	if as == nil {
		writeErr(w, 503, "asset store 未启用")
		return nil
	}
	return as
}

func parseCVEIDPath(r *http.Request) (int64, bool) {
	id := parseIntDefault(r.PathValue("id"))
	return id, id > 0
}

func (s *Server) listCVEs(w http.ResponseWriter, r *http.Request) {
	as := s.cveStore(w)
	if as == nil {
		return
	}
	q := r.URL.Query()
	f := db.CVEFilter{
		Q: q.Get("q"), Status: q.Get("status"), Severity: q.Get("severity"), Language: q.Get("language"),
		Limit: atoiDefault(q.Get("limit"), 50), Offset: atoiDefault(q.Get("offset"), 0),
	}
	if v := strings.TrimSpace(q.Get("has_exploit")); v != "" {
		switch strings.ToLower(v) {
		case "1", "true":
			b := true
			f.HasExploit = &b
		case "0", "false":
			b := false
			f.HasExploit = &b
		default:
			writeErr(w, 400, "has_exploit 只支持 true|false")
			return
		}
	}
	list, total, err := as.ListCVEs(f)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"cves": list, "total": total})
}

func (s *Server) getCVE(w http.ResponseWriter, r *http.Request) {
	as := s.cveStore(w)
	if as == nil {
		return
	}
	id, ok := parseCVEIDPath(r)
	if !ok {
		writeErr(w, 400, "bad CVE id")
		return
	}
	c, err := as.GetCVE(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if c == nil {
		writeErr(w, 404, "CVE 不存在")
		return
	}
	writeJSON(w, 200, c)
}

func decodeCVEBody(w http.ResponseWriter, r *http.Request) (db.CVE, bool) {
	var c db.CVE
	r.Body = http.MaxBytesReader(w, r.Body, maxCVEBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return c, false
	}
	return c, true
}

func cveDuplicateErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate key")
}

func (s *Server) createCVE(w http.ResponseWriter, r *http.Request) {
	as := s.cveStore(w)
	if as == nil {
		return
	}
	c, ok := decodeCVEBody(w, r)
	if !ok {
		return
	}
	if existing, _ := as.GetCVEByCVEID(c.CVEID); existing != nil {
		writeErr(w, 409, "CVE 编号已存在")
		return
	}
	out, err := as.CreateCVE(c)
	if err != nil {
		if cveDuplicateErr(err) {
			writeErr(w, 409, "CVE 编号已存在")
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) updateCVE(w http.ResponseWriter, r *http.Request) {
	as := s.cveStore(w)
	if as == nil {
		return
	}
	id, ok := parseCVEIDPath(r)
	if !ok {
		writeErr(w, 400, "bad CVE id")
		return
	}
	c, ok := decodeCVEBody(w, r)
	if !ok {
		return
	}
	if existing, _ := as.GetCVEByCVEID(c.CVEID); existing != nil && existing.ID != id {
		writeErr(w, 409, "CVE 编号已存在")
		return
	}
	out, err := as.UpdateCVE(id, c)
	if err != nil {
		if cveDuplicateErr(err) {
			writeErr(w, 409, "CVE 编号已存在")
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	if out == nil {
		writeErr(w, 404, "CVE 不存在")
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) patchCVE(w http.ResponseWriter, r *http.Request) {
	as := s.cveStore(w)
	if as == nil {
		return
	}
	id, ok := parseCVEIDPath(r)
	if !ok {
		writeErr(w, 400, "bad CVE id")
		return
	}
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	n, err := as.SetCVEStatus(id, req.Status, req.Reason)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if n == 0 {
		writeErr(w, 404, "CVE 不存在")
		return
	}
	c, err := as.GetCVE(id)
	if err != nil || c == nil {
		writeErr(w, 500, "读取更新后的 CVE 失败")
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) deleteCVE(w http.ResponseWriter, r *http.Request) {
	as := s.cveStore(w)
	if as == nil {
		return
	}
	id, ok := parseCVEIDPath(r)
	if !ok {
		writeErr(w, 400, "bad CVE id")
		return
	}
	n, err := as.DeleteCVE(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if n == 0 {
		writeErr(w, 404, "CVE 不存在")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// maxCVEDeleteBatch 是单次批量删除上限：防止一次误提交把整库带走。
const maxCVEDeleteBatch = 200

// deleteCVEsBatch 批量硬删除 CVE，返回逐条结果：单条不存在不会拖垮整批。
func (s *Server) deleteCVEsBatch(w http.ResponseWriter, r *http.Request) {
	as := s.cveStore(w)
	if as == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	ids := make([]int64, 0, len(req.IDs))
	seen := make(map[int64]struct{}, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 {
			writeErr(w, 400, "CVE id 无效")
			return
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 || len(ids) > maxCVEDeleteBatch {
		writeErr(w, 400, fmt.Sprintf("ids 数量必须为 1-%d", maxCVEDeleteBatch))
		return
	}
	deleted, err := as.DeleteCVEs(ids)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	deletedSet := make(map[int64]struct{}, len(deleted))
	for _, id := range deleted {
		deletedSet[id] = struct{}{}
	}
	type item struct {
		ID    int64  `json:"id"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	items := make([]item, 0, len(ids))
	for _, id := range ids {
		_, ok := deletedSet[id]
		it := item{ID: id, OK: ok}
		if !ok {
			it.Error = "CVE 不存在"
		}
		items = append(items, it)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// uploadCVEScript 接收 multipart 的 file 字段（也兼容纯文本 body 的 script 字段）。
// 脚本正文仍存 DB；落盘只在执行时由 execution service 生成服务端路径。
func (s *Server) uploadCVEScript(w http.ResponseWriter, r *http.Request) {
	as := s.cveStore(w)
	if as == nil {
		return
	}
	id, ok := parseCVEIDPath(r)
	if !ok {
		writeErr(w, 400, "bad CVE id")
		return
	}
	c, err := as.GetCVE(id)
	if err != nil || c == nil {
		writeErr(w, 404, "CVE 不存在")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCVEScriptBytes+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeErr(w, 400, "需要 multipart 上传（file 字段）")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "缺少 file 字段")
		return
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxCVEScriptBytes+1))
	if err != nil {
		writeErr(w, 400, "读取脚本失败")
		return
	}
	if len(body) > maxCVEScriptBytes {
		writeErr(w, 413, "脚本不能超过 1 MiB")
		return
	}
	lang := db.NormalizeExploitLanguage(r.FormValue("language"))
	if lang == "" {
		switch strings.ToLower(filepath.Ext(hdr.Filename)) {
		case ".py":
			lang = "python3"
		case ".sh":
			lang = "bash"
		case ".rb":
			lang = "ruby"
		case ".pl":
			lang = "perl"
		case ".php":
			lang = "php"
		case ".js":
			lang = "node"
		case ".ps1":
			lang = "pwsh"
		}
	}
	if lang == "" {
		writeErr(w, 400, "无法从文件名识别脚本语言，请指定 language")
		return
	}
	filename := strings.TrimSpace(r.FormValue("filename"))
	if filename == "" {
		filename = hdr.Filename
	}
	out, err := as.UpdateCVEScript(id, body, lang, filepath.Base(filename))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if out == nil {
		writeErr(w, 404, "CVE 不存在")
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) clearCVEScript(w http.ResponseWriter, r *http.Request) {
	as := s.cveStore(w)
	if as == nil {
		return
	}
	id, ok := parseCVEIDPath(r)
	if !ok {
		writeErr(w, 400, "bad CVE id")
		return
	}
	out, err := as.ClearCVEScript(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if out == nil {
		writeErr(w, 404, "CVE 不存在")
		return
	}
	writeJSON(w, 200, out)
}
