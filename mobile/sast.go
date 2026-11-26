package mobile

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

type NamedBuffer struct {
	Name string
	Data []byte
}

var (
	hermesMagicBytes = []byte{0xc6, 0x1f, 0xbc, 0x03, 0xc1, 0x03, 0x19, 0x1f}
	sbomVersionRegex = regexp.MustCompile(`(?:OpenSSL|BoringSSL|libcurl|curl|SQLite|OkHttp|okhttp|Retrofit|WebView|FFmpeg|libpng)[ /_-]?([0-9]+\.[0-9]+(?:\.[0-9]+)?[a-zA-Z0-9._-]*)`)
)

// InspectAPKSignatures detects APK v1 JAR signatures and APK Signing Block v2/v3 blocks directly from disk.
func InspectAPKSignatures(zr *zip.Reader, path string, meta *PackageMetadata) {
	var hasV1SF, hasV1Cert bool
	for _, f := range zr.File {
		upper := strings.ToUpper(f.Name)
		if strings.HasPrefix(upper, "META-INF/") {
			if strings.HasSuffix(upper, ".SF") {
				hasV1SF = true
			}
			if strings.HasSuffix(upper, ".RSA") || strings.HasSuffix(upper, ".DSA") || strings.HasSuffix(upper, ".EC") {
				hasV1Cert = true
			}
		}
	}
	if hasV1SF && hasV1Cert {
		meta.SignatureSchemes = appendUnique(meta.SignatureSchemes, "v1")
	}

	v2, v3 := scanAPKSigningBlockV2V3(path)
	if v2 {
		meta.SignatureSchemes = appendUnique(meta.SignatureSchemes, "v2")
	}
	if v3 {
		meta.SignatureSchemes = appendUnique(meta.SignatureSchemes, "v3")
	}
	if hasV1SF && hasV1Cert && !v2 && !v3 {
		meta.Warnings = appendUnique(meta.Warnings, "APK only uses legacy v1 (JAR) signature without v2/v3 APK Signature Scheme (susceptible to Janus CVE-2017-13156 on unpatched Android)")
	}
}

func scanAPKSigningBlockV2V3(path string) (v2, v3 bool) {
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < 64 {
		return false, false
	}

	// Read tail up to 64KiB to locate ZIP End of Central Directory (EOCD).
	tailLen := int64(65536)
	if st.Size() < tailLen {
		tailLen = st.Size()
	}
	tail := make([]byte, tailLen)
	if _, err := f.ReadAt(tail, st.Size()-tailLen); err != nil && err != io.EOF {
		return false, false
	}
	cdOffset := int64(-1)
	for i := len(tail) - 22; i >= 0; i-- {
		if tail[i] == 0x50 && tail[i+1] == 0x4b && tail[i+2] == 0x05 && tail[i+3] == 0x06 {
			commentLen := int(binary.LittleEndian.Uint16(tail[i+20 : i+22]))
			if i+22+commentLen == len(tail) {
				cdOffset = int64(binary.LittleEndian.Uint32(tail[i+16 : i+20]))
				break
			}
		}
	}
	if cdOffset < 32 || cdOffset > st.Size() {
		return false, false
	}
	// Check 24-byte footer immediately preceding ZIP Central Directory: [8B uint64 blockSize][16B "APK Sig Block 42"].
	var footer [24]byte
	if _, err := f.ReadAt(footer[:], cdOffset-24); err != nil {
		return false, false
	}
	if string(footer[8:24]) != "APK Sig Block 42" {
		return false, false
	}
	blockSize := binary.LittleEndian.Uint64(footer[:8])
	if blockSize < 24 || blockSize > 16<<20 || int64(blockSize)+8 > cdOffset {
		return false, false
	}
	sigBlock := make([]byte, int(blockSize)-16) // pairs section right up to footer[8:24]
	if _, err := f.ReadAt(sigBlock, cdOffset-int64(blockSize)); err != nil {
		return false, false
	}
	off := 0
	for off+12 <= len(sigBlock) {
		pairLen := binary.LittleEndian.Uint64(sigBlock[off : off+8])
		off += 8
		if pairLen < 4 || uint64(off)+pairLen > uint64(len(sigBlock)) {
			break
		}
		id := binary.LittleEndian.Uint32(sigBlock[off : off+4])
		switch id {
		case 0x7109871a:
			v2 = true
		case 0xf05368c0, 0x1b93ad61:
			v3 = true
		}
		off += int(pairLen)
	}
	return v2, v3
}

