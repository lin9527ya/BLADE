package mobile

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxZIPScanEntryBytes = 32 << 20 // Cap per-entry read for static string/manifest extraction
	maxURLsExtracted     = 120
	maxDomainsExtracted  = 80
)

var (
	urlRegex = regexp.MustCompile(`https?://[a-zA-Z0-9._-]+(?::[0-9]{2,5})?(?:/[a-zA-Z0-9._~:/?#[\]@!$&'()*+,;=%-]*)?`)
	keyRegex = regexp.MustCompile(`(?i)(?:api[_-]?secret|app[_-]?secret|client[_-]?secret|oss[_-]?access|AKIA[0-9A-Z]{16}|LTAI[0-9A-Za-z]{12,})`)
)

// packerSignatures maps ZIP path patterns (lowercased substrings) to known Android packer names.
var packerSignatures = []struct {
	pattern string
	name    string
}{
	{"libjiagu", "360 Jiagu"},
	{"/jiagu", "360 Jiagu"},
	{"libdexhelper", "Bangcle"},
	{"libsecexe", "Bangcle"},
	{"libexecmain", "Tencent Legu"},
	{"libexec.so", "Tencent Legu"},
	{"libshell", "Ali/Alibaba"},
	{"libnesec", "NetEase Yidun"},
	{"libpairipcore", "Google PairIP"},
	{"libbaiduprotect", "Baidu"},
	{"libijiami", "iJiami"},
	{"ijm_lib", "iJiami"},
	{"libkwscmm", "Kiwi Security"},
	{"libchaosvmp", "AliyunVMP"},
}

// frameworkSignatures detects cross-platform mobile frameworks.
var frameworkSignatures = []struct {
	pattern string
	name    string
}{
	{"libflutter.so", "Flutter"},
	{"flutter.framework/", "Flutter"},
	{"libapp.so", "Flutter AOT"},
	{"libreactnativejni.so", "React Native"},
	{"index.android.bundle", "React Native"},
	{"main.jsbundle", "React Native"},
	{"libweex", "Weex / uni-app"},
	{"assets/apps/__uni__", "uni-app"},
	{"libil2cpp.so", "Unity IL2CPP"},
	{"assets/bin/data/managed/", "Unity Mono"},
	{"assets/www/cordova.js", "Cordova/PhoneGap"},
	{"libxwalkcore.so", "Crosswalk WebView"},
	{"libelectron", "Electron/Hybrid"},
}

var ignoredNoiseHostSuffixes = []string{
	"schemas.android.com",
	"www.w3.org",
	"xml.org",
	"apache.org",
	"github.com",
	"googleapis.com",
	"gstatic.com",
	"google.com",
	"googlesyndication.com",
	"googletagmanager.com",
	"apple.com",
	"icloud.com",
	"localhost",
	"127.0.0.1",
	"0.0.0.0",
	"example.com",
	"example.org",
	"placeholder.com",
}

var thirdPartySDKHostSuffixes = []string{
	"umeng.com",
	"umengcloud.com",
	"bugly.qq.com",
	"sensorsdata.cn",
	"growingio.com",
	"jpush.cn",
	"jiguang.cn",
	"getui.com",
	"amap.com",
	"map.baidu.com",
	"crashlytics.com",
	"firebaseio.com",
	"sentry.io",
	"appsflyer.com",
	"adjust.com",
	"openinstall.io",
}

// InspectPackage validates and inspects a mobile archive (.apk, .aab, .xapk, .ipa, .wxapkg) on disk.
func InspectPackage(path string) (*PackageMetadata, error) {
	return InspectPackageWithHint(path, "")
}

