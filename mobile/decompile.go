package mobile

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ArchiveExtractLimits bounds safe extraction against Zip-Slip and Zip Bomb packages.
type ArchiveExtractLimits struct {
	MaxFiles      int
	MaxEntryBytes int64
	MaxTotalBytes int64
}

func defaultArchiveLimits() ArchiveExtractLimits {
	return ArchiveExtractLimits{
		MaxFiles:      4000,
		MaxEntryBytes: 32 << 20,  // 32 MiB per file
		MaxTotalBytes: 512 << 20, // 512 MiB total unpacked
	}
}

// ArchiveExtractReport records extracted entry counts and skipped entries.
type ArchiveExtractReport struct {
	ExtractedFiles int      `json:"extracted_files"`
	SkippedFiles   int      `json:"skipped_files"`
	TotalBytes     int64    `json:"total_bytes"`
	NestedAPKs     []string `json:"nested_apks,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// DecompileOptions configures safe APK/IPA/MiniProgram extraction, external decompilers, and directory whitebox scanning.
type DecompileOptions struct {
	Mode          string // auto | fast | deep
	SkipApktool   bool
	HintAppID     string // Optional WeChat AppID (wx+16hex) for V1MMWX decryption
	JadxTimeout   time.Duration
	CommandRunner ADBRunner // Optional override for deterministic unit testing
}

// DecompiledEndpoint represents an extracted HTTP/API route or annotation with exact file:line attribution.
type DecompiledEndpoint struct {
	Method string `json:"method,omitempty"`
	URL    string `json:"url"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// DecompiledSecret represents a masked secret finding extracted from decompiled source or assets.
type DecompiledSecret struct {
	Kind    string  `json:"kind"`
	Masked  string  `json:"masked"`
	Entropy float64 `json:"entropy,omitempty"`
	File    string  `json:"file"`
	Line    int     `json:"line"`
	Snippet string  `json:"snippet,omitempty"`
}

// DecompiledCodeRisk represents a static semantic/whitebox risk found in decompiled mobile source.
type DecompiledCodeRisk struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	MASVS    string `json:"masvs,omitempty"`
	Title    string `json:"title"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Snippet  string `json:"snippet"`
}

// DecompiledSignCandidate locates probable request signing, encryption, or pinning implementations.
type DecompiledSignCandidate struct {
	Hint    string `json:"hint"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

// DecompileAuditReport captures full extraction, decompiler quality gate, and directory whitebox results.
type DecompileAuditReport struct {
	PackageName    string                    `json:"package_name,omitempty"`
	Platform       Platform                  `json:"platform,omitempty"`
	SamplePath     string                    `json:"sample_path"`
	SampleSHA256   string                    `json:"sample_sha256,omitempty"`
	QualityStatus  string                    `json:"quality_status"` // full | partial_packed | raw_only
	ToolsUsed      []string                  `json:"tools_used,omitempty"`
	DecompiledDir  string                    `json:"decompiled_dir"`
	AuditDir       string                    `json:"audit_dir"`
	JavaFileCount  int                       `json:"java_file_count"`
	SmaliFileCount int                       `json:"smali_file_count"`
	ScannedFiles   int                       `json:"scanned_files"`
	Extract        ArchiveExtractReport      `json:"extract"`
	ManifestMeta   *PackageMetadata          `json:"manifest_meta,omitempty"`
	Endpoints      []DecompiledEndpoint      `json:"endpoints,omitempty"`
	Secrets        []DecompiledSecret        `json:"secrets,omitempty"`
	SignCandidates []DecompiledSignCandidate `json:"sign_candidates,omitempty"`
	Risks          []DecompiledCodeRisk      `json:"risks,omitempty"`
	Warnings       []string                  `json:"warnings,omitempty"`
	GeneratedAt    string                    `json:"generated_at"`
}

var (
	reRetrofitAnno = regexp.MustCompile(`@(GET|POST|PUT|DELETE|PATCH|HTTP)\(\s*"([^"\r\n]{1,180})"`)
	reRoutePathLit = regexp.MustCompile(`(?i)["'](/(?:api|v\d+|app|mobile|auth|user|order|pay|admin|graphql)[a-zA-Z0-9_./{}-]{1,120})["']`)
	reSecretAssign = regexp.MustCompile(`(?i)(?:app_?secret|api_?secret|client_?secret|secret_?key|aes_?key|jwt_?secret|oss_?access_?key|access_?key_?id|private_?key|signing_?key)["']?\s*[:=]\s*["']([A-Za-z0-9+/=_-]{8,128})["']`)
	rePEMBlock     = regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |PRIVATE )?PRIVATE KEY-----`)
	reAKSKToken    = regexp.MustCompile(`\b(?:AKIA[0-9A-Z]{16}|LTAI[0-9A-Za-z]{12,24}|sk_live_[0-9a-zA-Z]{16,}|ghp_[0-9a-zA-Z]{24,}|AIza[0-9A-Za-z\-_]{35}|eyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{8,})\b`)
	reHighEntCand  = regexp.MustCompile(`(?i)(?:key|secret|token|salt|sign|hmac|cipher|password|passwd|auth)["']?\s*[:=,]\s*["']([A-Za-z0-9+/=_-]{20,96})["']`)
	reSignKeyword  = regexp.MustCompile(`(?i)(?:Interceptor\b|CertificatePinner|getSign|calcSign|signature|HmacSHA|MessageDigest\.getInstance|Cipher\.getInstance|SecretKeySpec|IvParameterSpec|CryptoJS\b|wx\.requestPayment\b|\bmd5\s*\(|\bsha(?:1|256)\s*\(|\bhmac\b)`)
)

// SafeExtractMobileArchive extracts APK/XAPK/APKM/APKS/AAB/IPA entries safely into outDir,
// blocking path traversal (..), absolute paths, symlinks, and zip-bomb resource exhaustion.
func SafeExtractMobileArchive(pkgPath, outDir string, limits ArchiveExtractLimits) (*ArchiveExtractReport, error) {
	if limits.MaxFiles <= 0 {
		limits = defaultArchiveLimits()
	}
	st, err := os.Stat(pkgPath)
	if err != nil {
		return nil, fmt.Errorf("stat archive: %w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("archive path is a directory: %s", pkgPath)
	}
	baseAbs, err := filepath.Abs(outDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(baseAbs, 0o755); err != nil {
		return nil, err
	}
	zr, err := zip.OpenReader(pkgPath)
	if err != nil {
		return nil, fmt.Errorf("open zip archive: %w", err)
	}
	defer zr.Close()

	rep := &ArchiveExtractReport{}
	var nestedQueue []string
	for _, f := range zr.File {
		if rep.ExtractedFiles >= limits.MaxFiles {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("Archive extraction reached max file cap (%d); remaining entries skipped", limits.MaxFiles))
			break
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			rep.SkippedFiles++
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("Rejected symlink entry: %s", f.Name))
			continue
		}
		cleanRel, ok := safeArchiveEntryRel(f.Name)
		if !ok {
			rep.SkippedFiles++
			return nil, fmt.Errorf("unsafe archive entry path rejected (path traversal guard): %q", f.Name)
		}
		dst := filepath.Join(baseAbs, cleanRel)
		relCheck, relErr := filepath.Rel(baseAbs, dst)
		if relErr != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) || filepath.IsAbs(relCheck) {
			return nil, fmt.Errorf("escaped target path rejected: %q", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		rc, err := f.Open()
		if err != nil {
			rep.SkippedFiles++
			continue
		}
		outF, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			_ = rc.Close()
			return nil, err
		}
		n, copyErr := io.Copy(outF, io.LimitReader(rc, limits.MaxEntryBytes+1))
		_ = outF.Close()
		_ = rc.Close()
		if copyErr != nil {
			_ = os.Remove(dst)
			rep.SkippedFiles++
			continue
		}
		if n > limits.MaxEntryBytes {
			_ = os.Remove(dst)
			rep.SkippedFiles++
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("Skipped oversized entry (>%d bytes): %s", limits.MaxEntryBytes, cleanRel))
			continue
		}
		if rep.TotalBytes+n > limits.MaxTotalBytes {
			_ = os.Remove(dst)
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("Stopped extraction at %d byte limit", limits.MaxTotalBytes))
			break
		}
		rep.TotalBytes += n
		rep.ExtractedFiles++

		if strings.HasSuffix(strings.ToLower(cleanRel), ".apk") {
			rep.NestedAPKs = append(rep.NestedAPKs, cleanRel)
			if strings.EqualFold(filepath.Base(cleanRel), "base.apk") || len(nestedQueue) == 0 {
				nestedQueue = append(nestedQueue, dst)
			}
		}
	}

	// Unpack primary nested base.apk for .xapk/.apkm/.apks bundles into raw/split_base/.
	if len(nestedQueue) > 0 && rep.TotalBytes < limits.MaxTotalBytes {
		primaryNested := nestedQueue[0]
		for _, p := range nestedQueue {
			if strings.EqualFold(filepath.Base(p), "base.apk") {
				primaryNested = p
				break
			}
		}
		subLimits := limits
		subLimits.MaxTotalBytes = limits.MaxTotalBytes - rep.TotalBytes
		subLimits.MaxFiles = limits.MaxFiles - rep.ExtractedFiles
		if subLimits.MaxFiles > 0 && subLimits.MaxTotalBytes > 0 {
			subRep, subErr := SafeExtractMobileArchive(primaryNested, filepath.Join(baseAbs, "split_base"), subLimits)
			if subErr != nil {
				return nil, subErr
			}
			rep.ExtractedFiles += subRep.ExtractedFiles
			rep.SkippedFiles += subRep.SkippedFiles
			rep.TotalBytes += subRep.TotalBytes
			rep.Warnings = append(rep.Warnings, subRep.Warnings...)
		}
	}
	return rep, nil
}

func safeArchiveEntryRel(raw string) (string, bool) {
	if raw == "" || strings.ContainsRune(raw, '\x00') {
		return "", false
	}
	norm := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(norm, "/") || (len(norm) >= 2 && norm[1] == ':') {
		return "", false
	}
	parts := strings.Split(norm, "/")
	cleaned := make([]string, 0, len(parts))
	for _, seg := range parts {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." {
			return "", false
		}
		cleaned = append(cleaned, seg)
	}
	if len(cleaned) == 0 {
		return "", false
	}
	return filepath.Join(cleaned...), true
}

// DecompileAndAudit performs safe extraction, invokes available external decompilers (jadx/apktool/hermes-dec)
// strictly bounded inside taskRoot, runs directory-level whitebox SAST scanning, and persists audit artifacts
// into <taskRoot>/mobile_audit/.
func DecompileAndAudit(ctx context.Context, pkgPath, taskRoot string, opts DecompileOptions) (*DecompileAuditReport, error) {
	absRoot, err := filepath.Abs(taskRoot)
	if err != nil {
		return nil, err
	}
	absPkg, err := filepath.Abs(pkgPath)
	if err != nil {
		return nil, err
	}
	relPkg, err := filepath.Rel(absRoot, absPkg)
	if err != nil || relPkg == ".." || strings.HasPrefix(relPkg, ".."+string(filepath.Separator)) || filepath.IsAbs(relPkg) {
		return nil, fmt.Errorf("sample path must reside inside task workspace (%s)", taskRoot)
	}
	if opts.JadxTimeout <= 0 {
		opts.JadxTimeout = 120 * time.Second
	}
	runner := opts.CommandRunner
	if runner == nil {
		runner = defaultCommandRunner
	}

	meta, err := InspectPackageWithHint(absPkg, opts.HintAppID)
	if err != nil {
		return nil, err
	}

	decompiledRoot := filepath.Join(absRoot, "samples", "decompiled")
	rawOut := filepath.Join(decompiledRoot, "raw")
	jadxOut := filepath.Join(decompiledRoot, "jadx")
	apktoolOut := filepath.Join(decompiledRoot, "apktool")
	auditOut := filepath.Join(absRoot, "mobile_audit")
	for _, d := range []string{decompiledRoot, rawOut, auditOut} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	var extRep *ArchiveExtractReport
	if meta.Platform == PlatformMiniApp {
		extRep, err = extractMiniAppSample(absPkg, rawOut, meta.MiniApp)
	} else {
		extRep, err = SafeExtractMobileArchive(absPkg, rawOut, defaultArchiveLimits())
	}
	if err != nil {
		return nil, err
	}

	rep := &DecompileAuditReport{
		PackageName:   meta.PackageName,
		Platform:      meta.Platform,
		SamplePath:    filepath.ToSlash(relPkg),
		SampleSHA256:  meta.SHA256,
		QualityStatus: "raw_only",
		DecompiledDir: filepath.ToSlash(filepath.Join("samples", "decompiled")),
		AuditDir:      "mobile_audit",
		Extract:       *extRep,
		ManifestMeta:  meta,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if rawSHA, err := fileSHA256(absPkg); err == nil && rep.SampleSHA256 == "" {
		rep.SampleSHA256 = rawSHA
	}

	if meta.Platform == PlatformAndroid || strings.HasSuffix(strings.ToLower(absPkg), ".apk") {
		apkTarget := absPkg
		// If split archive extracted nested base.apk, point jadx/apktool at base.apk for clean Java recovery.
		for _, nested := range extRep.NestedAPKs {
			if strings.EqualFold(filepath.Base(nested), "base.apk") {
				cand := filepath.Join(rawOut, filepath.FromSlash(nested))
				if st, serr := os.Stat(cand); serr == nil && !st.IsDir() {
					apkTarget = cand
				}
				break
			}
		}

		// Invoke jadx if available (never trust exit code 0 alone; validate generated files below).
		if jadxBin, lerr := lookToolPath("jadx", opts.CommandRunner != nil); lerr == nil {
			rep.ToolsUsed = append(rep.ToolsUsed, "jadx")
			jCtx, cancel := context.WithTimeout(ctx, opts.JadxTimeout)
			_, jErrOut, jCode, jErr := runner(jCtx, []string{"JAVA_OPTS=-Xmx2g"}, jadxBin, "-d", jadxOut, "--no-debug-info", "--show-bad-code", "-j", "2", apkTarget)
			cancel()
			if jErr != nil || jCode != 0 {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("jadx exited with code %d (%s); evaluating partial output", jCode, clipDecompileMsg(jErrOut)))
			}
		} else {
			rep.Warnings = append(rep.Warnings, "jadx binary not available in PATH; running whitebox scan over safe-extracted DEX/XML/assets only")
		}

		if !opts.SkipApktool && !strings.EqualFold(strings.TrimSpace(opts.Mode), "fast") {
			if apktoolBin, lerr := lookToolPath("apktool", opts.CommandRunner != nil); lerr == nil {
				rep.ToolsUsed = append(rep.ToolsUsed, "apktool")
				aCtx, cancel := context.WithTimeout(ctx, opts.JadxTimeout)
				_, aErrOut, aCode, aErr := runner(aCtx, nil, apktoolBin, "d", "-f", "-o", apktoolOut, apkTarget)
				cancel()
				if aErr != nil || aCode != 0 {
					rep.Warnings = append(rep.Warnings, fmt.Sprintf("apktool exited with code %d (%s)", aCode, clipDecompileMsg(aErrOut)))
				}
			}
		}
	}

	// Optional Hermes bytecode decompilation if detected in extracted assets.
	if meta.HermesBytecode {
		bundlePath := findExtractedFile(rawOut, "index.android.bundle", "main.jsbundle")
		if bundlePath != "" {
			if hbcBin, lerr := lookToolPath("hbc-decompiler", opts.CommandRunner != nil); lerr == nil {
				rep.ToolsUsed = append(rep.ToolsUsed, "hbc-decompiler")
				hCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
				_, hErrOut, hCode, hErr := runner(hCtx, nil, hbcBin, bundlePath, filepath.Join(decompiledRoot, "hermes_dec.js"))
				cancel()
				if hErr != nil || hCode != 0 {
					rep.Warnings = append(rep.Warnings, fmt.Sprintf("hbc-decompiler exited %d (%s)", hCode, clipDecompileMsg(hErrOut)))
				}
			}
		}
	}

	if err := auditDecompiledDirectory(decompiledRoot, rep); err != nil {
		return nil, err
	}

	// Output quality gate (never judge success solely by tool exit codes).
	switch {
	case meta.Platform == PlatformMiniApp:
		if meta.MiniApp != nil && meta.MiniApp.EncryptionDetected && rep.Extract.ExtractedFiles == 0 {
			rep.QualityStatus = "partial_packed"
			rep.Warnings = append(rep.Warnings, "小程序包存在加密且未能完整提取；请确认 AppID 或使用动态 Hook")
		} else if meta.MiniApp != nil && meta.MiniApp.HasCompiledTpl {
			rep.QualityStatus = "partial_packed"
			rep.Warnings = append(rep.Warnings, "检测到 __wxCodeSpace__/batchAddCompiledTemplate 新编译模板，部分 WXML 结构可能不完整")
		} else if rep.Extract.ExtractedFiles > 0 && (meta.MiniApp != nil && len(meta.MiniApp.Routes) > 0) {
			rep.QualityStatus = "full"
		} else if rep.Extract.ExtractedFiles > 0 {
			rep.QualityStatus = "partial_packed"
		} else {
			rep.QualityStatus = "raw_only"
		}
	case rep.JavaFileCount >= 10 || rep.SmaliFileCount >= 10:
		rep.QualityStatus = "full"
	case len(meta.Packers) > 0 && rep.JavaFileCount < 10:
		rep.QualityStatus = "partial_packed"
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("Detected packer (%s) and low recovered source count (java=%d, smali=%d); prioritize Frida Hook / native .so inspection", strings.Join(meta.Packers, ","), rep.JavaFileCount, rep.SmaliFileCount))
	case rep.JavaFileCount > 0 || rep.SmaliFileCount > 0:
		rep.QualityStatus = "partial_packed"
	default:
		rep.QualityStatus = "raw_only"
	}

	if err := writeDecompileAuditArtifacts(auditOut, rep); err != nil {
		return nil, err
	}
	return rep, nil
}

func lookToolPath(name string, allowMock bool) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	if allowMock {
		return name, nil
	}
	return "", exec.ErrNotFound
}

func clipDecompileMsg(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

func findExtractedFile(root string, names ...string) string {
	want := map[string]bool{}
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	var found string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if want[strings.ToLower(d.Name())] {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	return found
}

var whiteboxSourceExts = map[string]bool{
	".java": true, ".kt": true, ".smali": true, ".js": true, ".jsx": true,
	".ts": true, ".tsx": true, ".mjs": true, ".json": true, ".xml": true,
	".html": true, ".htm": true, ".plist": true, ".yaml": true, ".yml": true,
	".properties": true, ".dart": true, ".wxml": true, ".wxss": true, ".wxs": true,
	".map": true,
}

type lineRiskPattern struct {
	id, category, severity, masvs, title string
	re                                   *regexp.Regexp
}

var whiteboxLineRisks = []lineRiskPattern{
	{
		id: "webview-js-bridge", category: "platform", severity: "high", masvs: "MASVS-PLATFORM-2",
		title: "WebView 暴露 addJavascriptInterface 原生桥接面",
		re:    regexp.MustCompile(`\baddJavascriptInterface\s*\(`),
	},
	{
		id: "webview-file-universal-access", category: "platform", severity: "high", masvs: "MASVS-PLATFORM-2",
		title: "WebView 允许 file:// 域访问敏感本地文件",
		re:    regexp.MustCompile(`\bsetAllow(?:UniversalAccessFromFileURLs|FileAccessFromFileURLs)\s*\(\s*true\s*\)`),
	},
	{
		id: "tls-trust-all-certs", category: "network", severity: "high", masvs: "MASVS-NETWORK-1",
		title: "自定义 SSL 校验空实现或放行所有证书/错误（TrustAll / onReceivedSslError.proceed）",
		re:    regexp.MustCompile(`(?:\bALLOW_ALL_HOSTNAME_VERIFIER\b|\bNullHostNameVerifier\b|\.proceed\s*\(\s*\)|X509TrustManager\b)`),
	},
	{
		id: "crypto-weak-ecb-des", category: "crypto", severity: "medium", masvs: "MASVS-CRYPTO-1",
		title: "使用不安全的加密模式或弱算法（AES/ECB、DES、MD5 口令散列）",
		re:    regexp.MustCompile(`Cipher\.getInstance\s*\(\s*["'](?:AES(?:/ECB)?|DES|DESede|RC4)["']`),
	},
	{
		id: "crypto-weak-prng", category: "crypto", severity: "low", masvs: "MASVS-CRYPTO-2",
		title: "安全敏感逻辑疑似使用非密码学随机数 java.util.Random",
		re:    regexp.MustCompile(`\bnew\s+(?:java\.util\.)?Random\s*\(`),
	},
	{
		id: "sqlite-rawquery-concat", category: "storage", severity: "medium", masvs: "MASVS-CODE-4",
		title: "SQLite rawQuery/execSQL 存在字符串拼接注入隐患",
		re:    regexp.MustCompile(`\b(?:rawQuery|execSQL)\s*\([^"\r\n]*\+`),
	},
	{
		id: "miniapp-cloud-call", category: "network", severity: "info", masvs: "MASVS-STORAGE-1",
		title: "小程序调用 CloudBase 云函数/数据库接口",
		re:    regexp.MustCompile(`wx\.cloud\.(?:callFunction|database)`),
	},
	{
		id: "miniapp-webview-bridge", category: "platform", severity: "medium", masvs: "MASVS-PLATFORM-2",
		title: "小程序使用 web-view 加载 H5 页面，需核查 URL 白名单与 JSSDK 通信",
		re:    regexp.MustCompile(`(?i)<web-view[^>]+src=`),
	},
}

func extractMiniAppSample(absPkg, rawOut string, summary *MiniAppSummary) (*ArchiveExtractReport, error) {
	appID := ""
	if summary != nil {
		appID = summary.AppID
	}
	ext := strings.ToLower(filepath.Ext(absPkg))
	if ext != ".zip" {
		raw, err := os.ReadFile(absPkg)
		if err != nil {
			return nil, err
		}
		rep, _, err := SafeExtractWxapkgData(raw, appID, rawOut, defaultArchiveLimits())
		if err != nil {
			// If encrypted with unknown AppID, return clean partial extraction report with warning
			return &ArchiveExtractReport{Warnings: []string{err.Error()}}, nil
		}
		return rep, nil
	}
	zr, err := zip.OpenReader(absPkg)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	agg := &ArchiveExtractReport{}
	for _, zf := range zr.File {
		if !isSafeZipEntryName(zf.Name) || !strings.HasSuffix(strings.ToLower(zf.Name), ".wxapkg") {
			continue
		}
		raw, rerr := readBoundedZipFile(zf, maxWxapkgRead)
		if rerr != nil || len(raw) == 0 {
			continue
		}
		subID := appID
		if subID == "" {
			subID = ExtractWeChatAppID(zf.Name)
		}
		pkgSubDir := filepath.Join(rawOut, strings.TrimSuffix(filepath.Base(zf.Name), filepath.Ext(zf.Name)))
		rep, _, derr := SafeExtractWxapkgData(raw, subID, pkgSubDir, defaultArchiveLimits())
		if derr != nil {
			agg.Warnings = append(agg.Warnings, zf.Name+": "+derr.Error())
			continue
		}
		agg.ExtractedFiles += rep.ExtractedFiles
		agg.SkippedFiles += rep.SkippedFiles
		agg.TotalBytes += rep.TotalBytes
		agg.Warnings = append(agg.Warnings, rep.Warnings...)
	}
	return agg, nil
}

func auditDecompiledDirectory(decompiledRoot string, rep *DecompileAuditReport) error {
	const (
		maxScanFiles     = 5000
		maxFileReadBytes = 256 << 10
		maxTotalRead     = 64 << 20
		maxEndpoints     = 200
		maxSecrets       = 120
		maxSignCands     = 80
		maxRisks         = 120
	)

	seenEndpoint := map[string]bool{}
	seenSecret := map[string]bool{}
	seenRisk := map[string]bool{}
	var totalBytes int64

	err := filepath.WalkDir(decompiledRoot, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext == ".java" || ext == ".kt" {
			rep.JavaFileCount++
		} else if ext == ".smali" {
			rep.SmaliFileCount++
		}
		if !whiteboxSourceExts[ext] {
			return nil
		}
		if rep.ScannedFiles >= maxScanFiles || totalBytes >= maxTotalRead {
			return fs.SkipAll
		}
		info, serr := d.Info()
		if serr != nil || info.Size() == 0 || info.Size() > maxFileReadBytes {
			return nil
		}
		rel, _ := filepath.Rel(decompiledRoot, p)
		rel = filepath.ToSlash(rel)

		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		rep.ScannedFiles++
		totalBytes += info.Size()

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 16*1024), maxFileReadBytes)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}

			// 1. Endpoints
			if len(rep.Endpoints) < maxEndpoints {
				for _, m := range urlRegex.FindAllString(line, 4) {
					u, perr := url.Parse(strings.TrimRight(m, `.,;:'")>`))
					if perr != nil || u.Hostname() == "" || sanitizeCandidateHost(u.Hostname()) == "" {
						continue
					}
					key := u.String()
					if !seenEndpoint[key] {
						seenEndpoint[key] = true
						rep.Endpoints = append(rep.Endpoints, DecompiledEndpoint{URL: key, File: rel, Line: lineNo})
					}
				}
				for _, rm := range reRetrofitAnno.FindAllStringSubmatch(line, 2) {
					key := rm[1] + " " + rm[2]
					if !seenEndpoint[key] {
						seenEndpoint[key] = true
						rep.Endpoints = append(rep.Endpoints, DecompiledEndpoint{Method: rm[1], URL: rm[2], File: rel, Line: lineNo})
					}
				}
				for _, rp := range reRoutePathLit.FindAllStringSubmatch(line, 2) {
					key := rp[1]
					if !seenEndpoint[key] {
						seenEndpoint[key] = true
						rep.Endpoints = append(rep.Endpoints, DecompiledEndpoint{URL: rp[1], File: rel, Line: lineNo})
					}
				}
			}

			// 2. Secrets (explicit regex + Shannon entropy candidates)
			if len(rep.Secrets) < maxSecrets {
				for _, sm := range reSecretAssign.FindAllStringSubmatch(line, 2) {
					addDecompiledSecret(rep, seenSecret, "hardcoded_secret", sm[1], 0, rel, lineNo, line)
				}
				for _, tok := range reAKSKToken.FindAllString(line, 2) {
					addDecompiledSecret(rep, seenSecret, "token_or_key", tok, 0, rel, lineNo, line)
				}
				if rePEMBlock.MatchString(line) {
					addDecompiledSecret(rep, seenSecret, "pem_private_key", "-----BEGIN PRIVATE KEY-----", 0, rel, lineNo, line)
				}
				for _, hem := range reHighEntCand.FindAllStringSubmatch(line, 2) {
					val := hem[1]
					ent := shannonEntropy(val)
					if ent >= 4.0 {
						addDecompiledSecret(rep, seenSecret, "entropy_candidate", val, ent, rel, lineNo, line)
					}
				}
			}

			// 3. Signing / Crypto candidates
			if len(rep.SignCandidates) < maxSignCands && reSignKeyword.MatchString(line) {
				rep.SignCandidates = append(rep.SignCandidates, DecompiledSignCandidate{
					Hint:    reSignKeyword.FindString(line),
					File:    rel,
					Line:    lineNo,
					Snippet: clipUIString(strings.TrimSpace(line), 140),
				})
			}

			// 4. Semantic code risks
			if len(rep.Risks) < maxRisks {
				for _, pat := range whiteboxLineRisks {
					if pat.re.MatchString(line) {
						rk := fmt.Sprintf("%s:%s:%d", pat.id, rel, lineNo)
						if !seenRisk[rk] {
							seenRisk[rk] = true
							rep.Risks = append(rep.Risks, DecompiledCodeRisk{
								ID: pat.id, Category: pat.category, Severity: pat.severity, MASVS: pat.masvs,
								Title: pat.title, File: rel, Line: lineNo, Snippet: clipUIString(strings.TrimSpace(line), 140),
							})
						}
					}
				}
			}
		}
		_ = f.Close()
		return nil
	})
	sort.Slice(rep.Endpoints, func(i, j int) bool { return rep.Endpoints[i].URL < rep.Endpoints[j].URL })
	return err
}

