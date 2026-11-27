package mobile

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func buildMinimalELF64SO(t *testing.T, withNX, withPIE bool) []byte {
	t.Helper()
	// Minimal valid little-endian ELF64 shared object (64-byte header + 2 program headers + section headers/dynstr/dynsym).
	var buf [512]byte
	// e_ident: \x7fELF, ELFCLASS64=2, ELFDATA2LSB=1, EV_CURRENT=1
	copy(buf[0:4], "\x7fELF")
	buf[4] = 2
	buf[5] = 1
	buf[6] = 1
	// e_type: ET_DYN=3 or ET_EXEC=2
	eType := uint16(2)
	if withPIE {
		eType = 3
	}
	binary.LittleEndian.PutUint16(buf[16:18], eType)
	binary.LittleEndian.PutUint16(buf[18:20], 183) // EM_AARCH64
	binary.LittleEndian.PutUint32(buf[20:24], 1)   // EV_CURRENT
	binary.LittleEndian.PutUint64(buf[32:40], 64)  // e_phoff = 64
	binary.LittleEndian.PutUint16(buf[52:54], 64)  // e_ehsize
	binary.LittleEndian.PutUint16(buf[54:56], 56)  // e_phentsize
	binary.LittleEndian.PutUint16(buf[56:58], 2)   // e_phnum = 2

	// Phdr 0: PT_GNU_STACK (0x6474e551) at off 64
	off := 64
	binary.LittleEndian.PutUint32(buf[off:off+4], 0x6474e551)
	stackFlags := uint32(6 | 1) // RWX if !withNX
	if withNX {
		stackFlags = 6 // RW (no X)
	}
	binary.LittleEndian.PutUint32(buf[off+4:off+8], stackFlags)

	// Phdr 1: PT_GNU_RELRO (0x6474e552) at off 120
	off = 120
	binary.LittleEndian.PutUint32(buf[off:off+4], 0x6474e552)
	binary.LittleEndian.PutUint32(buf[off+4:off+8], 4)

	copy(buf[240:], "https://native.api.test/v2/auth OpenSSL 3.0.13 Java_com_test_Native_sign")
	return buf[:]
}

