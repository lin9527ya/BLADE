package mobile

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unicode/utf16"
)

// buildSyntheticAXML builds a valid Android binary XML AndroidManifest.xml buffer for unit testing.
func buildSyntheticAXML(t *testing.T) []byte {
	t.Helper()
	stringsList := []string{
		"manifest", "package", "versionCode", "versionName",
		"uses-sdk", "minSdkVersion", "targetSdkVersion",
		"uses-permission", "name",
		"application", "label", "debuggable", "usesCleartextTraffic",
		"activity", "exported", "intent-filter", "data", "scheme", "host",
		"com.example.shop", "1.4.2", "ShopApp", "android.permission.CAMERA",
		"com.example.shop.DeepLinkActivity", "shopapp", "api.acme-shop.cn",
	}
	idx := map[string]uint32{}
	for i, s := range stringsList {
		idx[s] = uint32(i)
	}

	poolChunk := encodeAXMLStringPoolUTF16(stringsList)

	var body bytes.Buffer
	writeChunk := func(typ uint16, hdr []byte, payload []byte) {
		hdrSize := uint16(8 + len(hdr))
		totalSize := uint32(int(hdrSize) + len(payload))
		_ = binary.Write(&body, binary.LittleEndian, typ)
		_ = binary.Write(&body, binary.LittleEndian, hdrSize)
		_ = binary.Write(&body, binary.LittleEndian, totalSize)
		body.Write(hdr)
		body.Write(payload)
	}
	type attrSpec struct {
		name  string
		typ   byte
		val   uint32
		rawID uint32
	}
	startEl := func(name string, attrs ...attrSpec) {
		var h bytes.Buffer
		_ = binary.Write(&h, binary.LittleEndian, uint32(1)) // line
		_ = binary.Write(&h, binary.LittleEndian, uint32(androidAttrNoIndex))
		var p bytes.Buffer
		_ = binary.Write(&p, binary.LittleEndian, uint32(androidAttrNoIndex)) // ns
		_ = binary.Write(&p, binary.LittleEndian, idx[name])
		_ = binary.Write(&p, binary.LittleEndian, uint16(20)) // attrStart
		_ = binary.Write(&p, binary.LittleEndian, uint16(20)) // attrSize
		_ = binary.Write(&p, binary.LittleEndian, uint16(len(attrs)))
		_ = binary.Write(&p, binary.LittleEndian, uint16(0)) // idIdx
		_ = binary.Write(&p, binary.LittleEndian, uint16(0)) // classIdx
		_ = binary.Write(&p, binary.LittleEndian, uint16(0)) // styleIdx
		for _, a := range attrs {
			_ = binary.Write(&p, binary.LittleEndian, uint32(androidAttrNoIndex))
			_ = binary.Write(&p, binary.LittleEndian, idx[a.name])
			_ = binary.Write(&p, binary.LittleEndian, a.rawID)
			_ = binary.Write(&p, binary.LittleEndian, uint16(8))
			_ = binary.Write(&p, binary.LittleEndian, byte(0))
			_ = binary.Write(&p, binary.LittleEndian, a.typ)
			_ = binary.Write(&p, binary.LittleEndian, a.val)
		}
		writeChunk(resXMLStartElementType, h.Bytes(), p.Bytes())
	}
	endEl := func(name string) {
		var h bytes.Buffer
		_ = binary.Write(&h, binary.LittleEndian, uint32(1))
		_ = binary.Write(&h, binary.LittleEndian, uint32(androidAttrNoIndex))
		var p bytes.Buffer
		_ = binary.Write(&p, binary.LittleEndian, uint32(androidAttrNoIndex))
		_ = binary.Write(&p, binary.LittleEndian, idx[name])
		writeChunk(resXMLEndElementType, h.Bytes(), p.Bytes())
	}

	startEl("manifest",
		attrSpec{"package", resTypeString, idx["com.example.shop"], idx["com.example.shop"]},
		attrSpec{"versionCode", resTypeIntDec, 42, uint32(androidAttrNoIndex)},
		attrSpec{"versionName", resTypeString, idx["1.4.2"], idx["1.4.2"]},
	)
	startEl("uses-sdk",
		attrSpec{"minSdkVersion", resTypeIntDec, 24, uint32(androidAttrNoIndex)},
		attrSpec{"targetSdkVersion", resTypeIntDec, 34, uint32(androidAttrNoIndex)},
	)
	endEl("uses-sdk")
	startEl("uses-permission", attrSpec{"name", resTypeString, idx["android.permission.CAMERA"], idx["android.permission.CAMERA"]})
	endEl("uses-permission")
	startEl("application",
		attrSpec{"label", resTypeString, idx["ShopApp"], idx["ShopApp"]},
		attrSpec{"debuggable", resTypeIntBoolean, 0xffffffff, uint32(androidAttrNoIndex)},
		attrSpec{"usesCleartextTraffic", resTypeIntBoolean, 0xffffffff, uint32(androidAttrNoIndex)},
	)
	startEl("activity",
		attrSpec{"name", resTypeString, idx["com.example.shop.DeepLinkActivity"], idx["com.example.shop.DeepLinkActivity"]},
		attrSpec{"exported", resTypeIntBoolean, 0xffffffff, uint32(androidAttrNoIndex)},
	)
	startEl("intent-filter")
	startEl("data",
		attrSpec{"scheme", resTypeString, idx["shopapp"], idx["shopapp"]},
		attrSpec{"host", resTypeString, idx["api.acme-shop.cn"], idx["api.acme-shop.cn"]},
	)
	endEl("data")
	endEl("intent-filter")
	endEl("activity")
	endEl("application")
	endEl("manifest")

	var full bytes.Buffer
	total := uint32(8 + len(poolChunk) + body.Len())
	_ = binary.Write(&full, binary.LittleEndian, uint16(resXMLType))
	_ = binary.Write(&full, binary.LittleEndian, uint16(8))
	_ = binary.Write(&full, binary.LittleEndian, total)
	full.Write(poolChunk)
	full.Write(body.Bytes())
	return full.Bytes()
}

