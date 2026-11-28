package mobile

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func simpleIPA(t *testing.T, root, name string, extra bool) string {
	t.Helper()
	dir := filepath.Join(root, "samples")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".ipa")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, err := z.Create("Payload/Sample.app/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>org.example.sample</string></dict></plist>`))
	if extra {
		w, _ = z.Create("Payload/Sample.app/a.json")
		w.Write([]byte(`{"label":"example"}`))
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMobileReportRunIsolation(t *testing.T) {
	root := t.TempDir()
	a := simpleIPA(t, root, "a", true)
	b := simpleIPA(t, root, "b", false)
	first, err := DecompileAndAudit(t.Context(), a, root, DecompileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		r *DecompileAuditReport
		e error
	}
	out := make(chan result, 2)
	for range 2 {
		go func() {
			r, e := DecompileAndAudit(context.Background(), b, root, DecompileOptions{})
			out <- result{r, e}
		}()
	}
	ids := map[string]bool{}
	for range 2 {
		v := <-out
		if v.e != nil {
			t.Fatal(v.e)
		}
		if v.r.ScannedFiles != 1 || v.r.Extract.ExtractedFiles != 1 || ids[v.r.RunID] || v.r.AuditDir == first.AuditDir {
			t.Fatalf("unexpected run: %+v", v.r)
		}
		ids[v.r.RunID] = true
	}
	history, r, err := ReadReportHistory(root, "samples/b.ipa", "")
	if err != nil || len(history) != 2 || !ids[r.RunID] {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	if _, _, err = ReadReportHistory(root, "samples/b.ipa", first.RunID); !os.IsNotExist(err) {
		t.Fatalf("wrong sample: %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := DecompileAndAudit(cancelled, b, root, DecompileOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run: %v", err)
	}

	// Older tasks can contain one shared report alongside the new per-run history.
	legacyDir := filepath.Join(root, "mobile_audit")
	legacy := DecompileAuditReport{SamplePath: "samples/a.ipa", Warnings: []string{"legacy fixture only"}, Risks: []DecompiledCodeRisk{{ID: "legacy-only"}}}
	if err := writeDecompileAuditArtifacts(legacyDir, &legacy); err != nil {
		t.Fatal(err)
	}
	_, current, err := ReadReportHistory(root, "samples/b.ipa", r.RunID)
	if err != nil || current == nil || len(current.Risks) != 0 || len(current.Warnings) != len(r.Warnings) {
		t.Fatalf("legacy fields leaked across runs: %+v %v", current, err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "report.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	history, current, err = ReadReportHistory(root, "samples/b.ipa", "")
	if err != nil || len(history) != 2 || current == nil || current.RunID != r.RunID {
		t.Fatalf("corrupt legacy file hid valid runs: %+v %v", history, err)
	}
	if _, _, err = ReadReportHistory(root, "samples/a.ipa", "legacy"); err == nil {
		t.Fatal("corrupt legacy report returned")
	}
}

func TestLegacyMobilePaperCopiesAreSafe(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mobile_audit")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	const sentinel = "PRIVATE_SYNTHETIC_MOBILE_MARKER"
	r := DecompileAuditReport{SamplePath: "samples/a.ipa", SampleSHA256: "legacy", Endpoints: []DecompiledEndpoint{{URL: "https://example.invalid/page?q=" + sentinel}}, Secrets: []DecompiledSecret{{Kind: "sample", Masked: "***", Snippet: sentinel}}, Risks: []DecompiledCodeRisk{{Snippet: sentinel}}, SignCandidates: []DecompiledSignCandidate{{Snippet: sentinel}}}
	if err := writeDecompileAuditArtifacts(path, &r); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		h, p, e := ReadReportHistory(root, "samples/a.ipa", "legacy")
		if e != nil || len(h) != 1 || p.RunID != "legacy" || p.Secrets[0].Snippet != "" {
			t.Fatalf("history %v %v", h, e)
		}
	}
	for _, name := range []string{"report.json", "endpoints.txt", "manifest.json", "secrets.txt"} {
		raw, err := os.ReadFile(filepath.Join(path, name))
		if err != nil || bytes.Contains(raw, []byte(sentinel)) {
			t.Fatalf("unsafe %s: err=%v", name, err)
		}
	}
}

func TestMobilePlatformQualityReceipts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "samples")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var apk bytes.Buffer
	apkWriter := zip.NewWriter(&apk)
	manifest, _ := apkWriter.Create("AndroidManifest.xml")
	manifest.Write([]byte(`<manifest package="org.example.fixture"><application/></manifest>`))
	if err := apkWriter.Close(); err != nil {
		t.Fatal(err)
	}
	archive := func(name string, files map[string][]byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		f, e := os.Create(p)
		if e != nil {
			t.Fatal(e)
		}
		z := zip.NewWriter(f)
		for name, data := range files {
			w, e := z.Create(name)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = w.Write(data); e != nil {
				t.Fatal(e)
			}
		}
		if e = z.Close(); e != nil {
			t.Fatal(e)
		}
		if e = f.Close(); e != nil {
			t.Fatal(e)
		}
		return p
	}
	split, e := InspectUploadedPackage(archive("sample.apks", map[string][]byte{"splits/base-master.apk": apk.Bytes()}), "")
	if e != nil || split.PackageName != "org.example.fixture" || !split.ReviewIncomplete() {
		t.Fatalf("split: %+v %v", split, e)
	}
	main := buildSyntheticWxapkg(t, map[string][]byte{"app.json": []byte(`{"pages":["pages/home"]}`)})
	p := archive("collection.zip", map[string][]byte{"main.wxapkg": main, "other.wxapkg": []byte("unreadable fixture")})
	rep, e := DecompileAndAudit(t.Context(), p, root, DecompileOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if rep.QualityStatus == "full" || rep.Extract.PackagesSeen != 2 || rep.Extract.PackagesRead != 1 || rep.ManifestMeta.MiniApp.SkippedPackages != 1 || len(rep.Extract.Warnings) == 0 {
		t.Fatalf("partial coverage: %+v", rep)
	}
	ipa, e := DecompileAndAudit(t.Context(), simpleIPA(t, root, "metadata", false), root, DecompileOptions{})
	if e != nil || len(ipa.Checks) < 3 || ipa.QualityStatus == "full" || ipa.QualityStatus == "raw_only" {
		t.Fatalf("%+v %v", ipa, e)
	}
}