// InspectPackageWithHint inspects a mobile/mini-program sample while optionally supplying a known WeChat AppID.
func InspectPackageWithHint(path, hintAppID string) (*PackageMetadata, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat sample: %w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("sample path is a directory")
	}
	sha, err := SampleSHA256(path)
	if err != nil {
		return nil, err
	}

	ext := strings.ToLower(filepath.Ext(path))
	fileType := strings.TrimPrefix(ext, ".")
	if fileType == "" {
		fileType = "apk"
	}

	if fileType == "wxapkg" {
		return inspectMiniAppPackage(path, sha, st.Size(), hintAppID)
	}
	if fileType == "zip" {
		if kind, ok := HasWxapkgMagic(path); ok && kind == "zip_bundle" {
			return inspectMiniAppPackage(path, sha, st.Size(), hintAppID)
		}
		return nil, fmt.Errorf("zip 压缩包内未检测到 .wxapkg 小程序包")
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open mobile archive %s: %w", filepath.Base(path), err)
	}
	defer zr.Close()

	meta := &PackageMetadata{
		SampleName:      filepath.Base(path),
		SamplePath:      path,
		SHA256:          sha,
		SizeBytes:       st.Size(),
		FileType:        fileType,
		InspectedAtUnix: time.Now().Unix(),
	}

	switch fileType {
	case "ipa":
		meta.Platform = PlatformIOS
		if err := inspectIPAArchive(&zr.Reader, meta); err != nil {
			return nil, err
		}
	case "apk", "aab", "apks":
		meta.Platform = PlatformAndroid
		if err := inspectAPKArchive(&zr.Reader, meta); err != nil {
			return nil, err
		}
		if fileType == "apk" {
			InspectAPKSignatures(&zr.Reader, path, meta)
		}
	case "xapk", "apkm":
		meta.Platform = PlatformAndroid
		if err := inspectXAPKArchive(&zr.Reader, meta); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported mobile sample extension %q (expected .apk/.aab/.xapk/.ipa)", ext)
	}

	RunStaticSAST(meta, nil)

	sort.Strings(meta.Frameworks)
	sort.Strings(meta.Packers)
	sort.Strings(meta.NativeABIs)
	sort.Strings(meta.URLSchemes)
	sort.Strings(meta.CandidateDomains)
	sort.Strings(meta.CandidateURLs)
	sort.Strings(meta.ThirdPartySDKHosts)
	sort.Strings(meta.SBOMHints)
	if len(meta.CandidateURLs) > maxURLsExtracted {
		meta.CandidateURLs = meta.CandidateURLs[:maxURLsExtracted]
	}
	if len(meta.CandidateDomains) > maxDomainsExtracted {
		meta.CandidateDomains = meta.CandidateDomains[:maxDomainsExtracted]
	}
	return meta, nil
}

func inspectAPKArchive(zr *zip.Reader, meta *PackageMetadata) error {
	var manifestBytes []byte
	var netSecBytes []byte
	var resourceCount, resourceRead int
	var codeBufs []NamedBuffer
	for _, f := range zr.File {
		if !isSafeZipEntryName(f.Name) {
			meta.Warnings = appendUnique(meta.Warnings, "archive contains unsafe path traversal entry: "+f.Name)
			continue
		}
		lower := strings.ToLower(f.Name)
		scanPackerAndFramework(lower, meta)
		extractABIFromPath(lower, meta)

		switch lower {
		case "androidmanifest.xml", "base/manifest/androidmanifest.xml":
			if len(manifestBytes) == 0 {
				manifestBytes, _ = readBoundedZipFile(f, maxAXMLDecodeBytes)
			}
		case "res/xml/network_security_config.xml", "base/res/xml/network_security_config.xml":
			resourceCount++
			netSecBytes, _ = readBoundedZipFile(f, 512<<10)
			if len(netSecBytes) > 0 && f.UncompressedSize64 <= 512<<10 {
				resourceRead++
			}
		}
		switch {
		case strings.HasSuffix(lower, ".so") && strings.Contains(lower, "lib/"):
			if raw, err := readBoundedZipFile(f, maxZIPScanEntryBytes); err == nil && len(raw) > 0 {
				InspectELFBinary(f.Name, raw, meta)
			}
		case strings.HasSuffix(lower, ".dex") ||
			strings.HasSuffix(lower, "index.android.bundle") ||
			strings.HasSuffix(lower, "global-metadata.dat") ||
			(strings.Contains(lower, "assets/") && (strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".js") || strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".properties") || strings.HasSuffix(lower, ".yaml"))) ||
			(strings.HasPrefix(lower, "res/xml/") && strings.HasSuffix(lower, ".xml")):
			if raw, err := readBoundedZipFile(f, maxZIPScanEntryBytes); err == nil && len(raw) > 0 {
				if !ExtractHermesStrings(raw, meta) {
					scanTextBufferForEndpoints(raw, meta)
					extractSBOMHints(raw, meta)
				}
				if len(codeBufs) < 16 {
					codeBufs = append(codeBufs, NamedBuffer{Name: f.Name, Data: raw})
				}
			}
		}
	}
	if len(manifestBytes) == 0 {
		return fmt.Errorf("AndroidManifest.xml not found in archive")
	}
	if err := ParseAndroidManifest(manifestBytes, meta); err != nil {
		return err
	}
	meta.Checks = append(meta.Checks, countedStage("android_manifest", 1, 1), countedStage("network_config", resourceRead, resourceCount))
	if len(netSecBytes) > 0 {
		inspectNetworkSecurityXML(netSecBytes, meta)
	}
	RunStaticSAST(meta, codeBufs)
	return nil
}

