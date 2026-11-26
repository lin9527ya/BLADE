package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"unicode/utf16"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/traffic"
)

func buildMinimalTestAPK(t *testing.T) []byte {
	t.Helper()
	// Plain XML AndroidManifest.xml inside a valid ZIP is accepted by ParseAndroidManifest and hasZIPMagic.
	manifest := `<?xml version="1.0" encoding="utf-8"?>
<manifest package="com.test.mobileapp" versionCode="7" versionName="1.0.7">
  <uses-sdk minSdkVersion="24" targetSdkVersion="34"/>
  <uses-permission name="android.permission.CAMERA"/>
  <application label="MobileTest" debuggable="true" usesCleartextTraffic="true">
    <activity name="com.test.mobileapp.DeepActivity" exported="true">
      <intent-filter>
        <data scheme="mtest" host="api.mobile-target.cn"/>
      </intent-filter>
    </activity>
  </application>
</manifest>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	add("AndroidManifest.xml", []byte(manifest))
	add("lib/arm64-v8a/libflutter.so", []byte("ELF"))
	add("classes.dex", []byte("https://api.mobile-target.cn/v1/profile"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	// Avoid unused imports warnings even if utf16/binary unused in plain manifest helper.
	_ = binary.LittleEndian
	_ = utf16.Encode
	return buf.Bytes()
}

func TestAppUploadCreateTaskAndDynamicToolGating(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		if m == nil {
			t.Skipf("PG unavailable (%v)", err)
		}
		t.Fatal(err)
	}
	defer m.Close()
	// Open traffic store/CA without starting go-mitmproxy's attackerListener loop
	// (upstream go-mitmproxy@v1.9.2 attackerListener.Close is a no-op under goleak).
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

	// 1. Upload APK to staging via streaming /api/apps/upload
	apkBytes := buildMinimalTestAPK(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "demo-app.apk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(fw, bytes.NewReader(apkBytes)); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/apps/upload?scope=staging&id=draft-test-1", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	srv.appUpload(rr, req)
	if rr.Code != 200 {
		t.Fatalf("appUpload status=%d body=%s", rr.Code, rr.Body.String())
	}
	var upResp struct {
		Primary struct {
			SamplePath string `json:"sample_path"`
			Manifest   struct {
				PackageName string   `json:"package_name"`
				Frameworks  []string `json:"frameworks"`
			} `json:"manifest"`
		} `json:"primary"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &upResp); err != nil {
		t.Fatal(err)
	}
	if upResp.Primary.Manifest.PackageName != "com.test.mobileapp" || !slices.Contains(upResp.Primary.Manifest.Frameworks, "Flutter") {
		t.Fatalf("unexpected upload inspect result: %+v", upResp)
	}

	// 2. Create mobile task without binding a device -> mobile_* tools must be dynamically hidden!
	createPayload, _ := json.Marshal(map[string]any{
		"name":             "Mobile Integration Test",
		"description":      "Mobile APP test",
		"goal":             "Verify mobile flow",
		"app_mode":         "mobile",
		"app_sample_paths": []string{upResp.Primary.SamplePath},
		"app_extra_hosts":  []string{"api.mobile-target.cn"},
	})
	creq := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(createPayload))
	crr := httptest.NewRecorder()
	srv.createTask(crr, creq)
	if crr.Code != 201 {
		t.Fatalf("createTask status=%d body=%s", crr.Code, crr.Body.String())
	}
	var created TaskDTO
	if err := json.Unmarshal(crr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(created.ID, DeleteTaskOptions{DeleteAssets: true, DeleteFiles: true}) })

	if created.AppMode != "mobile" || created.AppPackageName != "com.test.mobileapp" {
		t.Fatalf("unexpected created task DTO: app_mode=%s pkg=%s", created.AppMode, created.AppPackageName)
	}

	// Verify TaskMobileInfo reports hasApp=true, hasDevice=false and WorkerTools excludes mobile_* when no device bound.
	tid, _ := parsePositiveInt64(created.ID)
	summary, hasApp, hasDev := agent.TaskMobileInfo(tid)
	if !hasApp || hasDev || summary == "" {
		t.Fatalf("TaskMobileInfo mismatch: hasApp=%v hasDev=%v summary=%q", hasApp, hasDev, summary)
	}

	ts := agent.NewToolSet(nil, "")
	workerBase := append(ts.WorkerTools(), actool.DefaultTools()...)
	workerCtx := agent.WithRunInfo(ctx, agent.RunInfo{TaskID: tid, ExplorationID: created.ExplorationID})
	unboundResolved := agent.ToolResolve(workerCtx, "worker", workerBase)
	for _, tl := range unboundResolved {
		if tl.Name() == "mobile_device_status" || tl.Name() == "mobile_frida_hook" {
			t.Fatalf("tool %s leaked when no mobile device is bound", tl.Name())
		}
	}

	// 3. Bind a mock mobile device -> mobile_* tools dynamically activate!
	bindBody, _ := json.Marshal(map[string]any{"serial": "192.168.10.55:5555", "proxy_mode": "usb_reverse"})
	breq := httptest.NewRequest(http.MethodPost, "/api/tasks/"+created.ID+"/mobile/bind", bytes.NewReader(bindBody))
	breq.SetPathValue("id", created.ID)
	brr := httptest.NewRecorder()
	srv.bindTaskMobileDevice(brr, breq)
	if brr.Code != 200 {
		t.Fatalf("bindTaskMobileDevice status=%d body=%s", brr.Code, brr.Body.String())
	}

	boundResolved := agent.ToolResolve(workerCtx, "worker", workerBase)
	foundMobile := false
	for _, tl := range boundResolved {
		if tl.Name() == "mobile_frida_hook" {
			foundMobile = true
			break
		}
	}
	if !foundMobile {
		t.Fatal("expected mobile_frida_hook dynamically exposed when device is bound")
	}

	// 4. Verify CA endpoint & LAN MobileProxy dynamic toggle
	caReq := httptest.NewRequest(http.MethodGet, "/api/traffic/ca?fmt=mobileconfig", nil)
	caRR := httptest.NewRecorder()
	srv.getTrafficCA(caRR, caReq)
	if caRR.Code != 200 || !bytes.Contains(caRR.Body.Bytes(), []byte("com.apple.security.root")) {
		t.Fatalf("CA mobileconfig endpoint failed: code=%d body=%s", caRR.Code, caRR.Body.String())
	}

	mpStartBody := []byte(`{"action":"start","listen_addr":"127.0.0.1:0"}`)
	mpStartReq := httptest.NewRequest(http.MethodPost, "/api/traffic/mobile-proxy", bytes.NewReader(mpStartBody))
	mpStartRR := httptest.NewRecorder()
	srv.controlTrafficMobileProxy(mpStartRR, mpStartReq)
	if mpStartRR.Code != 200 {
		t.Fatalf("controlTrafficMobileProxy start status=%d body=%s", mpStartRR.Code, mpStartRR.Body.String())
	}
	var mpInfo struct {
		Enabled bool   `json:"enabled"`
		CAToken string `json:"ca_token"`
	}
	if err := json.Unmarshal(mpStartRR.Body.Bytes(), &mpInfo); err != nil {
		t.Fatal(err)
	}
	if !mpInfo.Enabled || mpInfo.CAToken == "" {
		t.Fatalf("expected enabled LAN proxy + ca_token, got %+v", mpInfo)
	}

	mpStopReq := httptest.NewRequest(http.MethodPost, "/api/traffic/mobile-proxy", bytes.NewReader([]byte(`{"action":"stop"}`)))
	mpStopRR := httptest.NewRecorder()
	srv.controlTrafficMobileProxy(mpStopRR, mpStopReq)
	if mpStopRR.Code != 200 {
		t.Fatalf("controlTrafficMobileProxy stop status=%d body=%s", mpStopRR.Code, mpStopRR.Body.String())
	}

	// Also ensure db.TaskHasMobileDevice returns false for bogus/web task.
	webTask, werr := m.CreateTaskWithOptions("Web target", "scan web", db.TaskCreateOptions{})
	if werr != nil {
		t.Fatal(werr)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(webTask.ID, DeleteTaskOptions{}) })
	wtid, _ := parsePositiveInt64(webTask.ID)
	if m.pg.TaskHasMobileDevice(wtid) {
		t.Fatal("web task unexpectedly reported mobile device")
	}
}

func parsePositiveInt64(s string) (int64, error) {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, nil
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}
