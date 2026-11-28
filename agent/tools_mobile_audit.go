package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
)

func boolField(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func (t *ToolSet) mobileDecompileAudit() actool.CoreTool {
	return writeTool("mobile_decompile_audit",
		"对当前任务上传或绑定的移动应用样本执行安全解包（防 Zip Slip/路径穿越/炸弹限额，支持 APK/XAPK/APKM/APKS/AAB/IPA）、质量门控反编译（jadx/apktool 或仅白盒扫描）并输出源码层端点、硬编码秘钥前缀、WebView JS Bridge、加密误用与敏感清单（写入 tasks/<id>/mobile_audit/）。",
		obj(map[string]any{
			"sample_path":  str("可选：样本相对路径（相对于任务目录）或文件名；省略则自动选用任务当前绑定的主样本"),
			"mode":         str("auto（默认：jadx/apktool 质量门控后扫描）| fast（跳过反编译工具，仅安全解压与白盒扫描）| deep（优先完整反编译并白盒审计）"),
			"skip_apktool": boolField("是否跳过 apktool Smali/Manifest 解包（默认 false）"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			taskID, br, errRes, ok := t.requireMobileTask(ctx, "mobile_decompile_audit")
			if !ok {
				return errRes, nil
			}
			out, err := br.Audit(ctx, taskID, "decompile_audit", in)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(out), nil
		})
}

var (
	reUnmaskedSecretEvidence = regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]*PRIVATE KEY-----|eyJ[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{12,}|AKIA[0-9A-Z]{16}|LTAI[0-9A-Za-z]{16,}|Bearer\s+[A-Za-z0-9._-]{24,}|(?:sk_live|rk_live|AIza)[0-9A-Za-z_-]{16,})`)
	reHTTPProofHint          = regexp.MustCompile(`(?i)(\b(?:GET|POST|PUT|DELETE|PATCH|OPTIONS)\s+/|\bHTTP/[12](?:\.[01])?\s+[1-5][0-9]{2}\b|curl\s+-|mobile_sign_method|traffic_id|http_request\()`)
	reSourceTraceHint        = regexp.MustCompile(`(?i)(\b(?:jadx|apktool|smali|classes\d*\.dex|AndroidManifest\.xml|Info\.plist|frida|logcat|adb\s+shell|content://|\.so\b|lib[A-Za-z0-9_]+\.so|dump|hierarchy)\b)`)
)

type mobileEvidenceCheckInput struct {
	Title        string `json:"title"`
	VulnType     string `json:"vuln_type"`
	Severity     string `json:"severity"`
	PackageName  string `json:"package_name"`
	SampleSHA256 string `json:"sample_sha256"`
	SourceRef    string `json:"source_ref"`
	EvidenceText string `json:"evidence_text"`
	HTTPProof    string `json:"http_proof"`
	ClientOnly   bool   `json:"client_only"`
}

func (t *ToolSet) loadManifestFallback() (pkg, sha256Hex string) {
	if t == nil || strings.TrimSpace(t.taskRoot) == "" {
		return "", ""
	}
	raw, err := os.ReadFile(filepath.Join(t.taskRoot, "mobile_audit", "manifest.json"))
	if err != nil {
		return "", ""
	}
	var m struct {
		PackageName string `json:"package_name"`
		SHA256      string `json:"sha256"`
	}
	if json.Unmarshal(raw, &m) == nil {
		return strings.TrimSpace(m.PackageName), strings.TrimSpace(m.SHA256)
	}
	return "", ""
}

func (t *ToolSet) loadActiveTrafficFindingsCount() int {
	if t == nil || t.ts == nil {
		return 0
	}
	nodes, err := t.ts.ListActiveFindings(50)
	if err != nil || len(nodes) == 0 {
		return 0
	}
	_ = t.ts.PopulateFindingTrafficIDs(nodes)
	n := 0
	for _, node := range nodes {
		if node != nil && node.TrafficCount > 0 {
			n++
		}
	}
	return n
}

func (t *ToolSet) mobileEvidenceCheck() actool.CoreTool {
	return readTool("mobile_evidence_check",
		"在提交移动 APP 漏洞报告（report_finding）或收尾前执行结构化证据闭环校验：核验样本溯源（包名/SHA-256）、客户端源码或 Hook 追踪锚点、后端 HTTP 请求响应闭环、高危伪阳性（纯客户端配置不得定性高危）与凭据脱敏规范，输出 src_ready 与缺口建议。",
		obj(map[string]any{
			"title":         str("待校验发现或报告标题"),
			"vuln_type":     str("漏洞分类：api_idor | auth_bypass | hardcoded_secret | ipc_export | webview_xss | weak_crypto | cert_pinning | config_hardening"),
			"severity":      str("critical | high | medium | low | info"),
			"package_name":  str("被测 APP 包名或 Bundle ID（省略时自动从任务摘要/manifest.json 提取）"),
			"sample_sha256": str("样本 SHA-256（省略时自动从任务摘要/manifest.json 提取）"),
			"source_ref":    str("静态或动态追踪锚点，如 jadx 类方法名、AndroidManifest.xml 组件名、.so 符号、Frida Hook 日志路径或 ADB 输出"),
			"evidence_text": str("拟提交的发现详情/复现步骤/响应摘录（将自动检测是否包含未脱敏 JWT/AK/私钥/Bearer）"),
			"http_proof":    str("后端 API 请求/响应摘录、cURL 复现链或关联的 traffic_id/http_request 调用说明"),
			"client_only":   boolField("是否为纯客户端发现且尚未关联服务端可利用影响（如 allowBackup/debuggable/明文日志）"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a mobileEvidenceCheckInput
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			pkg := strings.TrimSpace(a.PackageName)
			sha := strings.TrimSpace(a.SampleSHA256)
			if mPkg, mSHA := t.loadManifestFallback(); pkg == "" || sha == "" {
				if pkg == "" {
					pkg = mPkg
				}
				if sha == "" {
					sha = mSHA
				}
			}
			taskID := RunInfoFrom(ctx).TaskID
			var taskSummary string
			var hasApp, hasDevice bool
			if taskID > 0 && TaskMobileInfo != nil {
				taskSummary, hasApp, hasDevice = TaskMobileInfo(taskID)
			}
			if sha == "" {
				if idx := strings.Index(taskSummary, "sha256="); idx >= 0 {
					rest := taskSummary[idx+len("sha256="):]
					if end := strings.IndexAny(rest, " \t\n,;)"); end > 0 {
						sha = rest[:end]
					} else {
						sha = strings.TrimSpace(rest)
					}
				}
			}

			severity := strings.ToLower(strings.TrimSpace(a.Severity))
			if severity == "" {
				severity = "info"
			}
			combinedEvidence := strings.TrimSpace(a.SourceRef + "\n" + a.EvidenceText + "\n" + a.HTTPProof)

			hasProvenance := (pkg != "" && sha != "") || (hasApp && sha != "") || (hasDevice && pkg != "")
			hasSourceTrace := strings.TrimSpace(a.SourceRef) != "" || reSourceTraceHint.MatchString(combinedEvidence)
			boundTrafficFindings := t.loadActiveTrafficFindingsCount()
			hasHTTPProof := strings.TrimSpace(a.HTTPProof) != "" || reHTTPProofHint.MatchString(combinedEvidence) || boundTrafficFindings > 0
			clientHighBlocked := a.ClientOnly && (severity == "high" || severity == "critical")
			unmaskedMatch := reUnmaskedSecretEvidence.FindString(a.EvidenceText + "\n" + a.HTTPProof)
			secretClean := unmaskedMatch == ""

			var missing []string
			var suggestions []string
			score := 0

			if hasProvenance {
				score += 25
			} else {
				missing = append(missing, "sample_provenance")
				suggestions = append(suggestions, "请先运行 mobile_decompile_audit 或补充 package_name 与 sample_sha256 证据溯源")
			}
			if hasSourceTrace {
				score += 25
			} else {
				missing = append(missing, "source_or_hook_trace")
				suggestions = append(suggestions, "请给出具体类/方法/Manifest组件/.so符号/Frida Hook 或 UI hierarchy 定位锚点（source_ref）")
			}
			if a.ClientOnly {
				if clientHighBlocked {
					missing = append(missing, "client_only_severity_mismatch")
					suggestions = append(suggestions, "纯客户端加固/配置项不得直接定性为 high/critical：请补充后端接口/凭证利用链（http_proof），或下调至 low/info")
				} else {
					score += 25
				}
			} else if hasHTTPProof {
				score += 25
			} else {
				missing = append(missing, "backend_http_proof")
				suggestions = append(suggestions, "APP 服务端漏洞须提供请求/响应包闭环或 cURL/traffic_refs 复现链（http_proof）")
			}
			if secretClean {
				score += 25
			} else {
				missing = append(missing, "unmasked_secret_in_evidence")
				suggestions = append(suggestions, "检测到疑似未脱敏凭据/JWT/私钥片段（"+maskSampleSecret(unmaskedMatch)+"），报告正文仅保留脱敏前后缀并在 loot 中管理凭据状态")
			}

			srcReady := len(missing) == 0
			out := map[string]any{
				"src_ready":              srcReady,
				"score":                  score,
				"severity":               severity,
				"has_app_context":        hasApp,
				"has_device_context":     hasDevice,
				"traffic_bound_findings": boundTrafficFindings,
				"checks": map[string]bool{
					"sample_provenance":       hasProvenance,
					"source_or_hook_trace":    hasSourceTrace,
					"backend_or_impact_proof": !clientHighBlocked && (a.ClientOnly || hasHTTPProof),
					"secret_masking_clean":    secretClean,
				},
				"missing":     missing,
				"suggestions": suggestions,
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return actool.Text(string(b)), nil
		})
}

func maskSampleSecret(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 10 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-4:]
}
