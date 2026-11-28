package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/mobile"
)

func reviewMobileServer(t *testing.T) (*Server, *Task) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	task, err := m.CreateTask("mobile sample QA", "inventory", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	t.Cleanup(func() { _ = m.pg.DeleteTask(id) })
	return &Server{m: m, engine: NewEngine(m)}, task
}
func reviewUpload(t *testing.T, s *Server, scope, id, name string, raw []byte, hint string, want int) appUploadResult {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	w, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(raw)
	// Multipart fields can follow the file.
	if hint != "" {
		mw.WriteField("appid", hint)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/apps/upload?scope="+scope+"&id="+id, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.appUpload(rr, req)
	if rr.Code != want {
		t.Fatalf("upload %d: %s", rr.Code, rr.Body)
	}
	var resp struct{ Primary appUploadResult }
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Primary
}
func TestMobileUploadReceiptsPreserveInputAndRetry(t *testing.T) {
	s, task := reviewMobileServer(t)
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	const hint = "wx0123456789abcdef"
	raw := buildTestPlainWxapkgBytes(t, map[string][]byte{"app.json": []byte(`{"pages":["pages/home"]}`)})
	draft := reviewUpload(t, s, "staging", "mobile-qa-draft", "generic.wxapkg", raw, hint, 200)
	if draft.Upload != "stored" || draft.Inspection != "complete" || draft.Registration != "staged" || draft.Manifest.PackageName != hint {
		t.Fatal(draft)
	}
	adopted := s.prepareTaskMobileSamples(task, []string{draft.SamplePath}, nil)
	if len(adopted) != 1 || adopted[0].Registration != "registered" || adopted[0].Manifest.PackageName != hint {
		t.Fatalf("adopted=%+v", adopted)
	}
	meta, err := s.runTaskStaticAudit(id, adopted[0].SamplePath)
	if err != nil || meta.PackageName != hint {
		t.Fatalf("recheck=%+v err=%v", meta, err)
	}
	if _, _, err = s.resolveTaskSampleAbs(id, draft.SamplePath); err == nil {
		t.Fatal("unregistered draft accepted")
	}

	var broken bytes.Buffer
	z := zip.NewWriter(&broken)
	f, _ := z.Create("README.txt")
	f.Write([]byte("fixture"))
	z.Close()
	// A real DB failure occurs after file storage. Registration can be retried
	// against the original file, including when inspecting its contents failed.
	const drop = "ALTER TABLE tasks DROP CONSTRAINT IF EXISTS mobile_qa_registration"
	if _, err = s.m.pg.Exec(fmt.Sprintf("ALTER TABLE tasks ADD CONSTRAINT mobile_qa_registration CHECK(id<>%d) NOT VALID", id)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(drop) })
	failed := reviewUpload(t, s, "task", task.ID, "bad.ipa", broken.Bytes(), "", 207)
	if failed.Upload != "stored" || failed.Inspection != "failed" || failed.Registration != "failed" || failed.SHA256 == "" {
		t.Fatal(failed)
	}
	cfg, err := s.m.pg.TaskMobileInfo(id)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.pendingMobileSamples(task.ID, cfg.AppSamplePaths)
	if err != nil || len(pending) != 1 || pending[0].SamplePath != failed.SamplePath {
		t.Fatalf("pending=%+v %v", pending, err)
	}
	if _, err = s.m.pg.Exec(drop); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		payload, _ := json.Marshal(map[string]string{"sample_path": failed.SamplePath})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(payload))
		req.SetPathValue("id", task.ID)
		w := httptest.NewRecorder()
		s.retryMobileRegistration(w, req)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	cfg, err = s.m.pg.TaskMobileInfo(id)
	if err != nil || len(cfg.AppSamplePaths) != 2 || cfg.AppPackageName != hint {
		t.Fatalf("cfg=%+v %v", cfg, err)
	}
	registered, err := readMobileReceipt(filepath.Join(s.m.dir, failed.SamplePath))
	if err != nil || registered.Registration != "registered" || registered.SHA256 != failed.SHA256 {
		t.Fatalf("registration receipt on disk=%+v %v", registered, err)
	}
}

