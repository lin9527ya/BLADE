package server

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/mobile"
)

// Map only a registered sample from this task, including the absolute paths
// written by older versions. Report history remains readable after file cleanup.
func (s *Server) registeredMobileSample(id int64, cfg db.TaskMobileConfig, requested string) (string, error) {
	root := filepath.Join(s.m.dir, "tasks", strconv.FormatInt(id, 10))
	for _, path := range cfg.AppSamplePaths {
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(s.m.dir, filepath.FromSlash(path))
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "samples/") || !filepath.IsLocal(rel) {
			continue
		}
		if requested == "" || requested == path || requested == rel || filepath.Clean(requested) == abs {
			return rel, nil
		}
	}
	return "", fmt.Errorf("所选样本未登记在当前任务")
}

func (s *Server) mobileManualReviewAllowed(r *http.Request) bool {
	p := principalFrom(r.Context())
	if s.m == nil || s.m.pg == nil || p.Role != "admin" || p.Actor != "admin" {
		return false
	}
	value, found, err := s.m.pg.GetSetting(settingCredentialPlaintext)
	return err == nil && (!found || value == "true" || value == "1")
}

func (s *Server) mobileReports(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	cfg, err := s.m.pg.TaskMobileInfo(id)
	if err != nil {
		writeErr(w, 500, "读取样本登记失败")
		return
	}
	sample, err := s.registeredMobileSample(id, cfg, r.URL.Query().Get("sample_path"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	history, rep, err := mobile.ReadReportHistory(filepath.Join(s.m.dir, "tasks", strconv.FormatInt(id, 10)), sample, r.URL.Query().Get("run_id"))
	if os.IsNotExist(err) {
		writeErr(w, 404, "所选样本没有这次报告")
		return
	}
	if err != nil {
		writeErr(w, 500, "报告读取失败")
		return
	}
	var receipt *appUploadResult
	path := filepath.Join(s.m.dir, "tasks", strconv.FormatInt(id, 10), filepath.FromSlash(sample))
	if stored, e := readMobileReceipt(path); e == nil {
		stored.Registration = "registered"
		if stored.Manifest != nil {
			stored.Manifest.ProtectReportMetadata()
		}
		receipt = &stored
	} else if !os.IsNotExist(e) {
		writeErr(w, 500, "读取样本回执失败")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, map[string]any{"receipt": receipt, "history": history, "audit": rep, "manual_source_review": s.mobileManualReviewAllowed(r)})
}

// Legacy exports may also be opened from the normal workspace after restoring
// an old archive, before its mobile workbench has been visited.
func (s *Server) protectLegacyMobileFile(rel string) error {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(rel)), "/")
	if len(parts) != 4 || parts[0] != "tasks" || parts[2] != "mobile_audit" {
		return nil
	}
	if id, e := strconv.ParseInt(parts[1], 10, 64); e != nil || id <= 0 {
		return nil
	}
	switch parts[3] {
	case "report.json", "secrets.txt", "manifest.json", "endpoints.txt":
		taskRoot := filepath.Join(s.m.dir, "tasks", parts[1])
		if _, err := os.Lstat(filepath.Join(taskRoot, "mobile_audit", "report.json")); err != nil {
			return err
		}
		return mobile.ProtectLegacyReport(taskRoot)
	}
	return nil
}

// The workspace root itself can be a symlink. Compare paths relative to its
// physical root so links elsewhere in the workspace retain the same policy.
func (s *Server) realMobileWorkspaceRel(abs string) (string, error) {
	root, err := filepath.EvalSymlinks(s.m.dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("路径不属于工作区")
	}
	return filepath.ToSlash(rel), nil
}

func (s *Server) validMobileReviewFile(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 9 || parts[0] != "tasks" || parts[2] != "mobile_audit" || parts[3] != "runs" || len(parts[4]) != 64 || len(parts[5]) != 64 || strings.HasPrefix(parts[6], ".") || parts[7] != "sources" || !filepath.IsLocal(rel) {
		return false
	}
	if _, ok := s.m.Task(parts[1]); !ok {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return true
}

func (s *Server) protectOldMobileExports() {
	if s.m == nil || s.m.dir == "" {
		return
	}
	entries, err := os.ReadDir(filepath.Join(s.m.dir, "tasks"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || id < 1 {
			continue
		}
		if err := mobile.ProtectLegacyReport(filepath.Join(s.m.dir, "tasks", e.Name())); err != nil {
			log.Printf("[mobile] 任务 %d 历史报告需要复核，未发布旧报告", id)
		}
	}
}

func (s *Server) allowMobileWorkspaceRead(w http.ResponseWriter, r *http.Request, abs string) bool {
	rel := s.wsRel(abs)
	real, err := s.realMobileWorkspaceRel(abs)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, 404, "文件不存在")
		} else {
			writeErr(w, 400, "文件路径无效")
		}
		return false
	}
	if err := s.protectLegacyMobileFile(real); err != nil {
		writeErr(w, 500, "历史报告暂不可安全读取")
		return false
	}
	p := strings.Split(real, "/")
	generated := len(p) >= 9 && p[0] == "tasks" && p[2] == "mobile_audit" && p[3] == "runs" && p[7] == "sources"
	legacy := len(p) >= 5 && p[0] == "tasks" && p[2] == "samples" && p[3] == "decompiled"
	manual := r.URL.Query().Get("mobile_manual_review") == "1"
	if !generated && !legacy && !manual {
		return true
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if !s.mobileManualReviewAllowed(r) || (manual && (rel != real || r.URL.Query().Get("path") != real || !s.validMobileReviewFile(real))) {
		writeErr(w, 403, "原文仅供开启明文保真的人工会话核验")
		return false
	}
	return true
}