func addDecompiledSecret(rep *DecompileAuditReport, seen map[string]bool, kind, rawVal string, entropy float64, file string, lineNo int, snippet string) {
	masked := maskSecretPreview(rawVal)
	k := fmt.Sprintf("%s:%s:%s:%d", kind, masked, file, lineNo)
	if seen[k] {
		return
	}
	seen[k] = true
	if entropy == 0 {
		entropy = math.Round(shannonEntropy(rawVal)*100) / 100
	} else {
		entropy = math.Round(entropy*100) / 100
	}
	rep.Secrets = append(rep.Secrets, DecompiledSecret{
		Kind: kind, Masked: masked, Entropy: entropy, File: file, Line: lineNo,
		Snippet: clipUIString(strings.TrimSpace(snippet), 120),
	})
}

func maskSecretPreview(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= 8 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:4]) + strings.Repeat("*", 6) + string(r[len(r)-4:])
}

func shannonEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	counts := make(map[byte]int, 32)
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	n := float64(len(s))
	var ent float64
	for _, c := range counts {
		p := float64(c) / n
		ent -= p * math.Log2(p)
	}
	return ent
}

func writeDecompileAuditArtifacts(auditOut string, rep *DecompileAuditReport) error {
	manifestRaw, err := json.MarshalIndent(map[string]any{
		"package_name":     rep.PackageName,
		"platform":         rep.Platform,
		"sample_path":      rep.SamplePath,
		"sample_sha256":    rep.SampleSHA256,
		"quality_status":   rep.QualityStatus,
		"tools_used":       rep.ToolsUsed,
		"java_file_count":  rep.JavaFileCount,
		"smali_file_count": rep.SmaliFileCount,
		"scanned_files":    rep.ScannedFiles,
		"endpoints_count":  len(rep.Endpoints),
		"secrets_count":    len(rep.Secrets),
		"risks_count":      len(rep.Risks),
		"warnings":         rep.Warnings,
		"generated_at":     rep.GeneratedAt,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(auditOut, "manifest.json"), manifestRaw, 0o644); err != nil {
		return err
	}

	reportRaw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(auditOut, "report.json"), reportRaw, 0o644); err != nil {
		return err
	}

	var epBuf strings.Builder
	for _, ep := range rep.Endpoints {
		if ep.Method != "" {
			fmt.Fprintf(&epBuf, "%s %s  # %s:%d\n", ep.Method, ep.URL, ep.File, ep.Line)
		} else {
			fmt.Fprintf(&epBuf, "%s  # %s:%d\n", ep.URL, ep.File, ep.Line)
		}
	}
	if err := os.WriteFile(filepath.Join(auditOut, "endpoints.txt"), []byte(epBuf.String()), 0o644); err != nil {
		return err
	}

	var secBuf strings.Builder
	for _, sec := range rep.Secrets {
		fmt.Fprintf(&secBuf, "[%s] %s (entropy=%.2f)  # %s:%d\n", sec.Kind, sec.Masked, sec.Entropy, sec.File, sec.Line)
	}
	return os.WriteFile(filepath.Join(auditOut, "secrets.txt"), []byte(secBuf.String()), 0o644)
}
