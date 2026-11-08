package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Workspace file manager — browse / view / edit / download / upload / delete the
// shared work dir (s.m.dir), where all agents write their artifacts. Every path is
// confined to the work dir root (traversal via ".." is neutralised). All routes sit
// behind requireAuth (see Handler()).

const (
	maxWorkspaceRead   = 2 << 20   // 2 MiB: files bigger than this aren't inlined for view/edit (download instead)
	maxWorkspaceUpload = 512 << 20 // 512 MiB per upload request
)

// wsResolve maps a user-supplied relative path to an absolute path INSIDE the work
// dir. It returns ok=false if the path would escape the root — **including via a
// symlink**. filepath.Clean on a rooted copy collapses any ".." so nothing can climb
// above the root lexically; 但工作区里一个指向外部的软链（agent 随手就能建）同样能
// 让 os.ReadFile 跟着走出去，所以词法收口之后还要把**最深的已存在祖先**真实解析
// （EvalSymlinks）再复查一次前缀。
//
// 返回的是逻辑路径（调用方与界面看到的是工作区相对路径），安全判定用真实路径。
// 校验与打开之间仍有 TOCTOU 窗口——同机的 agent 本来就有全盘权限，这个窗口不增加
// 实际风险；要更强的保证得用 openat/O_NOFOLLOW 逐段打开（见 deploy/README.md
// 的「信任模型」）。
func (s *Server) wsResolve(rel string) (string, bool) {
	base := filepath.Clean(s.m.dir)
	rel = strings.TrimPrefix(strings.TrimSpace(rel), "/")
	clean := filepath.Clean("/" + rel) // e.g. "/a/../../etc" → "/etc" (still rooted at "/")
	abs := filepath.Clean(filepath.Join(base, clean))
	if abs != base && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return "", false
	}
	// 工作区根本身可能就是软链（例如部署时 data -> 另一块盘）：先解析真实根再比。
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", false // 根解析不了：fail closed
	}
	realAbs, ok := resolveExisting(abs)
	if !ok {
		return "", false
	}
	if realAbs != realBase && !strings.HasPrefix(realAbs, realBase+string(os.PathSeparator)) {
		return "", false
	}
	return abs, true
}

// resolveExisting 把 path 解析成真实路径。目标可能还不存在（写新文件），所以从它自身
// 往上找**最深的已存在祖先**做 EvalSymlinks，再把未存在的尾巴拼回去。只有"不存在"才
// 继续往上找；其它解析错误一律 fail closed——不猜。
func resolveExisting(path string) (string, bool) {
	tail := ""
	for cur := path; ; {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if tail == "" {
				return real, true
			}
			return filepath.Join(real, tail), true
		}
		if !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(cur)
		if parent == cur { // 走到根还解析不出来
			return "", false
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

// wsRel renders an absolute path back as a workspace-relative path (forward slashes).
func (s *Server) wsRel(abs string) string {
	base := filepath.Clean(s.m.dir)
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

type wsEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix millis
}

// GET /api/workspace/list?path=<rel>
func (s *Server) wsList(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, "非法路径")
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeErr(w, 404, "路径不存在")
		return
	}
	if !fi.IsDir() {
		writeErr(w, 400, "不是目录")
		return
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]wsEntry, 0, len(ents))
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, wsEntry{
			Name:  e.Name(),
			Path:  s.wsRel(filepath.Join(abs, e.Name())),
			Dir:   e.IsDir(),
			Size:  info.Size(),
			MTime: info.ModTime().UnixMilli(),
		})
	}
	// 目录在前，各自按名称排序。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, 200, map[string]any{"path": s.wsRel(abs), "entries": out})
}

