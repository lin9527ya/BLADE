package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// 证据质量闸：长度、机械验证信号、纯版本匹配。逐字回验在 tool_outputs.go。
// 默认 Warn（不拒绝合法上报，只把风险回传给模型）；Block 档由宿主设置后启用。
const (
	criticalEvidenceMinRunes = 200
	highEvidenceMinRunes     = 120
)

// EvidenceVerifyModeFn is wired by the host and read per report_finding call,
// so changing the setting takes effect without rebuilding a hot session.
var EvidenceVerifyModeFn func() EvidenceVerifyMode

type evidenceGateResult struct {
	Block string // Block 档才拒绝
	Warn  string // Warn/Block 档都提示
}

func assessFindingEvidence(severity, evidence string) evidenceGateResult {
	sev := strings.ToLower(strings.TrimSpace(severity))
	if sev != "critical" && sev != "high" {
		return evidenceGateResult{}
	}
	var out evidenceGateResult
	n := utf8.RuneCountInString(strings.TrimSpace(evidence))
	switch sev {
	case "critical":
		if n < criticalEvidenceMinRunes {
			out.Block = fmt.Sprintf("证据不足以支撑 critical 级别：evidence 至少需要 %d 字的可核验原始证据（请求/响应原文、命令输出、回显片段）。请补全证据；若只达到 L2，请降为 medium 并写明利用条件。", criticalEvidenceMinRunes)
		}
	case "high":
		if n < highEvidenceMinRunes {
			out.Block = fmt.Sprintf("证据不足以支撑 high 级别：evidence 至少需要 %d 字的可核验原始证据（请求/响应原文、命令输出或回显片段）。请补全；若只是间接推断，请降为 medium 并注明。", highEvidenceMinRunes)
		}
	}
	if out.Block != "" {
		return out // 已经被更具体的阻断原因占住，不再叠加泛化告警
	}
	if strings.TrimSpace(evidence) == "" {
		return out // 空证据由既有门槛处理，避免重复提示
	}
	if hasMechanicalVerification(evidence) {
		return out
	}
	if hasVersionOnlyEvidence(evidence) {
		out.Block = "证据只有版本/banner 匹配，没有行为级验证——发行版 backport 补丁不改版本号，版本匹配 ≠ 漏洞。" +
			"请用 http_probe 或 curl 实际触发目标，把 HTTP 状态码/响应特征/命令输出写进 evidence；若只是版本匹配，请降为 medium 并注明「版本匹配，行为未验证」。"
		return out
	}
	if out.Warn == "" {
		out.Warn = "critical/high 证据缺少可机械复现的验证凭据（HTTP 状态码/curl 命令输出/工具命中结果）。" +
			"请把实际触发的状态码、响应头或关键字段写入 evidence；「可能存在」不是 finding。"
	}
	return out
}

func hasMechanicalVerification(evidence string) bool {
	lower := strings.ToLower(evidence)
	for _, sig := range []string{
		"http/1.", "http/2", "curl -", "curl ", "sqlmap", "nuclei",
		"status: 200", "status: 201", "status: 204", "status: 301", "status: 302", "status: 400", "status: 401", "status: 403", "status: 404", "status: 500",
		"\"code\":", "\"status\":", "\"data\":", "location:", "set-cookie:", "content-type:",
		"uid=", "uid :", "root:x:", "begin rsa private key", "begin openssh private key",
	} {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

func hasVersionOnlyEvidence(evidence string) bool {
	lower := strings.ToLower(evidence)
	hasVersion, hasExploit := false, false
	for _, sig := range []string{"version", "banner", "server:", "x-powered-by", "powered-by"} {
		if strings.Contains(lower, sig) {
			hasVersion = true
			break
		}
	}
	for _, sig := range []string{"curl -", "http/1.", "status: 200", "status: 403", "response", "output", "uid=", "root:x:"} {
		if strings.Contains(lower, sig) {
			hasExploit = true
			break
		}
	}
	return hasVersion && !hasExploit
}
