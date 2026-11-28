package mobile

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

const (
	wxapkgMagicV1MMWX = "V1MMWX"
	wxapkgMacTag      = "WAPkgEncryptedTagForMac"
	wxapkgSalt        = "saltiest"
	wxapkgIV          = "the iv: 16 bytes"
	maxWxapkgRead     = 128 << 20
	maxWxapkgEntry    = 32 << 20
)

var (
	reWeChatAppID     = regexp.MustCompile(`wx[0-9a-f]{16}`)
	reWxCloudEnv      = regexp.MustCompile(`(?i)\benv\s*[:=]\s*["']([a-zA-Z0-9][a-zA-Z0-9_-]{4,40})["']`)
	reWxCloudFn       = regexp.MustCompile(`wx\.cloud\.callFunction\s*\(\s*\{[^}]{0,220}?\bname\s*:\s*["']([^"']{1,80})["']`)
	reWxCloudColl     = regexp.MustCompile(`\.collection\s*\(\s*["']([^"']{1,80})["']`)
	reWxAPIURL        = regexp.MustCompile(`wx\.(request|uploadFile|downloadFile|connectSocket)\s*\(\s*\{[^}]{0,400}?\burl\s*:\s*["']([^"']{4,300})["']`)
	reWxWebViewSrc    = regexp.MustCompile(`(?i)<web-view[^>]+src\s*=\s*["']([^"']+)["']`)
	reWxAppSecret     = regexp.MustCompile(`(?i)(?:app[_-]?secret|appsecret)["']?\s*[:=]\s*["']([0-9a-f]{32})["']`)
	reWxTencentMapKey = regexp.MustCompile(`\b[A-Z0-9]{5}-[A-Z0-9]{5}-[A-Z0-9]{5}-[A-Z0-9]{5}-[A-Z0-9]{5}-[A-Z0-9]{5}\b`)
	reWxCompiledTpl   = regexp.MustCompile(`batchAddCompiledTemplate|__wxCodeSpace__|setRuntimeGlobals`)
)

// WxapkgEntry describes one parsed entry inside a decrypted .wxapkg container.
type WxapkgEntry struct {
	Name      string
	AbsOffset int
	Size      int
}

// WxapkgArchive captures a validated .wxapkg container header and table of contents.
type WxapkgArchive struct {
	OffsetMode string // absolute | relative
	DataStart  int
	Entries    []WxapkgEntry
	Raw        []byte
}

// ExtractWeChatAppID finds a valid WeChat Mini Program AppID (`wx` + 16 lowercase hex chars)
// across candidate strings/paths while explicitly ignoring personal `wxid_...` IDs.
func ExtractWeChatAppID(candidates ...string) string {
	for _, c := range candidates {
		low := strings.ToLower(strings.TrimSpace(c))
		if low == "" {
			continue
		}
		// Scan path segments even when a sibling folder contains a personal `wxid_...` ID;
		// reWeChatAppID (`wx[0-9a-f]{16}`) only matches real Mini Program AppIDs.
		for _, m := range reWeChatAppID.FindAllString(low, -1) {
			if len(m) == 18 {
				return m
			}
		}
	}
	return ""
}

// DetectWxapkgKind classifies raw package bytes as plain, v1mmwx, mac_legacy, or unknown.
func DetectWxapkgKind(data []byte) string {
	if len(data) >= 6 && string(data[:6]) == wxapkgMagicV1MMWX {
		return "v1mmwx"
	}
	if len(data) >= 18 && data[0] == 0xBE && data[13] == 0xED {
		return "plain"
	}
	if len(data) >= len(wxapkgMacTag) && bytes.HasSuffix(data, []byte(wxapkgMacTag)) {
		return "mac_legacy"
	}
	return "unknown"
}