// InspectELFBinary inspects an Android native .so ELF binary using Go debug/elf for security mitigations,
// JNI exports, SBOM library versions, and embedded URL/secret hints.
func InspectELFBinary(path string, raw []byte, meta *PackageMetadata) {
	if len(raw) < 64 || string(raw[:4]) != "\x7fELF" {
		return
	}
	ef, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return
	}
	defer ef.Close()

	rep := BinarySecurityReport{
		Path:   path,
		Format: "elf",
		Arch:   ef.Machine.String(),
		PIE:    ef.Type == elf.ET_DYN,
	}
	hasRELRO := false
	hasBindNow := false
	for _, ph := range ef.Progs {
		switch ph.Type {
		case elf.PT_GNU_STACK:
			rep.NX = (ph.Flags & elf.PF_X) == 0
		case elf.PT_GNU_RELRO:
			hasRELRO = true
		}
	}
	if sec := ef.Section(".dynamic"); sec != nil {
		if vals, err := ef.DynValue(elf.DT_FLAGS); err == nil {
			for _, v := range vals {
				if v&0x8 != 0 { // DF_BIND_NOW
					hasBindNow = true
				}
			}
		}
		if vals, err := ef.DynValue(elf.DT_FLAGS_1); err == nil {
			for _, v := range vals {
				if v&0x1 != 0 { // DF_1_NOW
					hasBindNow = true
				}
			}
		}
		if vals, err := ef.DynValue(elf.DT_BIND_NOW); err == nil && len(vals) > 0 {
			hasBindNow = true
		}
		if rpaths, err := ef.DynString(elf.DT_RPATH); err == nil && len(rpaths) > 0 {
			rep.RPath = strings.Join(rpaths, ":")
		}
		if runpaths, err := ef.DynString(elf.DT_RUNPATH); err == nil && len(runpaths) > 0 {
			if rep.RPath != "" {
				rep.RPath += ";"
			}
			rep.RPath += strings.Join(runpaths, ":")
		}
	}
	switch {
	case hasRELRO && hasBindNow:
		rep.RELRO = "full"
	case hasRELRO:
		rep.RELRO = "partial"
	default:
		rep.RELRO = "none"
	}

	inspectSyms := func(syms []elf.Symbol) {
		for _, s := range syms {
			switch s.Name {
			case "__stack_chk_fail":
				rep.StackCanary = true
			case "JNI_OnLoad":
				rep.JNIOnLoad = true
			}
			if strings.HasPrefix(s.Name, "Java_") && len(rep.JNIFunctions) < 25 {
				rep.JNIFunctions = appendUnique(rep.JNIFunctions, s.Name)
			}
		}
	}
	if syms, err := ef.DynamicSymbols(); err == nil {
		inspectSyms(syms)
	}
	if syms, err := ef.Symbols(); err == nil {
		inspectSyms(syms)
	}
	if len(meta.NativeBinaries) < 40 {
		meta.NativeBinaries = append(meta.NativeBinaries, rep)
	}
	scanTextBufferForEndpoints(raw, meta)
	extractSBOMHints(raw, meta)
}

// ExtractHermesStrings detects React Native Hermes bytecode and extracts printable ASCII strings
// for endpoint/key discovery when bundles are compiled instead of plain JS.
func ExtractHermesStrings(raw []byte, meta *PackageMetadata) bool {
	if len(raw) < 16 || !bytes.HasPrefix(raw, hermesMagicBytes) {
		return false
	}
	meta.HermesBytecode = true
	meta.Frameworks = appendUnique(meta.Frameworks, "React Native (Hermes)")
	var asciiBuf strings.Builder
	runStart := -1
	flush := func(end int) {
		if runStart >= 0 && end-runStart >= 10 {
			asciiBuf.Write(raw[runStart:end])
			asciiBuf.WriteByte('\n')
		}
		runStart = -1
	}
	for i, b := range raw {
		if b >= 0x20 && b <= 0x7e {
			if runStart < 0 {
				runStart = i
			}
		} else {
			flush(i)
		}
	}
	flush(len(raw))
	if asciiBuf.Len() > 0 {
		buf := []byte(asciiBuf.String())
		scanTextBufferForEndpoints(buf, meta)
		extractSBOMHints(buf, meta)
	}
	return true
}