func TestELFHermesProvisioningSASTAndAPKSignatures(t *testing.T) {
	// 1. ELF inspection + SBOM + URL scan
	meta := &PackageMetadata{Platform: PlatformAndroid, Debuggable: true, AllowBackup: true}
	InspectELFBinary("lib/arm64-v8a/libcrypto_sign.so", buildMinimalELF64SO(t, true, true), meta)
	if len(meta.NativeBinaries) != 1 {
		t.Fatalf("expected 1 ELF report, got %d", len(meta.NativeBinaries))
	}
	nb := meta.NativeBinaries[0]
	if !nb.PIE || !nb.NX || nb.RELRO != "partial" {
		t.Fatalf("unexpected ELF hardening report: %+v", nb)
	}
	if !slices.Contains(meta.CandidateDomains, "native.api.test") {
		t.Fatalf("expected URL extracted from ELF binary, got %v", meta.CandidateDomains)
	}
	if len(meta.SBOMHints) == 0 {
		t.Fatalf("expected SBOM version hint (OpenSSL 3.0.13) from ELF")
	}

	// 2. Hermes bytecode string recovery
	hermesBuf := append([]byte(nil), hermesMagicBytes...)
	hermesBuf = append(hermesBuf, 0, 0, 0, 0)
	hermesBuf = append(hermesBuf, []byte("https://hermes.api.test/graphql\x00\x00app_secret=deadbeef1234\x00")...)
	if !ExtractHermesStrings(hermesBuf, meta) || !meta.HermesBytecode {
		t.Fatalf("expected Hermes bytecode detection")
	}
	if !slices.Contains(meta.CandidateDomains, "hermes.api.test") {
		t.Fatalf("expected domain from Hermes bytecode, got %v", meta.CandidateDomains)
	}

	// 3. Static SAST rules on code buffers & manifest flags
	RunStaticSAST(meta, []NamedBuffer{
		{Name: "classes.dex", Data: []byte("onReceivedSslError SslErrorHandler proceed AES/ECB setJavaScriptEnabled addJavascriptInterface")},
	})
	var ids []string
	for _, f := range meta.StaticFindings {
		ids = append(ids, f.ID)
	}
	for _, want := range []string{
		"manifest-debuggable",
		"manifest-allow-backup",
		"android-ssl-handler-proceed",
		"android-webview-js-file-access",
		"android-weak-crypto-ecb-des-rc4",
	} {
		if !slices.Contains(ids, want) {
			t.Fatalf("missing SAST finding %q; got %v", want, ids)
		}
	}

	// 4. iOS embedded.mobileprovision parsing
	provPayload := append([]byte{0x30, 0x82, 0x01, 0x00}, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
  <key>TeamName</key><string>Acme Security</string>
  <key>Entitlements</key><dict>
    <key>application-identifier</key><string>TEAM123.com.acme.app</string>
    <key>get-task-allow</key><true/>
    <key>com.apple.developer.associated-domains</key><array><string>applinks:ios-link.acme.cn</string></array>
  </dict>
</dict></plist>`)...)
	provPayload = append(provPayload, 0x00, 0x01)
	iosMeta := &PackageMetadata{Platform: PlatformIOS}
	if err := ParseMobileProvision(provPayload, iosMeta); err != nil {
		t.Fatalf("ParseMobileProvision failed: %v", err)
	}
	if iosMeta.Provisioning == nil || !iosMeta.Provisioning.GetTaskAllow || !slices.Contains(iosMeta.CandidateDomains, "ios-link.acme.cn") {
		t.Fatalf("unexpected provisioning parse result: %+v domains=%v", iosMeta.Provisioning, iosMeta.CandidateDomains)
	}

	// 5. APK v1-only Janus signature detection
	tmpAPK := filepath.Join(t.TempDir(), "v1only.apk")
	var apkBuf bytes.Buffer
	zw := zip.NewWriter(&apkBuf)
	for name, content := range map[string]string{
		"AndroidManifest.xml": `<manifest package="com.janus.test"><application label="Janus"/></manifest>`,
		"META-INF/CERT.SF":    "Signature-Version: 1.0\n",
		"META-INF/CERT.RSA":   "PKCS7",
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	_ = zw.Close()
	if err := os.WriteFile(tmpAPK, apkBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	pm, err := InspectPackage(tmpAPK)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pm.SignatureSchemes, "v1") {
		t.Fatalf("expected v1 scheme detected, got %v", pm.SignatureSchemes)
	}
	hasJanus := false
	for _, f := range pm.StaticFindings {
		if f.ID == "apk-v1-only-janus" {
			hasJanus = true
		}
	}
	if !hasJanus {
		t.Fatalf("expected apk-v1-only-janus SAST finding, got %+v", pm.StaticFindings)
	}
}

func TestIPCFuzzSandboxAuditLogcatCrawlAndTransparentProxy(t *testing.T) {
	mgr := NewManager("")
	var cmds []string
	uiStep := 0
	mgr.runner = func(ctx context.Context, env []string, name string, args ...string) (string, string, int, error) {
		joined := strings.Join(args, " ")
		cmds = append(cmds, joined)
		switch {
		case strings.Contains(joined, "am startservice"):
			return "Starting service: Intent { cmp=com.demo.app/.BgSyncService }", "", 0, nil
		case strings.Contains(joined, "content query --uri content://com.demo.app.files/"):
			return "Row: 0 id=1, path=/data/secret", "", 0, nil
		case strings.Contains(joined, "am start"):
			return "Status: ok", "", 0, nil
		case strings.Contains(joined, "cat /data/local/tmp/ui_dump.xml"):
			uiStep++
			if uiStep == 1 {
				return `<hierarchy><node class="android.widget.Button" text="订单详情" resource-id="com.demo:id/btn_orders" clickable="true" enabled="true" bounds="[10,10][200,80]"/></hierarchy>`, "", 0, nil
			}
			return `<hierarchy><node class="android.widget.Button" text="注销账户" resource-id="com.demo:id/btn_logout" clickable="true" enabled="true" bounds="[10,10][200,80]"/></hierarchy>`, "", 0, nil
		case strings.Contains(joined, "dumpsys activity activities"):
			return "mResumedActivity: ActivityRecord{abc u0 com.demo.app/.MainActivity t10}", "", 0, nil
		case strings.Contains(joined, "dumpsys package com.demo.app"):
			return "userId=10245\n", "", 0, nil
		case strings.Contains(joined, "logcat -d"):
			return "D/Net(10245): Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig123456\nI/Net(10245): https://api.demo.cn/v1/user", "", 0, nil
		case strings.Contains(joined, "sh /data/local/tmp/sc_"):
			// Could be sandbox audit OR transparent iptables setup.
			return `===PERMS===
-rw-rw-rw- 1 u0_a100 u0_a100 128 2026-11-26 10:00 auth.xml
===SQLITE===
DB:/data/data/com.demo.app/databases/local.db|SQLite format 3
===PREFS===
FILE_START:/data/data/com.demo.app/shared_prefs/auth.xml
<map><string name="access_token">tok_super_secret_987654</string></map>
FILE_END:/data/data/com.demo.app/shared_prefs/auth.xml
===EXT===
-rw-r--r-- 1 u0_a100 u0_a100 64 2026-11-26 10:00 debug_dump.txt
transparent_ready:19901
`, "", 0, nil
		default:
			return "ok", "", 0, nil
		}
	}

	ctx := context.Background()
	meta := &PackageMetadata{
		PackageName: "com.demo.app",
		ExportedComponents: []ComponentSummary{
			{Type: "activity", Name: "com.demo.app.WebActivity", Exported: true},
			{Type: "service", Name: "com.demo.app.BgSyncService", Exported: true},
			{Type: "provider", Name: "com.demo.app.FileProv", Exported: true, Authorities: "com.demo.app.files", GrantURIPermissions: true},
		},
		URLSchemes: []string{"demoapp"},
	}

	fuzzRep, err := mgr.FuzzIPC(ctx, "emulator-5554", "", meta, 20)
	if err != nil {
		t.Fatalf("FuzzIPC failed: %v", err)
	}
	if fuzzRep.ProbedCount < 4 || fuzzRep.AccessibleCount < 4 {
		t.Fatalf("unexpected FuzzIPC stats: %+v", fuzzRep)
	}

	stRep, err := mgr.AuditSandboxStorage(ctx, "emulator-5554", "com.demo.app")
	if err != nil {
		t.Fatalf("AuditSandboxStorage failed: %v", err)
	}
	if len(stRep.SQLiteFiles) != 1 || len(stRep.Findings) < 3 {
		t.Fatalf("expected sqlite + prefs + world-writable + ext findings, got %+v", stRep)
	}

	logRep, err := mgr.CaptureLogcatAudit(ctx, "emulator-5554", "com.demo.app", 100)
	if err != nil {
		t.Fatalf("CaptureLogcatAudit failed: %v", err)
	}
	if len(logRep.Findings) < 2 {
		t.Fatalf("expected token & URL logcat findings, got %+v", logRep)
	}

	// Crawler should skip "注销账户" on step 2 and issue "keyevent 4" (back) instead of tapping dangerous button.
	var seq int64
	crawlRep, err := mgr.CrawlUI(ctx, "emulator-5554", UICrawlRequest{Package: "com.demo.app", MaxSteps: 2, StepDelayMs: 10}, func() int64 {
		seq += 3
		return seq
	})
	if err != nil {
		t.Fatalf("CrawlUI failed: %v", err)
	}
	if crawlRep.UniqueScreensVisited < 2 || crawlRep.TrafficDelta != 3 {
		t.Fatalf("unexpected crawl stats: %+v", crawlRep)
	}
	for _, c := range cmds {
		if strings.Contains(c, "btn_logout") {
			t.Fatalf("crawler must never tap destructive label")
		}
	}

	// Transparent proxy setup & cleanup must issue adb reverse and iptables removal.
	addr, err := mgr.SetupProxy(ctx, "emulator-5554", "transparent", 19901, "")
	if err != nil || !strings.Contains(addr, "transparent:127.0.0.1:19901") {
		t.Fatalf("transparent SetupProxy failed: addr=%q err=%v", addr, err)
	}
	if err := mgr.ClearProxy(ctx, "emulator-5554"); err != nil {
		t.Fatalf("ClearProxy failed: %v", err)
	}

	// Check Frida catalog contains upgraded Flutter + Native + Storage/FlagSecure modules.
	cat := FridaCatalog()
	var modIDs []string
	for _, m := range cat {
		modIDs = append(modIDs, m.ID)
	}
	for _, reqMod := range []string{"flutter_ssl_bypass", "native_tls_hook", "storage_crypto_audit", "flag_secure_bypass", "socket_traffic_tap"} {
		if !slices.Contains(modIDs, reqMod) {
			t.Fatalf("missing Frida catalog module %q", reqMod)
		}
	}
}