func encodeAXMLStringPoolUTF16(strs []string) []byte {
	var offsets bytes.Buffer
	var pool bytes.Buffer
	for _, s := range strs {
		_ = binary.Write(&offsets, binary.LittleEndian, uint32(pool.Len()))
		codes := utf16.Encode([]rune(s))
		_ = binary.Write(&pool, binary.LittleEndian, uint16(len(codes)))
		for _, c := range codes {
			_ = binary.Write(&pool, binary.LittleEndian, c)
		}
		_ = binary.Write(&pool, binary.LittleEndian, uint16(0))
	}
	for pool.Len()%4 != 0 {
		pool.WriteByte(0)
	}
	hdrSize := uint16(28)
	stringsStart := uint32(int(hdrSize) + offsets.Len())
	totalSize := stringsStart + uint32(pool.Len())

	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.LittleEndian, uint16(resStringPoolType))
	_ = binary.Write(&chunk, binary.LittleEndian, hdrSize)
	_ = binary.Write(&chunk, binary.LittleEndian, totalSize)
	_ = binary.Write(&chunk, binary.LittleEndian, uint32(len(strs)))
	_ = binary.Write(&chunk, binary.LittleEndian, uint32(0)) // styleCount
	_ = binary.Write(&chunk, binary.LittleEndian, uint32(0)) // UTF-16LE flags=0
	_ = binary.Write(&chunk, binary.LittleEndian, stringsStart)
	_ = binary.Write(&chunk, binary.LittleEndian, uint32(0)) // stylesStart
	chunk.Write(offsets.Bytes())
	chunk.Write(pool.Bytes())
	return chunk.Bytes()
}