func extractSBOMHints(buf []byte, meta *PackageMetadata) {
	if len(meta.SBOMHints) >= 30 {
		return
	}
	for _, m := range sbomVersionRegex.FindAll(buf, 12) {
		s := strings.TrimSpace(string(m))
		if len(s) >= 6 && len(s) <= 64 {
			meta.SBOMHints = appendUnique(meta.SBOMHints, s)
			if len(meta.SBOMHints) >= 30 {
				return
			}
		}
	}
}

type sastByteRule struct {
	id             string
	category       string
	severity       string
	masvs          string
	title          string
	anyTokens      []string
	allTokens      []string
	recommendation string
}

var codeSASTRules = []sastByteRule{
	{
		id:             "android-ssl-handler-proceed",
		category:       "network",
		severity:       "high",
		masvs:          "MASVS-NETWORK-1",
		title:          "WebViewClient.onReceivedSslError 或 SslErrorHandler.proceed 可能绕过 TLS 证书校验",
		allTokens:      []string{"onReceivedSslError", "SslErrorHandler", "proceed"},
		recommendation: "禁止在生产包对无效 TLS 证书调用 SslErrorHandler.proceed()，应直接取消加载或校验证书链。",
	},
	{
		id:             "android-permissive-hostname-verifier",
		category:       "network",
		severity:       "high",
		masvs:          "MASVS-NETWORK-1",
		title:          "检测到宽松 HostnameVerifier / ALLOW_ALL_HOSTNAME_VERIFIER 引用",
		anyTokens:      []string{"ALLOW_ALL_HOSTNAME_VERIFIER", "AllowAllHostnameVerifier"},
		recommendation: "禁用全部主机名信任规则，使用默认 strict HostnameVerifier 并配合可信证书校验。",
	},
	{
		id:             "android-webview-js-file-access",
		category:       "platform",
		severity:       "high",
		masvs:          "MASVS-PLATFORM-2",
		title:          "WebView 同时存在 JavaScriptEnabled 与本地文件/跨域文件访问或 JSBridge 接口引用",
		allTokens:      []string{"setJavaScriptEnabled", "addJavascriptInterface"},
		recommendation: "严格限制 WebView 加载域名白名单，关闭 setAllowFileAccessFromFileURLs / setAllowUniversalAccessFromFileURLs，校验调用源后再暴露 JSBridge。",
	},
	{
		id:             "android-webview-universal-file",
		category:       "platform",
		severity:       "medium",
		masvs:          "MASVS-PLATFORM-2",
		title:          "检测到 setAllowUniversalAccessFromFileURLs 或 setAllowFileAccessFromFileURLs",
		anyTokens:      []string{"setAllowUniversalAccessFromFileURLs", "setAllowFileAccessFromFileURLs"},
		recommendation: "若非明确离线本地渲染场景，确保禁止 file:// 上下文跨域读取应用私有目录文件。",
	},
	{
		id:             "android-weak-crypto-ecb-des-rc4",
		category:       "crypto",
		severity:       "medium",
		masvs:          "MASVS-CRYPTO-1",
		title:          "检测到弱对称加密模式或淘汰算法常量 (AES/ECB / DES / RC4)",
		anyTokens:      []string{"AES/ECB", "DES/ECB", "DESede/ECB", "Cipher.getInstance(\"DES\"", "\"RC4\""},
		recommendation: "替换为 AES-GCM / ChaCha20-Poly1305 等带完整性校验的现代 AEAD 算法并随机生成每次加密的 IV/Nonce。",
	},
	{
		id:             "android-world-readable-storage",
		category:       "storage",
		severity:       "high",
		masvs:          "MASVS-STORAGE-1",
		title:          "检测到 MODE_WORLD_READABLE / MODE_WORLD_WRITEABLE 过宽文件权限常量",
		anyTokens:      []string{"MODE_WORLD_READABLE", "MODE_WORLD_WRITEABLE"},
		recommendation: "改用 MODE_PRIVATE 与 EncryptedSharedPreferences / Android Keystore。",
	},
	{
		id:             "android-dynamic-dex-load",
		category:       "resilience",
		severity:       "low",
		masvs:          "MASVS-CODE-4",
		title:          "检测到 DexClassLoader / PathClassLoader 动态代码加载特征",
		anyTokens:      []string{"Ldalvik/system/DexClassLoader;", "DexClassLoader"},
		recommendation: "确保动态下发 DEX 存放在私有只读目录并校验数字签名后加载。",
	},
	{
		id:             "android-pendingintent-mutable",
		category:       "platform",
		severity:       "medium",
		masvs:          "MASVS-PLATFORM-1",
		title:          "检测到 PendingIntent.FLAG_MUTABLE，若配隐式 Intent 可能存在截获/劫持风险",
		allTokens:      []string{"PendingIntent", "FLAG_MUTABLE"},
		recommendation: "优先使用 FLAG_IMMUTABLE，必须可变时须显式指定 ComponentName。",
	},
}