// HasWxapkgMagic reports whether a file on disk is a .wxapkg binary or a ZIP archive containing one.
func HasWxapkgMagic(path string) (kind string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	var head [32]byte
	n, _ := io.ReadFull(f, head[:])
	if n >= 6 && string(head[:6]) == wxapkgMagicV1MMWX {
		return "v1mmwx", true
	}
	if n >= 18 && head[0] == 0xBE && head[13] == 0xED {
		return "plain", true
	}
	if st, err := f.Stat(); err == nil && st.Size() >= int64(len(wxapkgMacTag)) {
		var tail [32]byte
		_, _ = f.Seek(-int64(len(wxapkgMacTag)), io.SeekEnd)
		if rn, _ := io.ReadFull(f, tail[:len(wxapkgMacTag)]); rn == len(wxapkgMacTag) && string(tail[:rn]) == wxapkgMacTag {
			return "mac_legacy", true
		}
	}
	if n >= 2 && head[0] == 'P' && head[1] == 'K' {
		if zr, err := zip.OpenReader(path); err == nil {
			defer zr.Close()
			for _, zf := range zr.File {
				if strings.HasSuffix(strings.ToLower(zf.Name), ".wxapkg") {
					return "zip_bundle", true
				}
			}
		}
	}
	return "", false
}