func inspectXAPKArchive(zr *zip.Reader, meta *PackageMetadata) error {
	// Look for manifest.json / base.apk / *.apk entries inside XAPK bundle.
	var inspectedBase bool
	for _, f := range zr.File {
		if !isSafeZipEntryName(f.Name) {
			continue
		}
		lower := strings.ToLower(f.Name)
		if !strings.HasSuffix(lower, ".apk") {
			continue
		}
		meta.SplitAPKNames = appendUnique(meta.SplitAPKNames, f.Name)
		// Inspect nested APK when size is bounded (base apk usually has manifest).
		if !inspectedBase || strings.Contains(lower, "base") || !strings.Contains(lower, "config.") {
			raw, err := readBoundedZipFile(f, 128<<20)
			if err != nil {
				continue
			}
			nzr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				continue
			}
			subMeta := &PackageMetadata{}
			if err := inspectAPKArchive(nzr, subMeta); err == nil && subMeta.PackageName != "" {
				mergeSubAPKMeta(meta, subMeta)
				if !strings.Contains(lower, "config.") {
					inspectedBase = true
				}
			}
		}
	}
	if meta.PackageName == "" {
		return fmt.Errorf("no valid base APK manifest found in XAPK bundle")
	}
	return nil
}

func mergeSubAPKMeta(dst, src *PackageMetadata) {
	if len(dst.Checks) == 0 {
		dst.Checks = append(dst.Checks, src.Checks...)
	}
	if dst.PackageName == "" {
		dst.PackageName = src.PackageName
		dst.AppName = src.AppName
		dst.VersionName = src.VersionName
		dst.VersionCode = src.VersionCode
		dst.MinSDK = src.MinSDK
		dst.TargetSDK = src.TargetSDK
		dst.Debuggable = src.Debuggable
		dst.AllowBackup = src.AllowBackup
		dst.CleartextTraffic = src.CleartextTraffic
		dst.NetworkSecurityCfg = src.NetworkSecurityCfg
		dst.NetworkSecTrustUser = src.NetworkSecTrustUser
		dst.Permissions = append(dst.Permissions, src.Permissions...)
		dst.ExportedComponents = append(dst.ExportedComponents, src.ExportedComponents...)
	}
	for _, fw := range src.Frameworks {
		dst.Frameworks = appendUnique(dst.Frameworks, fw)
	}
	for _, pk := range src.Packers {
		dst.Packers = appendUnique(dst.Packers, pk)
	}
	for _, abi := range src.NativeABIs {
		dst.NativeABIs = appendUnique(dst.NativeABIs, abi)
	}
	for _, s := range src.URLSchemes {
		dst.URLSchemes = appendUnique(dst.URLSchemes, s)
	}
	for _, d := range src.CandidateDomains {
		dst.CandidateDomains = appendUnique(dst.CandidateDomains, d)
	}
	for _, u := range src.CandidateURLs {
		dst.CandidateURLs = appendUnique(dst.CandidateURLs, u)
	}
	for _, h := range src.ThirdPartySDKHosts {
		dst.ThirdPartySDKHosts = appendUnique(dst.ThirdPartySDKHosts, h)
	}
	for _, w := range src.Warnings {
		dst.Warnings = appendUnique(dst.Warnings, w)
	}
	for _, sb := range src.SBOMHints {
		dst.SBOMHints = appendUnique(dst.SBOMHints, sb)
	}
	dst.NativeBinaries = append(dst.NativeBinaries, src.NativeBinaries...)
	dst.StaticFindings = append(dst.StaticFindings, src.StaticFindings...)
	if src.HermesBytecode {
		dst.HermesBytecode = true
	}
}