// RunStaticSAST produces deterministic MASVS findings across manifest, signature, binary protections,
// provisioning profiles, and extracted bytecode/resource buffers.
func RunStaticSAST(meta *PackageMetadata, buffers []NamedBuffer) {
	if meta == nil {
		return
	}
	seen := map[string]bool{}
	for _, f := range meta.StaticFindings {
		seen[f.ID+":"+f.Location] = true
	}
	addFinding := func(f StaticFinding) {
		key := f.ID + ":" + f.Location
		if seen[key] {
			return
		}
		seen[key] = true
		meta.StaticFindings = append(meta.StaticFindings, f)
	}

	// 1. Manifest & config rules
	if meta.Debuggable {
		addFinding(StaticFinding{
			ID: "manifest-debuggable", Category: "resilience", Severity: "high", MASVS: "MASVS-RESILIENCE-2",
			Title: "AndroidManifest 开启了 android:debuggable=true", Location: "AndroidManifest.xml",
			Evidence: "debuggable=true", Recommendation: "发布包关闭 android:debuggable，防止非 Root 环境 jdb/run-as 附加。",
		})
	}
	if meta.AllowBackup && meta.Platform == PlatformAndroid {
		addFinding(StaticFinding{
			ID: "manifest-allow-backup", Category: "storage", Severity: "medium", MASVS: "MASVS-STORAGE-2",
			Title: "AndroidManifest 允许应用数据备份 (allowBackup=true)", Location: "AndroidManifest.xml",
			Evidence: "allowBackup=true", Recommendation: "设置 android:allowBackup=\"false\" 或配置严格 dataExtractionRules 排除敏感凭证。",
		})
	}
	if meta.CleartextTraffic != nil && *meta.CleartextTraffic {
		addFinding(StaticFinding{
			ID: "manifest-cleartext-http", Category: "network", Severity: "medium", MASVS: "MASVS-NETWORK-1",
			Title: "允许明文 HTTP 通信 (usesCleartextTraffic=true)", Location: "AndroidManifest.xml",
			Evidence: "usesCleartextTraffic=true", Recommendation: "全站强制 HTTPS 并禁用 cleartextTrafficPermitted。",
		})
	}
	if meta.NetworkSecTrustUser {
		addFinding(StaticFinding{
			ID: "nsc-trust-user-ca", Category: "network", Severity: "medium", MASVS: "MASVS-NETWORK-1",
			Title: "network_security_config 配置信任用户导入证书 (certificates src=\"user\")", Location: "res/xml/network_security_config.xml",
			Evidence: "src=user", Recommendation: "正式版仅在 debug-overrides 下允许 user CA，避免流量被恶意或本地证书轻易中间人截获。",
		})
	}
	if meta.RequestLegacyStorage {
		addFinding(StaticFinding{
			ID: "manifest-legacy-external-storage", Category: "storage", Severity: "low", MASVS: "MASVS-STORAGE-2",
			Title: "启用 requestLegacyExternalStorage=true 申请广域外部存储读写", Location: "AndroidManifest.xml",
			Evidence: "requestLegacyExternalStorage=true", Recommendation: "迁移至分区存储 (Scoped Storage) 并避免外部存储写入隐私数据。",
		})
	}
	if len(meta.SignatureSchemes) > 0 && slicesContains(meta.SignatureSchemes, "v1") &&
		!slicesContains(meta.SignatureSchemes, "v2") && !slicesContains(meta.SignatureSchemes, "v3") {
		addFinding(StaticFinding{
			ID: "apk-v1-only-janus", Category: "resilience", Severity: "medium", MASVS: "MASVS-RESILIENCE-1",
			Title: "APK 仅采用 v1 签名而缺少 v2/v3 整包校验（Janus 漏洞面）", Location: "META-INF/",
			Evidence: strings.Join(meta.SignatureSchemes, ","), Recommendation: "使用 apksigner 同时开启 APK Signature Scheme v2/v3。",
		})
	}

	unprotectedExported := 0
	for _, c := range meta.ExportedComponents {
		if c.Exported && c.Permission == "" && c.ReadPermission == "" && c.WritePermission == "" {
			if c.Type == "provider" || c.Type == "receiver" || c.Type == "service" {
				addFinding(StaticFinding{
					ID: "ipc-unprotected-" + c.Type, Category: "platform", Severity: "medium", MASVS: "MASVS-PLATFORM-1",
					Title: fmt.Sprintf("无权限保护的导出 %s 组件 (%s)", c.Type, c.Name), Location: "AndroidManifest.xml",
					Evidence:       fmt.Sprintf("type=%s authorities=%s", c.Type, c.Authorities),
					Recommendation: "若非跨应用公开接口，应设置 android:exported=\"false\" 或声明 signature 级权限保护。",
				})
			} else {
				unprotectedExported++
			}
		}
		if c.Type == "activity" && c.Exported && (strings.Contains(strings.ToLower(c.LaunchMode), "singletask") || c.LaunchMode == "2") && (c.TaskAffinity != "" || meta.TaskAffinity != "") {
			addFinding(StaticFinding{
				ID: "android-task-hijacking", Category: "platform", Severity: "low", MASVS: "MASVS-PLATFORM-1",
				Title: fmt.Sprintf("Activity (%s) 存在 StrandHogg 任务栈劫持隐患（singleTask + taskAffinity）", c.Name), Location: "AndroidManifest.xml",
				Evidence: "launchMode=" + c.LaunchMode, Recommendation: "显式设置 taskAffinity=\"\" 并谨慎使用 singleTask。",
			})
		}
	}
	if unprotectedExported > 0 {
		addFinding(StaticFinding{
			ID: "ipc-unprotected-activities", Category: "platform", Severity: "low", MASVS: "MASVS-PLATFORM-1",
			Title: fmt.Sprintf("Manifest 存在 %d 个未声明权限的导出 Activity", unprotectedExported), Location: "AndroidManifest.xml",
			Recommendation: "核查接收 Intent/DeepLink 参数的 Activity 是否校验来源或存在 WebView/认证绕过。",
		})
	}

	if meta.ATS != nil && meta.ATS.AllowsArbitraryLoads {
		addFinding(StaticFinding{
			ID: "ios-ats-arbitrary-loads", Category: "network", Severity: "medium", MASVS: "MASVS-NETWORK-1",
			Title: "Info.plist 开启了 NSAllowsArbitraryLoads=true 禁用全局 iOS ATS 校验", Location: "Info.plist",
			Evidence: "NSAllowsArbitraryLoads=true", Recommendation: "关闭 NSAllowsArbitraryLoads，仅对必要域名配置最小例外。",
		})
	}
	if meta.Provisioning != nil && meta.Provisioning.GetTaskAllow {
		addFinding(StaticFinding{
			ID: "ios-get-task-allow", Category: "resilience", Severity: "high", MASVS: "MASVS-RESILIENCE-2",
			Title: "embedded.mobileprovision 允许调试器附加 (get-task-allow=true)", Location: "embedded.mobileprovision",
			Evidence: "get-task-allow=true", Recommendation: "正式分发包使用 Release Provisioning Profile，禁止包含调试权限。",
		})
	}
	if meta.FairPlayEncrypted != nil && !*meta.FairPlayEncrypted && meta.Platform == PlatformIOS {
		addFinding(StaticFinding{
			ID: "ios-macho-unencrypted", Category: "binary", Severity: "info", MASVS: "MASVS-RESILIENCE-1",
			Title: "iOS 主二进制 CryptID=0（未加 FairPlay 加密，可直接进行静态反编译分析）", Location: "Payload/*.app",
			Evidence: "cryptid=0",
		})
	}

	// 2. Native ELF / Mach-O hardening rules (first 5 notable violations summarized cleanly).
	noPIECount, noCanaryCount, noNXCount := 0, 0, 0
	for _, b := range meta.NativeBinaries {
		if !b.PIE {
			noPIECount++
		}
		if !b.StackCanary {
			noCanaryCount++
		}
		if !b.NX {
			noNXCount++
		}
		if b.RPath != "" && (strings.Contains(b.RPath, "/data/") || b.RPath == "." || strings.Contains(b.RPath, "./")) {
			addFinding(StaticFinding{
				ID: "native-unsafe-rpath", Category: "binary", Severity: "medium", MASVS: "MASVS-CODE-4",
				Title: fmt.Sprintf("原生库 %s 含可疑 RPATH/RUNPATH (%s)", b.Path, b.RPath), Location: b.Path,
				Recommendation: "清理编译选项中的非系统 RPATH，防范动态库劫持。",
			})
		}
	}
	if noPIECount > 0 || noNXCount > 0 || noCanaryCount > 0 {
		addFinding(StaticFinding{
			ID: "native-missing-mitigations", Category: "binary", Severity: "low", MASVS: "MASVS-CODE-9",
			Title:          fmt.Sprintf("原生二进制安全编译选项缺失汇总：缺 PIE=%d、缺 NX=%d、缺 StackCanary=%d", noPIECount, noNXCount, noCanaryCount),
			Location:       "lib/* / Frameworks",
			Recommendation: "开启 -fPIE -pie、-fstack-protector-strong、-Wl,-z,relro,-z,now 与禁用可执行栈。",
		})
	}

	// 3. Bytecode / JS / XML pattern rules across extracted buffers
	for _, buf := range buffers {
		if len(buf.Data) == 0 {
			continue
		}
		for _, rule := range codeSASTRules {
			matched := false
			ev := ""
			if len(rule.allTokens) > 0 {
				allOK := true
				for _, tok := range rule.allTokens {
					if !bytes.Contains(buf.Data, []byte(tok)) {
						allOK = false
						break
					}
				}
				if allOK {
					matched = true
					ev = strings.Join(rule.allTokens, " + ")
				}
			} else if len(rule.anyTokens) > 0 {
				for _, tok := range rule.anyTokens {
					if bytes.Contains(buf.Data, []byte(tok)) {
						matched = true
						ev = tok
						break
					}
				}
			}
			if matched {
				addFinding(StaticFinding{
					ID: rule.id, Category: rule.category, Severity: rule.severity, MASVS: rule.masvs,
					Title: rule.title, Location: buf.Name, Evidence: ev, Recommendation: rule.recommendation,
				})
			}
		}
	}

	if len(meta.HardcodedHints) > 0 {
		addFinding(StaticFinding{
			ID: "static-hardcoded-secret-hints", Category: "crypto", Severity: "high", MASVS: "MASVS-CRYPTO-1",
			Title:    fmt.Sprintf("静态扫描命中 %d 处疑似硬编码密钥/AK/Secret 特征", len(meta.HardcodedHints)),
			Location: "classes.dex / assets / binary", Evidence: strings.Join(meta.HardcodedHints, "; "),
			Recommendation: "立即排查对应客户端文件是否存在云厂商 AK/SK、签名 Secret 或私钥明文，并做后端鉴权与轮换。",
		})
	}
}

func slicesContains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