// DecryptV1MMWX decrypts a PC WeChat V1MMWX-encrypted .wxapkg using appID (`wx` + 16 hex).
func DecryptV1MMWX(data []byte, appID string) ([]byte, error) {
	appID = strings.TrimSpace(appID)
	if len(appID) < 2 {
		return nil, fmt.Errorf("V1MMWX decryption requires valid AppID (wx+16hex), not wxid")
	}
	if len(data) < 6+1024 {
		return nil, fmt.Errorf("V1MMWX package truncated (%d bytes)", len(data))
	}
	key := pbkdf2.Key([]byte(appID), []byte(wxapkgSalt), 1000, 32, sha1.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	firstBlock := make([]byte, 1024)
	copy(firstBlock, data[6:6+1024])
	cipher.NewCBCDecrypter(block, []byte(wxapkgIV)).CryptBlocks(firstBlock, firstBlock)

	head := firstBlock[:1023]
	xorKey := appID[len(appID)-2]
	rest := data[6+1024:]
	plain := make([]byte, len(head)+len(rest))
	copy(plain, head)
	for i, b := range rest {
		plain[len(head)+i] = b ^ xorKey
	}
	if len(plain) < 18 || plain[0] != 0xBE || plain[13] != 0xED {
		return nil, fmt.Errorf("V1MMWX decrypt failed: missing 0xBE/0xED header (wrong AppID %q?)", appID)
	}
	return plain, nil
}

// ParseWxapkg validates a decrypted .wxapkg container and returns sanitized entry metadata.
func ParseWxapkg(data []byte) (*WxapkgArchive, error) {
	if len(data) < 18 || data[0] != 0xBE || data[13] != 0xED {
		return nil, fmt.Errorf("invalid wxapkg magic (expected 0xBE/0xED)")
	}
	indexLen := int(binary.BigEndian.Uint32(data[5:9]))
	bodyLen := int(binary.BigEndian.Uint32(data[9:13]))
	size := len(data)
	if size != 14+indexLen+bodyLen && size != 18+indexLen+bodyLen {
		return nil, fmt.Errorf("inconsistent wxapkg lengths: size=%d index=%d body=%d", size, indexLen, bodyLen)
	}
	fileCount := int(binary.BigEndian.Uint32(data[14:18]))
	if fileCount <= 0 || fileCount > 100000 {
		return nil, fmt.Errorf("invalid wxapkg file count: %d", fileCount)
	}

	pos := 18
	entries := make([]WxapkgEntry, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		if pos+4 > size {
			return nil, fmt.Errorf("truncated wxapkg index (nameLen)")
		}
		nLen := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		pos += 4
		if nLen <= 0 || nLen > 1024 || pos+nLen+8 > size {
			return nil, fmt.Errorf("invalid wxapkg entry name length: %d", nLen)
		}
		rawName := string(data[pos : pos+nLen])
		pos += nLen
		cleanName, ok := sanitizeWxapkgEntryName(rawName)
		if !ok {
			return nil, fmt.Errorf("unsafe wxapkg path entry: %q", rawName)
		}
		off := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		sz := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		if sz < 0 || sz > maxWxapkgEntry {
			return nil, fmt.Errorf("wxapkg entry %s size out of bounds (%d)", cleanName, sz)
		}
		entries = append(entries, WxapkgEntry{Name: cleanName, AbsOffset: off, Size: sz})
	}

	var candidates []int
	if pos == 14+indexLen {
		candidates = append(candidates, 14+indexLen)
	}
	if pos == 18+indexLen {
		candidates = append(candidates, 18+indexLen)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("wxapkg index length mismatch: pos=%d indexLen=%d", pos, indexLen)
	}
	dataStart := candidates[0]
	mode := "absolute"
	if len(entries) > 0 && entries[0].AbsOffset != candidates[0] {
		isCand := false
		for _, c := range candidates {
			if entries[0].AbsOffset == c {
				isCand = true
				break
			}
		}
		if !isCand {
			if entries[0].AbsOffset+dataStart <= size {
				mode = "relative"
			} else {
				return nil, fmt.Errorf("wxapkg first entry offset %d out of bounds", entries[0].AbsOffset)
			}
		}
	}
	for i := range entries {
		base := 0
		if mode == "relative" {
			base = dataStart
		}
		absOff := base + entries[i].AbsOffset
		if absOff < 0 || absOff+entries[i].Size > size {
			return nil, fmt.Errorf("wxapkg entry %s data out of bounds (off=%d size=%d)", entries[i].Name, absOff, entries[i].Size)
		}
		entries[i].AbsOffset = absOff
	}
	return &WxapkgArchive{
		OffsetMode: mode,
		DataStart:  dataStart,
		Entries:    entries,
		Raw:        data,
	}, nil
}

// SafeExtractWxapkgData decrypts (when V1MMWX + appID present) and safely extracts entries into outDir.
func SafeExtractWxapkgData(raw []byte, appID, outDir string, limits ArchiveExtractLimits) (*ArchiveExtractReport, string, error) {
	if limits.MaxFiles <= 0 {
		limits = defaultArchiveLimits()
	}
	kind := DetectWxapkgKind(raw)
	plain := raw
	if kind == "v1mmwx" {
		if appID == "" {
			return nil, kind, fmt.Errorf("V1MMWX encrypted package requires AppID")
		}
		dec, err := DecryptV1MMWX(raw, appID)
		if err != nil {
			return nil, kind, err
		}
		plain = dec
	} else if kind != "plain" {
		return nil, kind, fmt.Errorf("unsupported wxapkg container kind %q", kind)
	}

	parsed, err := ParseWxapkg(plain)
	if err != nil {
		return nil, kind, err
	}
	if len(parsed.Entries) > limits.MaxFiles {
		return nil, kind, fmt.Errorf("wxapkg file count %d exceeds limit %d", len(parsed.Entries), limits.MaxFiles)
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return nil, kind, err
	}
	if err := os.MkdirAll(absOut, 0o755); err != nil {
		return nil, kind, err
	}
	rep := &ArchiveExtractReport{}
	for _, e := range parsed.Entries {
		if rep.TotalBytes+int64(e.Size) > limits.MaxTotalBytes {
			return nil, kind, fmt.Errorf("wxapkg total size exceeds %d bytes", limits.MaxTotalBytes)
		}
		target := filepath.Join(absOut, filepath.FromSlash(e.Name))
		rel, rerr := filepath.Rel(absOut, target)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			rep.SkippedFiles++
			rep.Warnings = append(rep.Warnings, "blocked unsafe wxapkg entry: "+e.Name)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, kind, err
		}
		if err := os.WriteFile(target, parsed.Raw[e.AbsOffset:e.AbsOffset+e.Size], 0o600); err != nil {
			return nil, kind, err
		}
		rep.ExtractedFiles++
		rep.TotalBytes += int64(e.Size)
	}
	return rep, kind, nil
}

