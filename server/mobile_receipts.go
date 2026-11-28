package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/mobile"
)

// The app workspace may contain user-created links. Walk each directory from
// its already-confined parent instead of allowing a link to redirect an upload.
func ensureMobileDir(base string, parts ...string) (string, error) {
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", err
	}
	dir := base
	for _, part := range parts {
		if !filepath.IsLocal(part) || filepath.Base(part) != part || part == "." {
			root.Close()
			return "", fmt.Errorf("样本目录无效")
		}
		if e := root.Mkdir(part, 0700); e != nil && !os.IsExist(e) {
			root.Close()
			return "", e
		}
		expected, e := root.Lstat(part)
		if e != nil || !expected.IsDir() {
			root.Close()
			return "", fmt.Errorf("样本目录无效")
		}
		next, e := root.OpenRoot(part)
		root.Close()
		if e != nil {
			return "", e
		}
		actual, e := next.Stat(".")
		if e != nil || !os.SameFile(expected, actual) {
			next.Close()
			return "", fmt.Errorf("样本目录已变化")
		}
		root = next
		dir = filepath.Join(dir, part)
	}
	root.Close()
	return dir, nil
}

func mobileReceiptFile(sample string) string {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Base(sample))))
	return filepath.Join(filepath.Dir(sample), ".receipts", key+".json")
}
func saveMobileReceipt(sample string, r appUploadResult) error {
	name := mobileReceiptFile(sample)
	if _, e := ensureMobileDir(filepath.Dir(sample), ".receipts"); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(name), ".receipt-")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	e = json.NewEncoder(tmp).Encode(r)
	if e == nil {
		e = tmp.Sync()
	}
	closeErr := tmp.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), name)
}
func readMobileReceipt(sample string) (appUploadResult, error) {
	var r appUploadResult
	name := mobileReceiptFile(sample)
	parent, e := os.OpenRoot(filepath.Dir(sample))
	if e != nil {
		return r, e
	}
	defer parent.Close()
	dir, e := parent.Lstat(".receipts")
	if e != nil {
		return r, e
	}
	if !dir.IsDir() {
		return r, fmt.Errorf("无效回执目录")
	}
	rel := filepath.Join(".receipts", filepath.Base(name))
	info, e := parent.Lstat(rel)
	if e != nil {
		return r, e
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return r, fmt.Errorf("无效样本回执")
	}
	f, e := parent.Open(rel)
	if e != nil {
		return r, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(b, &r)
	return r, e
}
func inspectUploadedSample(abs, rel, hint string, size int64) appUploadResult {
	result := appUploadResult{Name: filepath.Base(abs), SamplePath: filepath.ToSlash(rel), SizeBytes: size, Upload: "stored", Inspection: "complete", Registration: "staged", AppID: mobile.ExtractWeChatAppID(hint)}
	m, e := mobile.InspectUploadedPackage(abs, result.AppID)
	if e != nil {
		result.SHA256, _ = mobile.SampleSHA256(abs)
		result.Inspection = "failed"
		result.Message = e.Error()
		return result
	}
	m.SamplePath = result.SamplePath
	m.ProtectReportMetadata()
	result.SHA256 = m.SHA256
	result.Manifest = m
	if m.ReviewIncomplete() {
		result.Inspection = "partial"
		result.Message = "包信息仅部分可读，详见解析状态"
	}
	return result
}
func (s *Server) retryMobileRegistration(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	task := strconv.FormatInt(id, 10)
	if !s.engine.beginTaskOperation(task) {
		writeErr(w, 409, "任务正在删除")
		return
	}
	defer s.engine.decInflight(task)
	var b struct {
		Path string `json:"sample_path"`
	}
	if decode(r, &b) != nil {
		writeErr(w, 400, "样本路径无效")
		return
	}
	prefix := "tasks/" + task + "/samples/"
	name := strings.TrimPrefix(b.Path, prefix)
	if b.Path != prefix+name || name == "" || filepath.Base(name) != name || !filepath.IsLocal(name) {
		writeErr(w, 400, "请选择本任务的样本")
		return
	}
	abs := filepath.Join(s.m.dir, filepath.FromSlash(b.Path))
	real, err := s.realMobileWorkspaceRel(abs)
	if err != nil || real != b.Path {
		writeErr(w, 404, "样本不存在")
		return
	}
	receipt, err := readMobileReceipt(abs)
	if err != nil || receipt.SamplePath != b.Path {
		writeErr(w, 404, "上传回执不存在")
		return
	}
	digest, err := mobile.SampleSHA256(abs)
	if err != nil || receipt.SHA256 == "" || digest != receipt.SHA256 {
		writeErr(w, 409, "上传后样本已变更或原始摘要缺失，请重新上传")
		return
	}
	if err = s.adoptUploadedToTask(id, []appUploadResult{receipt}); err != nil {
		writeErr(w, 500, "任务登记失败，可重试")
		return
	}
	receipt.Registration = "registered"
	writeJSON(w, 200, receipt)
}

func (s *Server) pendingMobileSamples(taskID string, registered []string) ([]appUploadResult, error) {
	dir := filepath.Join(s.m.dir, "tasks", taskID, "samples")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []appUploadResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(registered))
	for _, p := range registered {
		known[filepath.Base(p)] = true
	}
	out := []appUploadResult{}
	for _, e := range entries {
		if !e.Type().IsRegular() || known[e.Name()] {
			continue
		}
		abs := filepath.Join(dir, e.Name())
		r, err := readMobileReceipt(abs)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if r.SamplePath != filepath.ToSlash(filepath.Join("tasks", taskID, "samples", e.Name())) {
			continue
		}
		if r.Registration != "failed" {
			r.Registration = "pending"
		}
		r.Manifest = nil
		out = append(out, r)
	}
	return out, nil
}
