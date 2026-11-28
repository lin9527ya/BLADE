package mobile

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildZipBytes(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip.Create(%s): %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip.Write(%s): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSafeExtractMobileArchiveBlocksZipSlip(t *testing.T) {
	dir := t.TempDir()
	evilZip := filepath.Join(dir, "evil.apk")
	raw := buildZipBytes(t, map[string]string{
		"../../etc/evil.txt":  "pwn",
		"AndroidManifest.xml": `<manifest package="com.evil.test"></manifest>`,
	})
	if err := os.WriteFile(evilZip, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if _, err := SafeExtractMobileArchive(evilZip, out, defaultArchiveLimits()); err == nil || !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("expected path traversal rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "evil.txt")); !os.IsNotExist(err) {
		t.Fatalf("evil entry escaped extraction directory!")
	}
}

func TestDecompileAndAuditQualityGateAndWhiteboxScan(t *testing.T) {
	taskRoot := t.TempDir()
	sampleDir := filepath.Join(taskRoot, "samples")
	if err := os.MkdirAll(sampleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	apkPath := filepath.Join(sampleDir, "demo.apk")
	raw := buildZipBytes(t, map[string]string{
		"AndroidManifest.xml":       `<manifest package="com.acme.mobile"></manifest>`,
		"lib/arm64-v8a/libjiagu.so": "elf",
		"assets/app_config.json":    `{"endpoint":"https://app-api.acme.example/v2/orders","app_secret":"SuperSecretValue1234567890"}`,
	})
	if err := os.WriteFile(apkPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate jadx exiting 0 WITHOUT producing Java files (e.g. silent packer failure) + whitebox findings in extracted JS/JSON.
	emptyRunner := func(_ context.Context, _ []string, _ string, _ ...string) (string, string, int, error) {
		return "done", "", 0, nil
	}
	rep, err := DecompileAndAudit(context.Background(), apkPath, taskRoot, DecompileOptions{
		SkipApktool:   true,
		CommandRunner: emptyRunner,
	})
	if err != nil {
		t.Fatalf("DecompileAndAudit failed: %v", err)
	}
	if rep.QualityStatus != "partial_packed" {
		t.Fatalf("expected quality_status=partial_packed when packer present and java=0, got %q", rep.QualityStatus)
	}

	// Now simulate real jadx output containing Retrofit, WebView, TrustAll, AES/ECB, and high-entropy token.
	jadxRunner := func(_ context.Context, _ []string, name string, args ...string) (string, string, int, error) {
		if strings.Contains(name, "jadx") {
			var outDir string
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "-d" {
					outDir = args[i+1]
				}
			}
			srcDir := filepath.Join(outDir, "sources", "com", "acme")
			if err := os.MkdirAll(srcDir, 0o755); err != nil {
				return "", err.Error(), 1, err
			}
			for i := 0; i < 10; i++ {
				fname := filepath.Join(srcDir, string(rune('A'+i))+"Service.java")
				body := "package com.acme;\n"
				if i == 0 {
					body += `
import retrofit2.http.POST;
public class AService {
  @POST("/api/v2/pay/confirm")
  void pay() {
    String url = "https://gateway.acme.example/v1/checkout";
    String aws = "AKIA1234567890ABCDEF";
    webView.addJavascriptInterface(new Bridge(), "NativeBridge");
    Cipher.getInstance("AES/ECB");
    sslErrorHandler.proceed();
  }
}
`
				}
				if err := os.WriteFile(fname, []byte(body), 0o644); err != nil {
					return "", err.Error(), 1, err
				}
			}
		}
		return "ok", "", 0, nil
	}

	rep2, err := DecompileAndAudit(context.Background(), apkPath, taskRoot, DecompileOptions{
		SkipApktool:   true,
		CommandRunner: jadxRunner,
	})
	if err != nil {
		t.Fatalf("DecompileAndAudit(jadxRunner) failed: %v", err)
	}
	if rep2.QualityStatus != "full" || rep2.JavaFileCount != 10 {
		t.Fatalf("expected full quality with 10 java files, got status=%s java=%d", rep2.QualityStatus, rep2.JavaFileCount)
	}

	// Assert whitebox endpoints, masked secrets, and code risks.
	hasRetrofit, hasGW := false, false
	for _, ep := range rep2.Endpoints {
		if ep.Method == "POST" && ep.URL == "/api/v2/pay/confirm" {
			hasRetrofit = true
		}
		if strings.Contains(ep.URL, "gateway.acme.example") {
			hasGW = true
		}
	}
	if !hasRetrofit || !hasGW {
		t.Fatalf("missing expected endpoints: %+v", rep2.Endpoints)
	}
	if len(rep2.Secrets) < 2 {
		t.Fatalf("expected >=2 extracted secrets, got %+v", rep2.Secrets)
	}
	secretsTxt, err := os.ReadFile(filepath.Join(taskRoot, "mobile_audit", "secrets.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(secretsTxt), "AKIA1234567890ABCDEF") || strings.Contains(string(secretsTxt), "SuperSecretValue1234567890") {
		t.Fatalf("secrets.txt leaked unmasked plaintext secrets:\n%s", string(secretsTxt))
	}
	hasWV, hasECB, hasTLS := false, false, false
	for _, r := range rep2.Risks {
		switch r.ID {
		case "webview-js-bridge":
			hasWV = true
		case "crypto-weak-ecb-des":
			hasECB = true
		case "tls-trust-all-certs":
			hasTLS = true
		}
	}
	if !hasWV || !hasECB || !hasTLS {
		t.Fatalf("expected webview/ecb/tls risks, got %+v", rep2.Risks)
	}
}
