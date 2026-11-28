package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/traffic"
)

func TestFirstSemanticUnitPreservesTokens(t *testing.T) {
	got := firstSemanticUnit("Android/iOS 移动应用逆向与全链路审计。当目标给了 APK 样本时使用。")
	if got != "Android/iOS 移动应用逆向与全链路审计。" {
		t.Fatalf("unexpected CJK sentence split: %q", got)
	}

	gotVer := firstSemanticUnit("Supports MASVS v2.0 checks for api.demo.test endpoints. Secondary note ignored.")
	if gotVer != "Supports MASVS v2.0 checks for api.demo.test endpoints." {
		t.Fatalf("unexpected ASCII version/domain split: %q", gotVer)
	}

	long := strings.Repeat("token123 ", 40)
	gotLong := firstSemanticUnit(long)
	if !strings.HasSuffix(gotLong, "…") || len([]rune(gotLong)) > 225 {
		t.Fatalf("unexpected long truncation: %q", gotLong)
	}
}

func TestProbeHostToolFallbackAltNames(t *testing.T) {
	st := probeHostTool(context.Background(), hostToolProbe{
		Name:     "__blade_missing_aapt2_xyz__",
		AltNames: []string{"sh"},
		Args:     []string{"-c", "echo fallback-ok"},
		Purpose:  "fallback test",
	})
	if !st.Available || !strings.Contains(st.Version, "sh:") || !strings.Contains(st.Version, "fallback-ok") {
		t.Fatalf("expected fallback alt command to report available, got %+v", st)
	}
}

func TestSanitizeScreenshotNameAndAccountLabel(t *testing.T) {
	if got := sanitizeScreenshotName("../../evil/shot"); got != "shot.png" {
		t.Fatalf("unexpected sanitized screenshot name: %q", got)
	}
	if got := sanitizeScreenshotName("login.PNG"); got != "login.PNG" {
		t.Fatalf("expected clean png filename preserved, got %q", got)
	}
	if got := normalizeAccountLabel("  user_b  "); got != "user_b" {
		t.Fatalf("unexpected normalized account label: %q", got)
	}
}