func TestInspectPackage_SyntheticAPK(t *testing.T) {
	apkPath := filepath.Join(t.TempDir(), "sample.apk")
	axml := buildSyntheticAXML(t)

	f, err := os.Create(apkPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	addEntry := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	addEntry("AndroidManifest.xml", axml)
	addEntry("lib/arm64-v8a/libflutter.so", []byte("ELF"))
	addEntry("lib/arm64-v8a/libjiagu.so", []byte("JIAGU"))
	addEntry("classes.dex", []byte("https://mobile-api.acme-shop.cn/v1/login https://analytics.umeng.com/log https://schemas.android.com/apk/res/android"))
	addEntry("../../evil.txt", []byte("traversal"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	meta, err := InspectPackage(apkPath)
	if err != nil {
		t.Fatalf("InspectPackage: %v", err)
	}
	if meta.Platform != PlatformAndroid || meta.PackageName != "com.example.shop" {
		t.Fatalf("unexpected platform/pkg: %+v", meta)
	}
	if !meta.Debuggable || meta.CleartextTraffic == nil || !*meta.CleartextTraffic {
		t.Fatalf("expected debuggable & cleartext=true, got debuggable=%v cleartext=%v", meta.Debuggable, meta.CleartextTraffic)
	}
	if !slices.Contains(meta.Frameworks, "Flutter") {
		t.Errorf("expected Flutter framework, got %v", meta.Frameworks)
	}
	if !slices.Contains(meta.Packers, "360 Jiagu") {
		t.Errorf("expected 360 Jiagu packer, got %v", meta.Packers)
	}
	if !slices.Contains(meta.NativeABIs, "arm64-v8a") {
		t.Errorf("expected arm64-v8a ABI, got %v", meta.NativeABIs)
	}
	if !slices.Contains(meta.URLSchemes, "shopapp") {
		t.Errorf("expected scheme shopapp, got %v", meta.URLSchemes)
	}
	if !slices.Contains(meta.CandidateDomains, "mobile-api.acme-shop.cn") {
		t.Errorf("expected mobile-api.acme-shop.cn in candidates: %v", meta.CandidateDomains)
	}
	if slices.Contains(meta.CandidateDomains, "schemas.android.com") {
		t.Errorf("schemas.android.com should have been filtered")
	}
	if !slices.Contains(meta.ThirdPartySDKHosts, "analytics.umeng.com") {
		t.Errorf("expected umeng in ThirdPartySDKHosts: %v", meta.ThirdPartySDKHosts)
	}
	if len(meta.ExportedComponents) != 1 || meta.ExportedComponents[0].Name != "com.example.shop.DeepLinkActivity" {
		t.Errorf("unexpected exported components: %+v", meta.ExportedComponents)
	}
}

func TestInspectPackage_SyntheticIPA(t *testing.T) {
	ipaPath := filepath.Join(t.TempDir(), "app.ipa")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
  <key>CFBundleIdentifier</key><string>com.example.ios</string>
  <key>CFBundleDisplayName</key><string>ShopIOS</string>
  <key>CFBundleShortVersionString</key><string>2.0.1</string>
  <key>CFBundleExecutable</key><string>ShopBin</string>
  <key>NSAppTransportSecurity</key><dict>
    <key>NSAllowsArbitraryLoads</key><true/>
    <key>NSExceptionDomains</key><dict><key>legacy.acme-ios.cn</key><dict/></dict>
  </dict>
  <key>CFBundleURLTypes</key><array><dict>
    <key>CFBundleURLSchemes</key><array><string>shoppay</string></array>
  </dict></array>
</dict></plist>`

	// Synthetic 64-bit Mach-O header with LC_ENCRYPTION_INFO_64 cryptid=1.
	var machO bytes.Buffer
	_ = binary.Write(&machO, binary.LittleEndian, uint32(0xfeedfacf))
	_ = binary.Write(&machO, binary.LittleEndian, uint32(0x0100000c)) // arm64
	_ = binary.Write(&machO, binary.LittleEndian, uint32(0))
	_ = binary.Write(&machO, binary.LittleEndian, uint32(2)) // MH_EXECUTE
	_ = binary.Write(&machO, binary.LittleEndian, uint32(1)) // ncmds
	_ = binary.Write(&machO, binary.LittleEndian, uint32(24))
	_ = binary.Write(&machO, binary.LittleEndian, uint32(0))
	_ = binary.Write(&machO, binary.LittleEndian, uint32(0))
	// LC_ENCRYPTION_INFO_64 (cmd=0x2c, size=24, cryptid=1 at offset 16)
	_ = binary.Write(&machO, binary.LittleEndian, uint32(0x2c))
	_ = binary.Write(&machO, binary.LittleEndian, uint32(24))
	_ = binary.Write(&machO, binary.LittleEndian, uint32(4096))
	_ = binary.Write(&machO, binary.LittleEndian, uint32(8192))
	_ = binary.Write(&machO, binary.LittleEndian, uint32(1))
	_ = binary.Write(&machO, binary.LittleEndian, uint32(0))
	machO.WriteString(" https://ios-api.acme-ios.cn/v2/auth ")

	f, err := os.Create(ipaPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	add := func(n string, d []byte) {
		w, _ := zw.Create(n)
		_, _ = w.Write(d)
	}
	add("Payload/Shop.app/Info.plist", []byte(plist))
	add("Payload/Shop.app/ShopBin", machO.Bytes())
	add("Payload/Shop.app/Frameworks/Flutter.framework/Flutter", []byte("FLUTTER"))
	_ = zw.Close()
	f.Close()

	meta, err := InspectPackage(ipaPath)
	if err != nil {
		t.Fatalf("InspectPackage IPA: %v", err)
	}
	if meta.Platform != PlatformIOS || meta.PackageName != "com.example.ios" || meta.AppName != "ShopIOS" {
		t.Fatalf("unexpected IPA meta: %+v", meta)
	}
	if meta.ATS == nil || !meta.ATS.AllowsArbitraryLoads || !slices.Contains(meta.ATS.ExceptionDomains, "legacy.acme-ios.cn") {
		t.Errorf("unexpected ATS: %+v", meta.ATS)
	}
	if meta.FairPlayEncrypted == nil || !*meta.FairPlayEncrypted {
		t.Errorf("expected FairPlayEncrypted=true, got %v", meta.FairPlayEncrypted)
	}
	if !slices.Contains(meta.URLSchemes, "shoppay") || !slices.Contains(meta.CandidateDomains, "ios-api.acme-ios.cn") {
		t.Errorf("missing schemes/domains: %+v", meta)
	}
}

func TestAndroidCertHashAndUIXML(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mitmproxy", Organization: []string{"mitmproxy"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	hash, cert, err := AndroidCertHash(pemBytes)
	if err != nil || len(hash) != 8 || cert == nil {
		t.Fatalf("AndroidCertHash failed: hash=%q err=%v", hash, err)
	}

	uiXML := `<?xml version="1.0" encoding="UTF-8"?>
<hierarchy rotation="0">
  <node index="0" class="android.widget.FrameLayout" clickable="false" bounds="[0,0][1080,2400]">
    <node index="1" text="Login" resource-id="com.shop:id/btn_login" class="android.widget.Button" clickable="true" enabled="true" bounds="[100,500][300,600]"/>
    <node index="2" text="" content-desc="Username" class="android.widget.EditText" clickable="true" enabled="true" bounds="[100,300][500,400]"/>
  </node>
</hierarchy>`
	elems, trunc := ParseUIHierarchyXML([]byte(uiXML), 50)
	if trunc || len(elems) != 2 {
		t.Fatalf("unexpected UI elems: len=%d trunc=%v", len(elems), trunc)
	}
	if elems[0].CenterX != 200 || elems[0].CenterY != 550 || elems[0].Text != "Login" {
		t.Errorf("unexpected element 0: %+v", elems[0])
	}
	if !elems[1].Editable || elems[1].CenterX != 300 || elems[1].CenterY != 350 {
		t.Errorf("unexpected element 1: %+v", elems[1])
	}
}