// GET /api/workspace/read?path=<rel> — inline text for view/edit. Binary or oversize
// files return {binary:true}/{too_large:true} with no content (use download instead).
func (s *Server) wsRead(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, "非法路径")
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeErr(w, 404, "文件不存在")
		return
	}
	if fi.IsDir() {
		writeErr(w, 400, "是目录，不能作为文件读取")
		return
	}
	version, err := workspaceVersion(abs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if fi.Size() > maxWorkspaceRead {
		writeJSON(w, 200, map[string]any{"path": s.wsRel(abs), "size": fi.Size(), "too_large": true, "binary": true, "version": version})
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		writeJSON(w, 200, map[string]any{"path": s.wsRel(abs), "size": fi.Size(), "binary": true, "version": version})
		return
	}
	writeJSON(w, 200, map[string]any{"path": s.wsRel(abs), "size": fi.Size(), "binary": false, "content": string(data), "version": workspaceContentVersion(data)})
}

// Existing files require the version returned by read; omission only creates new files.
func (s *Server) wsWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string  `json:"path"`
		Content string  `json:"content"`
		Version *string `json:"version"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceRead*6+65536)
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(req.Content) > maxWorkspaceRead {
		writeErr(w, 413, "在线编辑最多支持 2 MiB")
		return
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	abs, ok := s.wsResolve(req.Path)
	if !ok || abs == filepath.Clean(s.m.dir) {
		writeErr(w, 400, "非法路径")
		return
	}
	real, ok := resolveExisting(abs)
	if !ok {
		writeErr(w, 400, "非法路径")
		return
	}
	current, err := workspaceVersion(real)
	if err != nil && !os.IsNotExist(err) {
		writeErr(w, 400, "目标不可编辑："+err.Error())
		return
	}
	expected := ""
	if req.Version != nil {
		expected = *req.Version
	}
	if current != expected {
		writeJSON(w, 409, map[string]any{"error": "文件已修改或覆盖尚未确认；请重新读取，当前草稿仍可保留", "conflicts": []workspaceConflict{{Path: s.wsRel(abs), Version: current}}})
		return
	}
	if err := workspaceAtomicWrite(real, strings.NewReader(req.Content), expected); err != nil {
		if errors.Is(err, errWorkspaceChanged) {
			writeErr(w, 409, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": s.wsRel(abs), "version": workspaceContentVersion([]byte(req.Content))})
}

// POST /api/workspace/mkdir  {path}
func (s *Server) wsMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	abs, ok := s.wsResolve(req.Path)
	if !ok || abs == filepath.Clean(s.m.dir) {
		writeErr(w, 400, "非法路径")
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": s.wsRel(abs)})
}

// DELETE /api/workspace/delete?path=<rel> — removes a file or a directory tree
// (confined to the work dir; the root itself can't be deleted).
func (s *Server) wsDelete(w http.ResponseWriter, r *http.Request) {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	abs, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, "非法路径")
		return
	}
	if abs == filepath.Clean(s.m.dir) {
		writeErr(w, 400, "不能删除工作区根目录")
		return
	}
	if _, err := os.Stat(abs); err != nil {
		writeErr(w, 404, "路径不存在")
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// GET /api/workspace/download?path=<rel> — stream a file as an attachment.
func (s *Server) wsDownload(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, "非法路径")
		return
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		writeErr(w, 404, "文件不存在")
		return
	}
	name := filepath.Base(abs)
	// RFC 5987 filename* keeps non-ASCII names intact; plain filename is the fallback.
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(name)+"\"; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeFile(w, r, abs)
}

type workspaceUploadTarget struct {
	path     string
	expected string
}

type workspaceConflict struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// Preview existing names without transferring file bodies or changing the workspace.
func (s *Server) wsUploadCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path  string   `json:"path"`
		Names []string `json:"names"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(req.Names) == 0 || len(req.Names) > 1000 {
		writeErr(w, 400, "请选择 1–1000 个文件")
		return
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	_, conflicts, err := s.workspaceUploadTargets(req.Path, req.Names, nil)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"conflicts": conflicts})
}

