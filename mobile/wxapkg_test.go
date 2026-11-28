package mobile

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

func buildSyntheticWxapkg(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var keys []string
	total := 0
	for k, v := range files {
		keys = append(keys, k)
		total += len(v)
	}
	slices.Sort(keys)
	if total < 1200 {
		files["__padding__.bin"] = bytes.Repeat([]byte{0x00}, 2048-total)
		keys = append(keys, "__padding__.bin")
		slices.Sort(keys)
	}

	indexLen := 4
	for _, k := range keys {
		indexLen += 4 + len(k) + 8
	}
	offset := 14 + indexLen
	var idx bytes.Buffer
	var data bytes.Buffer
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

func encryptSyntheticV1MMWX(t *testing.T, plain []byte, appID string) []byte {
	t.Helper()
	key := pbkdf2.Key([]byte(appID), []byte(wxapkgSalt), 1000, 32, sha1.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 1024)
	copy(head, plain[:1023])
	cipher.NewCBCEncrypter(block, []byte(wxapkgIV)).CryptBlocks(head, head)

	xorKey := appID[len(appID)-2]
	rest := plain[1023:]
	tail := make([]byte, len(rest))
	for i, b := range rest {
		tail[i] = b ^ xorKey
	}
	out := make([]byte, 0, len(wxapkgMagicV1MMWX)+len(head)+len(tail))
	out = append(out, []byte(wxapkgMagicV1MMWX)...)
	out = append(out, head...)
	out = append(out, tail...)
	return out
}

func TestWxapkgV1MMWXDecryptTraversalGuardAndDecompileAudit(t *testing.T) {
	const appID = "wx1234567890abcdef"
	plain := buildSyntheticWxapkg(t, map[string][]byte{
		"app.json": []byte(`{"pages":["pages/index/index","pages/order/order"],"subPackages":[{"root":"pkgA","pages":["pages/detail/detail"]}],"plugins":{"myPlug":{"version":"1.0"}}}`),
		"app-service.js": []byte(`
wx.request({ url: "https://api.mini.test/v1/user/profile" });
wx.cloud.init({ env: "prod-env-01" });
wx.cloud.callFunction({ name: "getUserOrder", data: {} });
wx.cloud.database().collection("orders").get();
var appSecret = "0123456789abcdef0123456789abcdef";
function signReq(p) { return md5(p + "salt"); }
`),
		"pages/index/index.wxml": []byte(`<web-view src="https://h5.mini.test/entry"></web-view><view>{{user.name}}</view>`),
	})

	enc := encryptSyntheticV1MMWX(t, plain, appID)
	if got := DetectWxapkgKind(enc); got != "v1mmwx" {
		t.Fatalf("expected v1mmwx, got %q", got)
	}
	dec, err := DecryptV1MMWX(enc, appID)
	if err != nil {
		t.Fatalf("DecryptV1MMWX failed: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Fatalf("decrypted wxapkg byte mismatch")
	}

	// Malicious traversal entry must be rejected cleanly
	evil := buildSyntheticWxapkg(t, map[string][]byte{
		"../evil_poc.txt": []byte("pwned"),
		"app.json":        []byte(`{"pages":["pages/index/index"]}`),
	})
	if _, _, err := SafeExtractWxapkgData(evil, appID, t.TempDir(), defaultArchiveLimits()); err == nil {
		t.Fatal("expected traversal path inside wxapkg to be rejected")
	}

	// Full task DecompileAndAudit on encrypted .wxapkg inside task workspace
	taskRoot := t.TempDir()
	samplesDir := filepath.Join(taskRoot, "samples")
	_ = os.MkdirAll(samplesDir, 0o755)
	pkgPath := filepath.Join(samplesDir, appID+"_v1.wxapkg")
	if err := os.WriteFile(pkgPath, enc, 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := DecompileAndAudit(context.Background(), pkgPath, taskRoot, DecompileOptions{Mode: "fast"})
	if err != nil {
		t.Fatalf("DecompileAndAudit wxapkg failed: %v", err)
	}
	if rep.Platform != PlatformMiniApp || rep.QualityStatus != "full" || rep.PackageName != appID {
		t.Fatalf("unexpected miniapp report metadata: %+v", rep)
	}
	if rep.ManifestMeta == nil || rep.ManifestMeta.MiniApp == nil {
		t.Fatalf("missing ManifestMeta.MiniApp")
	}
	ma := rep.ManifestMeta.MiniApp
	if len(ma.Routes) != 3 || !slices.Contains(ma.CloudEnvs, "prod-env-01") || !slices.Contains(ma.CloudFunctions, "getUserOrder") || !slices.Contains(ma.CloudCollections, "orders") {
		t.Fatalf("unexpected miniapp parsed metadata: %+v", ma)
	}
	if len(rep.Endpoints) == 0 || len(rep.Secrets) == 0 || len(rep.SignCandidates) == 0 {
		t.Fatalf("expected endpoints/secrets/sign_candidates scanned, got ep=%d sec=%d sign=%d", len(rep.Endpoints), len(rep.Secrets), len(rep.SignCandidates))
	}
	if strings.Contains(rep.Secrets[0].Masked, "0123456789abcdef0123456789abcdef") {
		t.Fatalf("raw appSecret leaked unmasked: %s", rep.Secrets[0].Masked)
	}

	// Also verify ZIP containing .wxapkg inspects as miniapp
	zipPath := filepath.Join(samplesDir, "bundle.zip")
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	fw, _ := zw.Create(appID + "/__APP__.wxapkg")
	_, _ = fw.Write(enc)
	_ = zw.Close()
	_ = os.WriteFile(zipPath, zbuf.Bytes(), 0o644)

	zMeta, err := InspectPackage(zipPath)
	if err != nil || zMeta.Platform != PlatformMiniApp || zMeta.PackageName != appID {
		t.Fatalf("InspectPackage miniapp zip failed: meta=%+v err=%v", zMeta, err)
	}
	_ = json.Marshal // keep json used or clean import
}