func inspectMiniAppPackage(path, sha string, sizeBytes int64, hintAppID string) (*PackageMetadata, error) {
	ext := strings.ToLower(filepath.Ext(path))
	appID := ExtractWeChatAppID(hintAppID, path)
	meta := &PackageMetadata{
		SampleName:      filepath.Base(path),
		SamplePath:      path,
		SHA256:          sha,
		SizeBytes:       sizeBytes,
		Platform:        PlatformMiniApp,
		FileType:        strings.TrimPrefix(ext, "."),
		PackageName:     appID,
		InspectedAtUnix: time.Now().Unix(),
		MiniApp: &MiniAppSummary{
			AppID: appID,
		},
	}

	var pkgs []struct {
		name string
		data []byte
	}
	if ext == ".zip" {
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		meta.MiniApp.ContainerType = "zip_bundle"
		for _, zf := range zr.File {
			if !isSafeZipEntryName(zf.Name) {
				meta.Warnings = appendUnique(meta.Warnings, "archive contains unsafe entry: "+zf.Name)
				continue
			}
			if strings.HasSuffix(strings.ToLower(zf.Name), ".wxapkg") {
				if meta.MiniApp.AppID == "" {
					if inferred := ExtractWeChatAppID(zf.Name); inferred != "" {
						meta.MiniApp.AppID = inferred
						meta.PackageName = inferred
					}
				}
				raw, rerr := readBoundedZipFile(zf, maxWxapkgRead)
				if rerr == nil && len(raw) > 0 {
					pkgs = append(pkgs, struct {
						name string
						data []byte
					}{name: zf.Name, data: raw})
				}
			}
		}
	} else {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) > maxWxapkgRead {
			return nil, fmt.Errorf("wxapkg exceeds size limit (%d bytes)", maxWxapkgRead)
		}
		pkgs = append(pkgs, struct {
			name string
			data []byte
		}{name: filepath.Base(path), data: raw})
	}

	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no .wxapkg payload found in %s", filepath.Base(path))
	}
	meta.MiniApp.PackageCount = len(pkgs)
	var bufs []NamedBuffer
	for _, pk := range pkgs {
		kind := DetectWxapkgKind(pk.data)
		if meta.MiniApp.ContainerType == "" {
			meta.MiniApp.ContainerType = kind
		}
		plain := pk.data
		switch kind {
		case "v1mmwx":
			meta.MiniApp.EncryptionDetected = true
			if meta.MiniApp.AppID == "" {
				meta.Warnings = appendUnique(meta.Warnings, "检测到 V1MMWX 加密外壳但未提供或推断出 AppID（wx+16位hex），请填写 AppID 后重新审计")
				continue
			}
			dec, derr := DecryptV1MMWX(pk.data, meta.MiniApp.AppID)
			if derr != nil {
				meta.Warnings = appendUnique(meta.Warnings, derr.Error())
				continue
			}
			plain = dec
		case "mac_legacy":
			meta.MiniApp.EncryptionDetected = true
			meta.Warnings = appendUnique(meta.Warnings, "检测到 macOS <3.8 旧版加密 wxapkg，需通过 Frida 提取解密密钥")
			continue
		case "unknown":
			meta.Warnings = appendUnique(meta.Warnings, "无法识别的 wxapkg 容器魔数: "+pk.name)
			continue
		}
		parsed, perr := ParseWxapkg(plain)
		if perr != nil {
			meta.Warnings = appendUnique(meta.Warnings, perr.Error())
			continue
		}
		meta.MiniApp.FileCount += len(parsed.Entries)
		for _, e := range parsed.Entries {
			low := strings.ToLower(e.Name)
			if !strings.HasSuffix(low, ".js") && !strings.HasSuffix(low, ".json") && !strings.HasSuffix(low, ".wxml") && !strings.HasSuffix(low, ".wxs") && !strings.HasSuffix(low, ".html") {
				continue
			}
			body := parsed.Raw[e.AbsOffset : e.AbsOffset+e.Size]
			bufs = append(bufs, NamedBuffer{Name: e.Name, Data: body})
		}
	}

	InspectMiniAppBuffers(bufs, meta)
	if meta.PackageName == "" {
		meta.PackageName = "miniapp_unknown"
	}
	return meta, nil
}