func (s *Server) workspaceUploadTargets(dir string, names []string, expected map[string]string) ([]workspaceUploadTarget, []workspaceConflict, error) {
	abs, ok := s.wsResolve(dir)
	if !ok {
		return nil, nil, fmt.Errorf("非法路径")
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, nil, fmt.Errorf("目标目录不存在")
	}
	targets := make([]workspaceUploadTarget, 0, len(names))
	conflicts := make([]workspaceConflict, 0)
	seen := make(map[string]bool)
	for _, raw := range names {
		name := filepath.Base(raw)
		if name == "" || name == "." || name == ".." || seen[name] {
			return nil, nil, fmt.Errorf("上传文件名无效或重复")
		}
		seen[name] = true
		dest, ok := s.wsResolve(filepath.Join(s.wsRel(abs), name))
		if !ok {
			return nil, nil, fmt.Errorf("上传目标越界")
		}
		real, ok := resolveExisting(dest)
		if !ok {
			return nil, nil, fmt.Errorf("上传目标不可用")
		}
		version, err := workspaceVersion(real)
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("目标不可覆盖：%w", err)
		}
		key := s.wsRel(dest)
		if version != expected[key] {
			conflicts = append(conflicts, workspaceConflict{Path: key, Version: version})
		}
		targets = append(targets, workspaceUploadTarget{path: real, expected: expected[key]})
	}
	return targets, conflicts, nil
}

// POST /api/workspace/upload?path=<dir> — overwrite only versions explicitly confirmed by the client.
func (s *Server) wsUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, "解析上传失败或超出大小限制："+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) == 0 || len(files) > 1000 {
		writeErr(w, 400, "请选择 1–1000 个上传文件")
		return
	}
	expected := map[string]string{}
	if raw := r.FormValue("expected_versions"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &expected); err != nil {
			writeErr(w, 400, "覆盖版本无效")
			return
		}
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Filename)
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	targets, conflicts, err := s.workspaceUploadTargets(r.URL.Query().Get("path"), names, expected)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(conflicts) > 0 {
		writeJSON(w, 409, map[string]any{"error": "同名文件未确认覆盖或确认后又有修改；未上传任何文件", "conflicts": conflicts})
		return
	}
	saved := 0
	for i, hdr := range files {
		if err := saveWorkspaceUpload(hdr, targets[i].path, targets[i].expected); err != nil {
			status := 500
			if errors.Is(err, errWorkspaceChanged) {
				status = 409
			}
			writeJSON(w, status, map[string]any{"error": err.Error(), "uploaded": saved})
			return
		}
		saved++
	}
	writeJSON(w, 200, map[string]any{"uploaded": saved})
}

func saveUpload(hdr *multipart.FileHeader, dest string) error {
	return saveWorkspaceUpload(hdr, dest, "")
}

func saveWorkspaceUpload(hdr *multipart.FileHeader, dest, expected string) error {
	src, err := hdr.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	return workspaceAtomicWrite(dest, src, expected)
}

// Editable files use content hashes; large files use size and nanosecond mtime.
func workspaceVersion(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("不是普通文件")
	}
	if fi.Size() > maxWorkspaceRead {
		return fmt.Sprintf("stat:%d:%d", fi.Size(), fi.ModTime().UnixNano()), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxWorkspaceRead+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxWorkspaceRead {
		return "", fmt.Errorf("文件在读取过程中发生变化，请重试")
	}
	return workspaceContentVersion(data), nil
}

func workspaceContentVersion(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var errWorkspaceChanged = errors.New("文件在写入过程中再次修改；未覆盖该文件")

func workspaceAtomicWrite(path string, src io.Reader, expected string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".blade-edit-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := io.Copy(f, src); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	version, err := workspaceVersion(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if version != expected {
		return errWorkspaceChanged
	}
	return os.Rename(f.Name(), path)
}

// sanitizeFilename strips characters unsafe for a Content-Disposition filename token.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\\", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\r", "")
	return name
}