func inspectIPAArchive(zr *zip.Reader, meta *PackageMetadata) error {
	var plistBytes []byte
	var provBytes []byte
	var appDir string
	var machOExec []byte
	var provisionPresent, provisionRead int
	var binaryLimited, provisionLimited bool
	var execName string

	for _, f := range zr.File {
		if !isSafeZipEntryName(f.Name) {
			meta.Warnings = appendUnique(meta.Warnings, "archive contains unsafe path traversal entry: "+f.Name)
			continue
		}
		lower := strings.ToLower(f.Name)
		scanPackerAndFramework(lower, meta)
		// Match top-level Payload/<AppName>.app/Info.plist (not embedded frameworks/plugins)
		parts := strings.Split(f.Name, "/")
		if len(parts) == 3 && strings.EqualFold(parts[0], "Payload") && strings.HasSuffix(strings.ToLower(parts[1]), ".app") {
			if strings.EqualFold(parts[2], "Info.plist") {
				appDir = parts[0] + "/" + parts[1] + "/"
				plistBytes, _ = readBoundedZipFile(f, maxPlistDecodeBytes)
			} else if strings.EqualFold(parts[2], "embedded.mobileprovision") {
				provisionPresent = 1
				provisionLimited = f.UncompressedSize64 > maxPlistDecodeBytes
				provBytes, _ = readBoundedZipFile(f, maxPlistDecodeBytes)
			}
		}
	}
	if len(plistBytes) == 0 {
		return fmt.Errorf("Payload/*.app/Info.plist not found in IPA")
	}
	if err := ParseInfoPlist(plistBytes, meta); err != nil {
		return err
	}
	if len(provBytes) > 0 {
		if ParseMobileProvision(provBytes, meta) == nil {
			provisionRead = 1
		}
	}
	if dictRaw, err := decodePlistExecutableName(plistBytes); err == nil && dictRaw != "" {
		execName = dictRaw
	}
	for _, f := range zr.File {
		if !isSafeZipEntryName(f.Name) {
			continue
		}
		lower := strings.ToLower(f.Name)
		if appDir != "" && execName != "" && f.Name == appDir+execName {
			binaryLimited = f.UncompressedSize64 > maxZIPScanEntryBytes
			machOExec, _ = readBoundedZipFile(f, maxZIPScanEntryBytes)
		} else if strings.HasSuffix(lower, ".jsbundle") || strings.HasSuffix(lower, "main.jsbundle") || strings.HasSuffix(lower, "googleservice-info.plist") || strings.HasSuffix(lower, ".plist") && strings.HasPrefix(lower, strings.ToLower(appDir)) {
			if raw, err := readBoundedZipFile(f, maxZIPScanEntryBytes); err == nil {
				if !ExtractHermesStrings(raw, meta) {
					scanTextBufferForEndpoints(raw, meta)
					extractSBOMHints(raw, meta)
				}
			}
		}
	}
	if len(machOExec) > 0 {
		if enc, ok := CheckMachOFairPlayEncrypted(machOExec); ok {
			meta.FairPlayEncrypted = &enc
			if enc {
				meta.Warnings = appendUnique(meta.Warnings, "Mach-O binary has cryptid!=0 (FairPlay DRM encrypted); dump-decrypted required for class-dump/reversing")
			}
		}
		InspectMachOBinary(appDir+execName, machOExec, meta)
		scanTextBufferForEndpoints(machOExec, meta)
		extractSBOMHints(machOExec, meta)
	}
	provision := countedStage("provisioning", provisionRead, provisionPresent)
	if provisionLimited {
		provision.Status = "limited"
	}
	meta.Checks = append(meta.Checks, countedStage("info_plist", 1, 1), provision)
	binary := countedStage("binary_metadata", 0, 1)
	if len(meta.NativeBinaries) > 0 {
		binary.Checked = 1
		binary.Status = "complete"
	} else if execName == "" {
		binary.Status = "unknown"
		binary.Note = "清单未声明可执行文件"
	}
	if binaryLimited {
		binary.Status = "limited"
		binary.Note = "仅检查了文件前部"
	}
	meta.Checks = append(meta.Checks, binary)

	return nil
}

func decodePlistExecutableName(raw []byte) (string, error) {
	var root any
	var err error
	if bytes.HasPrefix(raw, []byte(bplistMagic)) {
		root, err = parseBinaryPlist(raw)
	} else {
		root, err = parseXMLPlist(raw)
	}
	if err != nil {
		return "", err
	}
	m, ok := root.(map[string]any)
	if !ok {
		return "", fmt.Errorf("not dict")
	}
	s, _ := m["CFBundleExecutable"].(string)
	return s, nil
}