func TestMobileDecompileAuditRouteAndTaskMobileAppGating(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		if m == nil {
			t.Skipf("PG unavailable (%v)", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	tr, err := traffic.Open(filepath.Join(m.dir, "traffic"), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.traffic = tr
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	srv := New(ctx, m, t.TempDir(), m.dir, m.dir)
	t.Cleanup(func() {
		cancel()
		srv.archive.wg.Wait()
		<-srv.side.done
	})

	// 1. Seeded mobile playbooks and skill visibility exist in DB
	tpls, err := m.pg.ListTaskTemplates()
	if err != nil {
		t.Fatalf("ListTaskTemplates failed: %v", err)
	}
	var hasMobileTpl bool
	for _, tpl := range tpls {
		if strings.Contains(tpl.Name, "移动 APP") {
			hasMobileTpl = true
			break
		}
	}
	if !hasMobileTpl {
		t.Fatalf("expected builtin mobile Playbook seed in task_templates, got %d templates", len(tpls))
	}

	// 2. Web-only task keeps task_mobile_app tools gated out
	webTask, err := m.CreateTaskWithOptions("Web plain", "scan web", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(webTask.ID, DeleteTaskOptions{}) })
	webTID, _ := strconv.ParseInt(webTask.ID, 10, 64)

	ts := agent.NewToolSet(nil, "")
	workerBase := append(ts.WorkerTools(), actool.DefaultTools()...)
	webCtx := agent.WithRunInfo(ctx, agent.RunInfo{TaskID: webTID, ExplorationID: webTask.ExpID})
	for _, tl := range agent.ToolResolve(webCtx, "worker", workerBase) {
		if tl.Name() == "mobile_decompile_audit" || tl.Name() == "mobile_evidence_check" {
			t.Fatalf("tool %s leaked into non-mobile web task", tl.Name())
		}
	}

	// 3. Create mobile task & upload sample directly into task workspace
	mobTask, err := m.CreateTaskWithOptions("Mobile app audit", "audit apk", db.TaskCreateOptions{AppMode: "mobile"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(mobTask.ID, DeleteTaskOptions{}) })
	mobTID, _ := strconv.ParseInt(mobTask.ID, 10, 64)

	apkBytes := buildMinimalTestAPK(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "sample.apk")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(fw, bytes.NewReader(apkBytes))
	_ = mw.Close()

	upReq := httptest.NewRequest(http.MethodPost, "/api/apps/upload?scope=task&id="+mobTask.ID, &body)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upRR := httptest.NewRecorder()
	srv.appUpload(upRR, upReq)
	if upRR.Code != 200 {
		t.Fatalf("appUpload task status=%d body=%s", upRR.Code, upRR.Body.String())
	}

	// Unbound device -> task_mobile_app tools ARE exposed while task_mobile_device tools remain hidden
	mobCtx := agent.WithRunInfo(ctx, agent.RunInfo{TaskID: mobTID, ExplorationID: mobTask.ExpID})
	resolved := agent.ToolResolve(mobCtx, "worker", workerBase)
	seenAppTools := map[string]bool{}
	for _, tl := range resolved {
		seenAppTools[tl.Name()] = true
		if tl.Name() == "mobile_device_status" || tl.Name() == "mobile_frida_hook" {
			t.Fatalf("device tool %s leaked without bound device", tl.Name())
		}
	}
	if !seenAppTools["mobile_decompile_audit"] || !seenAppTools["mobile_evidence_check"] {
		t.Fatalf("expected mobile_decompile_audit & mobile_evidence_check enabled on uploaded APK task, got %v", seenAppTools)
	}

	// 4. POST /api/tasks/{id}/mobile/decompile-audit with fast mode writes report artifacts cleanly
	daReq := httptest.NewRequest(http.MethodPost, "/api/tasks/"+mobTask.ID+"/mobile/decompile-audit", strings.NewReader(`{"mode":"fast"}`))
	daReq.SetPathValue("id", mobTask.ID)
	daRR := httptest.NewRecorder()
	srv.handleTaskMobileDecompileAudit(daRR, daReq)
	if daRR.Code != 200 {
		t.Fatalf("handleTaskMobileDecompileAudit failed: code=%d body=%s", daRR.Code, daRR.Body.String())
	}
	var daResp struct {
		OK    bool `json:"ok"`
		Audit struct {
			QualityStatus string `json:"quality_status"`
			AuditDir      string `json:"audit_dir"`
		} `json:"audit"`
	}
	if err := json.Unmarshal(daRR.Body.Bytes(), &daResp); err != nil || !daResp.OK || daResp.Audit.QualityStatus == "" {
		t.Fatalf("unexpected decompile audit response: %s (%v)", daRR.Body.String(), err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "tasks", mobTask.ID, daResp.Audit.AuditDir, "report.json")); err != nil {
		t.Fatalf("expected report.json in %s: %v", daResp.Audit.AuditDir, err)
	}

	// 5. extractTaskMobileSession accepts account_label and FindingsMissingReport returns no error
	exReq := httptest.NewRequest(http.MethodPost, "/api/tasks/"+mobTask.ID+"/mobile/extract-session", strings.NewReader(`{"account_label":"user_b"}`))
	exReq.SetPathValue("id", mobTask.ID)
	exRR := httptest.NewRecorder()
	srv.extractTaskMobileSession(exRR, exReq)
	if exRR.Code != 200 || !strings.Contains(exRR.Body.String(), `"account_label":"user_b"`) {
		t.Fatalf("extractTaskMobileSession account_label mismatch: code=%d body=%s", exRR.Code, exRR.Body.String())
	}
	if _, err := m.pg.FindingsMissingReport(mobTID, 10); err != nil {
		t.Fatalf("FindingsMissingReport failed: %v", err)
	}
}

func buildTestPlainWxapkgBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var keys []string
	total := 0
	for k, v := range files {
		keys = append(keys, k)
		total += len(v)
	}
	if total < 1200 {
		files["__padding__.bin"] = bytes.Repeat([]byte{0x00}, 2048-total)
		keys = append(keys, "__padding__.bin")
	}
	indexLen := 4
	for _, k := range keys {
		indexLen += 4 + len(k) + 8
	}
	offset := 14 + indexLen
	var idx, data bytes.Buffer
	for _, k := range keys {
		body := files[k]
		_ = binary.Write(&idx, binary.BigEndian, uint32(len(k)))
		idx.WriteString(k)
		_ = binary.Write(&idx, binary.BigEndian, uint32(offset))
		_ = binary.Write(&idx, binary.BigEndian, uint32(len(body)))
		offset += len(body)
		data.Write(body)
	}
	var out bytes.Buffer
	out.WriteByte(0xBE)
	_ = binary.Write(&out, binary.BigEndian, uint32(0))
	_ = binary.Write(&out, binary.BigEndian, uint32(indexLen))
	_ = binary.Write(&out, binary.BigEndian, uint32(data.Len()))
	out.WriteByte(0xED)
	_ = binary.Write(&out, binary.BigEndian, uint32(len(keys)))
	out.Write(idx.Bytes())
	out.Write(data.Bytes())
	return out.Bytes()
}

func TestMiniAppUploadStatePersistenceAndScreenshotRoute(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		if m == nil {
			t.Skipf("PG unavailable (%v)", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	srv := New(ctx, m, t.TempDir(), m.dir, m.dir)
	t.Cleanup(func() {
		cancel()
		srv.archive.wg.Wait()
		<-srv.side.done
	})

	task, err := m.CreateTaskWithOptions("MiniApp UI Flow", "audit wxapkg", db.TaskCreateOptions{AppMode: "mobile"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })

	wxBytes := buildTestPlainWxapkgBytes(t, map[string][]byte{
		"app.json":       []byte(`{"pages":["pages/index/index","pages/pay/pay"]}`),
		"app-service.js": []byte(`wx.request({url:"https://api.wx.test/v1/order"}); wx.cloud.init({env:"cloud-demo-1"});`),
	})
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("appid", "wxabcdef1234567890")
	fw, _ := mw.CreateFormFile("file", "wxabcdef1234567890_plain.wxapkg")
	_, _ = io.Copy(fw, bytes.NewReader(wxBytes))
	_ = mw.Close()

	upReq := httptest.NewRequest(http.MethodPost, "/api/apps/upload?scope=task&id="+task.ID, &body)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upRR := httptest.NewRecorder()
	srv.appUpload(upRR, upReq)
	if upRR.Code != 200 || !strings.Contains(upRR.Body.String(), "wxabcdef1234567890") || !strings.Contains(upRR.Body.String(), `"miniapp"`) {
		t.Fatalf("miniapp upload failed: code=%d body=%s", upRR.Code, upRR.Body.String())
	}

	// Run decompile audit and place a screenshot artifact, then confirm getTaskMobileState persists both
	daReq := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/mobile/decompile-audit", strings.NewReader(`{"mode":"fast"}`))
	daReq.SetPathValue("id", task.ID)
	daRR := httptest.NewRecorder()
	srv.handleTaskMobileDecompileAudit(daRR, daReq)
	if daRR.Code != 200 {
		t.Fatalf("miniapp decompile audit failed: code=%d body=%s", daRR.Code, daRR.Body.String())
	}

	shotDir := filepath.Join(m.dir, "tasks", task.ID, "screenshots")
	_ = os.MkdirAll(shotDir, 0o755)
	_ = os.WriteFile(filepath.Join(shotDir, "demo.png"), []byte("\x89PNG\r\n\x1a\nMOCK"), 0o600)

	stReq := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/mobile/state", nil)
	stReq.SetPathValue("id", task.ID)
	stRR := httptest.NewRecorder()
	srv.getTaskMobileState(stRR, stReq)
	if stRR.Code != 200 || !strings.Contains(stRR.Body.String(), `"decompile_audit"`) || !strings.Contains(stRR.Body.String(), `"demo.png"`) {
		t.Fatalf("getTaskMobileState missing persisted audit or screenshot: code=%d body=%s", stRR.Code, stRR.Body.String())
	}

	imgReq := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/mobile/screenshots/demo.png", nil)
	imgReq.SetPathValue("id", task.ID)
	imgReq.SetPathValue("name", "demo.png")
	imgRR := httptest.NewRecorder()
	srv.getTaskMobileScreenshot(imgRR, imgReq)
	if imgRR.Code != 200 || !bytes.HasPrefix(imgRR.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatalf("getTaskMobileScreenshot failed: code=%d", imgRR.Code)
	}
}