func TestMobileRegistrationOrderAndUploadRollback(t *testing.T) {
	s, task := reviewMobileServer(t)
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	raw := buildTestPlainWxapkgBytes(t, map[string][]byte{"app.json": []byte(`{"pages":["pages/home"]}`)})
	first := reviewUpload(t, s, "task", task.ID, "first.wxapkg", raw, "wx0123456789abcdef", 200)
	// Pre-existing installations also used absolute sample paths.
	paths, _ := json.Marshal([]string{filepath.Join(s.m.dir, first.SamplePath)})
	if _, err := s.m.pg.Exec("UPDATE tasks SET app_sample_paths=$1::jsonb WHERE id=$2", string(paths), id); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join("tasks", task.ID, "samples", "second.wxapkg")
	thirdPath := filepath.Join("tasks", task.ID, "samples", "third.wxapkg")
	results := make(chan error, 3)
	for _, path := range []string{secondPath, thirdPath, secondPath} {
		go func() {
			_, err := s.m.pg.AppendTaskMobileSamples(id, s.m.dir, []string{path}, nil)
			results <- err
		}()
	}
	for range 3 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := s.m.pg.TaskMobileInfo(id)
	if err != nil || len(cfg.AppSamplePaths) != 3 || cfg.AppSamplePaths[0] != first.SamplePath || !slices.Contains(cfg.AppSamplePaths, secondPath) || !slices.Contains(cfg.AppSamplePaths, thirdPath) || cfg.AppPackageName != first.Manifest.PackageName {
		t.Fatalf("registrations=%+v %v", cfg, err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, spec := range []struct {
		name string
		body []byte
	}{{"good.wxapkg", raw}, {"bad.wxapkg", []byte("invalid fixture")}} {
		part, err := mw.CreateFormFile("file", spec.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(spec.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/apps/upload?scope=staging&id=rollback", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.appUpload(w, req)
	entries, err := os.ReadDir(filepath.Join(s.m.dir, "drafts", "rollback", "samples"))
	if w.Code != 400 || err != nil || len(entries) != 0 {
		t.Fatalf("partial batch left files: code=%d entries=%v err=%v", w.Code, entries, err)
	}
}

func TestMobileManualFidelitySwitchAndLegacyReports(t *testing.T) {
	s, task := reviewMobileServer(t)
	old, found, err := s.m.pg.GetSetting(settingCredentialPlaintext)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if found {
			_ = s.m.pg.SetSetting(settingCredentialPlaintext, old)
		} else {
			_, _ = s.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingCredentialPlaintext)
		}
	})
	item := reviewUpload(t, s, "task", task.ID, "generic.wxapkg", buildTestPlainWxapkgBytes(t, map[string][]byte{"app.json": []byte(`{"pages":["pages/home"],"label":"ORIGINAL_FIXTURE_ONLY"}`)}), "", 200)
	root := filepath.Join(s.m.dir, "tasks", task.ID)
	rep, err := mobile.DecompileAndAudit(t.Context(), filepath.Join(s.m.dir, item.SamplePath), root, mobile.DecompileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.ToSlash(filepath.Join("tasks", task.ID, rep.DecompiledDir, "raw", "app.json"))
	read := func(path, role, actor string, manual bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/workspace/read?path="+path, nil)
		if manual {
			q := req.URL.Query()
			q.Set("mobile_manual_review", "1")
			req.URL.RawQuery = q.Encode()
		}
		req = req.WithContext(withPrincipal(req.Context(), runPrincipal{Actor: actor, Role: role}))
		rr := httptest.NewRecorder()
		s.wsRead(rr, req)
		return rr
	}
	if err = s.m.pg.SetSetting(settingCredentialPlaintext, "false"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.m.dir, filepath.FromSlash(source)), filepath.Join(s.m.dir, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{source, "alias.txt"} {
		if w := read(path, "admin", "admin", false); w.Code != 403 || strings.Contains(w.Body.String(), "ORIGINAL_FIXTURE_ONLY") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := read(source, "admin", "admin", true); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if err = s.m.pg.SetSetting(settingCredentialPlaintext, "true"); err != nil {
		t.Fatal(err)
	}
	if w := read(source, "admin", "admin", true); w.Code != 200 || !strings.Contains(w.Body.String(), "ORIGINAL_FIXTURE_ONLY") || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal(w.Code)
	}
	if w := read(source, "system", "scheduler", true); w.Code != 403 {
		t.Fatal(w.Code)
	}
	req := httptest.NewRequest("GET", "/?sample_path="+item.SamplePath, nil)
	req.SetPathValue("id", task.ID)
	req = req.WithContext(withPrincipal(req.Context(), runPrincipal{Role: "admin", Actor: "admin"}))
	w := httptest.NewRecorder()
	s.mobileReports(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), rep.RunID) || strings.Contains(w.Body.String(), "ORIGINAL_FIXTURE_ONLY") {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = os.Stat(filepath.Join(root, rep.AuditDir, "report.json")); err != nil {
		t.Fatal(err)
	}
}