// InspectMiniAppBuffers extracts Mini Program routes, CloudBase identifiers, API URLs,
// and high-value secrets from decrypted JS/JSON/WXML buffers into meta.
func InspectMiniAppBuffers(bufs []NamedBuffer, meta *PackageMetadata) {
	if meta == nil {
		return
	}
	if meta.MiniApp == nil {
		meta.MiniApp = &MiniAppSummary{AppID: meta.PackageName}
	}
	seenFinding := map[string]bool{}
	addFinding := func(sf StaticFinding) {
		if seenFinding[sf.ID+":"+sf.Location] {
			return
		}
		seenFinding[sf.ID+":"+sf.Location] = true
		meta.StaticFindings = append(meta.StaticFindings, sf)
	}

	for _, b := range bufs {
		low := strings.ToLower(filepath.Base(b.Name))
		txt := string(b.Data)
		if low == "app.json" || low == "app-config.json" {
			var cfg struct {
				Pages       []string `json:"pages"`
				SubPackages []struct {
					Root  string   `json:"root"`
					Pages []string `json:"pages"`
				} `json:"subPackages"`
				SubpackagesAlt []struct {
					Root  string   `json:"root"`
					Pages []string `json:"pages"`
				} `json:"subpackages"`
				Plugins map[string]any `json:"plugins"`
			}
			if json.Unmarshal(b.Data, &cfg) == nil {
				for _, p := range cfg.Pages {
					meta.MiniApp.Routes = appendUniqueCap(meta.MiniApp.Routes, p, 300)
				}
				for _, sub := range append(cfg.SubPackages, cfg.SubpackagesAlt...) {
					root := strings.Trim(sub.Root, "/")
					if root != "" {
						meta.MiniApp.SubPackages = appendUniqueCap(meta.MiniApp.SubPackages, root, 64)
					}
					for _, p := range sub.Pages {
						full := strings.Trim(p, "/")
						if root != "" {
							full = root + "/" + full
						}
						meta.MiniApp.Routes = appendUniqueCap(meta.MiniApp.Routes, full, 300)
					}
				}
				for k := range cfg.Plugins {
					meta.MiniApp.Plugins = appendUniqueCap(meta.MiniApp.Plugins, k, 64)
				}
			}
		}

		// Inferred AppID from code if missing
		if meta.MiniApp.AppID == "" {
			if inferred := ExtractWeChatAppID(txt); inferred != "" {
				meta.MiniApp.AppID = inferred
				if meta.PackageName == "" {
					meta.PackageName = inferred
				}
			}
		}

		scanTextBufferForEndpoints(b.Data, meta)
		for _, m := range reWxAPIURL.FindAllStringSubmatch(txt, 25) {
			uStr := m[2]
			meta.CandidateURLs = appendUniqueCap(meta.CandidateURLs, uStr, maxURLsExtracted)
			if pu, perr := url.Parse(uStr); perr == nil && pu.Hostname() != "" {
				if h := sanitizeCandidateHost(pu.Hostname()); h != "" {
					meta.CandidateDomains = appendUniqueCap(meta.CandidateDomains, h, maxDomainsExtracted)
				}
			}
		}
		for _, m := range reWxWebViewSrc.FindAllStringSubmatch(txt, 10) {
			meta.CandidateURLs = appendUniqueCap(meta.CandidateURLs, m[1], maxURLsExtracted)
			addFinding(StaticFinding{
				ID: "miniapp-webview-container", Category: "platform", Severity: "medium", MASVS: "MASVS-PLATFORM-2",
				Title:    "发现小程序 web-view 外部加载容器（需验证业务域名校验与传参拼接）",
				Location: b.Name, Evidence: m[1],
				Recommendation: "检查 <web-view src> 是否接收可控页面参数及 postMessage 来源域名验证",
			})
		}
		for _, m := range reWxCloudEnv.FindAllStringSubmatch(txt, 10) {
			meta.MiniApp.CloudEnvs = appendUniqueCap(meta.MiniApp.CloudEnvs, m[1], 32)
			addFinding(StaticFinding{
				ID: "miniapp-cloudbase-env", Category: "network", Severity: "info", MASVS: "MASVS-STORAGE-1",
				Title: "检测到微信云开发 CloudBase 环境 ID", Location: b.Name, Evidence: "env=" + m[1],
				Recommendation: "审计对应云函数权限、数据库安全规则 (read/write) 及云存储访问控制",
			})
		}
		for _, m := range reWxCloudFn.FindAllStringSubmatch(txt, 25) {
			meta.MiniApp.CloudFunctions = appendUniqueCap(meta.MiniApp.CloudFunctions, m[1], 100)
		}
		for _, m := range reWxCloudColl.FindAllStringSubmatch(txt, 25) {
			meta.MiniApp.CloudCollections = appendUniqueCap(meta.MiniApp.CloudCollections, m[1], 100)
		}
		for _, m := range reWxAppSecret.FindAllStringSubmatch(txt, 5) {
			masked := maskSecretPreview(m[1])
			meta.HardcodedHints = appendUniqueCap(meta.HardcodedHints, masked, 32)
			addFinding(StaticFinding{
				ID: "miniapp-hardcoded-appsecret", Category: "crypto", Severity: "high", MASVS: "MASVS-CRYPTO-1",
				Title: "源码疑似硬编码微信小程序 AppSecret", Location: b.Name, Evidence: masked,
				Recommendation: "核实该 AppSecret 能否兑换 access_token 并严禁硬编码于前端包内",
			})
		}
		for _, mk := range reWxTencentMapKey.FindAllString(txt, 5) {
			meta.MiniApp.MapKeys = appendUniqueCap(meta.MiniApp.MapKeys, maskSecretPreview(mk), 20)
		}
		if reWxCompiledTpl.MatchString(txt) {
			meta.MiniApp.HasCompiledTpl = true
		}
	}

	if meta.MiniApp.EncryptionDetected && meta.MiniApp.FileCount == 0 {
		meta.Frameworks = appendUnique(meta.Frameworks, "WeChat MiniProgram (Encrypted)")
	} else {
		meta.Frameworks = appendUnique(meta.Frameworks, "WeChat MiniProgram")
	}
	if len(meta.MiniApp.CloudEnvs) > 0 || len(meta.MiniApp.CloudFunctions) > 0 {
		meta.Frameworks = appendUnique(meta.Frameworks, "CloudBase")
	}
}

func appendUniqueCap(slice []string, val string, maxCap int) []string {
	val = strings.TrimSpace(val)
	if val == "" || len(slice) >= maxCap {
		return slice
	}
	return appendUnique(slice, val)
}

func sanitizeWxapkgEntryName(raw string) (string, bool) {
	if raw == "" || strings.ContainsRune(raw, '\x00') {
		return "", false
	}
	norm := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(norm, "//") || (len(norm) >= 2 && norm[1] == ':') {
		return "", false
	}
	norm = strings.TrimLeft(norm, "/")
	if norm == "" {
		return "", false
	}
	parts := strings.Split(norm, "/")
	cleaned := make([]string, 0, len(parts))
	for _, seg := range parts {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." || len(seg) > 255 {
			return "", false
		}
		for _, r := range seg {
			if r < 32 {
				return "", false
			}
		}
		cleaned = append(cleaned, seg)
	}
	if len(cleaned) == 0 {
		return "", false
	}
	rel := strings.Join(cleaned, "/")
	if len(rel) > 1024 {
		return "", false
	}
	return rel, true
}