func scanPackerAndFramework(lowerPath string, meta *PackageMetadata) {
	for _, p := range packerSignatures {
		if strings.Contains(lowerPath, p.pattern) {
			meta.Packers = appendUnique(meta.Packers, p.name)
		}
	}
	for _, fw := range frameworkSignatures {
		if strings.Contains(lowerPath, fw.pattern) {
			meta.Frameworks = appendUnique(meta.Frameworks, fw.name)
		}
	}
}

func extractABIFromPath(lowerPath string, meta *PackageMetadata) {
	for _, abi := range []string{"arm64-v8a", "armeabi-v7a", "armeabi", "x86_64", "x86"} {
		if strings.Contains(lowerPath, "lib/"+abi+"/") {
			meta.NativeABIs = appendUnique(meta.NativeABIs, abi)
			break
		}
	}
}

func inspectNetworkSecurityXML(raw []byte, meta *PackageMetadata) {
	// Both plain XML and AXML can live in res/xml/network_security_config.xml.
	if bytes.Contains(raw, []byte("certificates")) && bytes.Contains(raw, []byte("user")) {
		meta.NetworkSecTrustUser = true
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "certificates" {
			for _, a := range se.Attr {
				if a.Name.Local == "src" && a.Value == "user" {
					meta.NetworkSecTrustUser = true
				}
			}
		} else if se.Name.Local == "domain" {
			var b strings.Builder
			for {
				t2, e2 := dec.Token()
				if e2 != nil {
					break
				}
				if cd, ok := t2.(xml.CharData); ok {
					b.Write(cd)
				} else if _, ok := t2.(xml.EndElement); ok {
					break
				}
			}
			if host := sanitizeCandidateHost(b.String()); host != "" {
				meta.CandidateDomains = appendUnique(meta.CandidateDomains, host)
			}
		}
	}
}

func scanTextBufferForEndpoints(buf []byte, meta *PackageMetadata) {
	matches := urlRegex.FindAll(buf, 500)
	for _, m := range matches {
		rawURL := strings.TrimRight(string(m), ".,;\"'()[]{}")
		u, err := url.Parse(rawURL)
		if err != nil || u.Hostname() == "" {
			continue
		}
		host := sanitizeCandidateHost(u.Hostname())
		if host == "" {
			continue
		}
		if isThirdPartySDKHost(host) {
			meta.ThirdPartySDKHosts = appendUnique(meta.ThirdPartySDKHosts, host)
			continue
		}
		meta.CandidateDomains = appendUnique(meta.CandidateDomains, host)
		if len(meta.CandidateURLs) < maxURLsExtracted {
			meta.CandidateURLs = appendUnique(meta.CandidateURLs, rawURL)
		}
	}
	if keyRegex.Match(buf) && len(meta.HardcodedHints) < 20 {
		for _, m := range keyRegex.FindAll(buf, 5) {
			s := string(m)
			if len(s) > 4 {
				meta.HardcodedHints = appendUnique(meta.HardcodedHints, "matched potential key/secret pattern: "+s[:4]+"...")
			}
		}
	}
}

func sanitizeCandidateHost(raw string) string {
	h := strings.ToLower(strings.TrimSpace(raw))
	h = strings.TrimPrefix(h, "*.")
	if h == "" || strings.ContainsAny(h, " \t\n\r/\\") {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
			return ""
		}
		return ip.String()
	}
	if !strings.Contains(h, ".") || len(h) > 253 {
		return ""
	}
	for _, suf := range ignoredNoiseHostSuffixes {
		if h == suf || strings.HasSuffix(h, "."+suf) {
			return ""
		}
	}
	return h
}

func isThirdPartySDKHost(host string) bool {
	for _, suf := range thirdPartySDKHostSuffixes {
		if host == suf || strings.HasSuffix(host, "."+suf) {
			return true
		}
	}
	return false
}

func isSafeZipEntryName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(name))
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

func readBoundedZipFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	r := io.LimitReader(rc, limit+1)
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return b[:limit], nil
	}
	return b, nil
}

func SampleSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func appendUnique(slice []string, val string) []string {
	if val == "" {
		return slice
	}
	for _, item := range slice {
		if item == val {
			return slice
		}
	}
	return append(slice, val)
}
