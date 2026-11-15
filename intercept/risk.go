package intercept

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RiskReport is a deterministic, display-only risk score for a pending approval.
// It is computed when a pending row is read; it is never stored and never changes
// the decision. The UI uses it to sort/confirm batch actions, not to bypass rules.
type RiskReport struct {
	Score   int      `json:"score"`
	Level   string   `json:"level"` // low | medium | high
	Reasons []string `json:"reasons"`
}

const (
	riskLowMax    = 29
	riskMediumMax = 69
)

func riskLevel(score int) string {
	switch {
	case score <= riskLowMax:
		return "low"
	case score <= riskMediumMax:
		return "medium"
	default:
		return "high"
	}
}

// ScoreRisk scores a tool call without a model. Shell commands are the main case;
// file writes, web fetches and MCP/unknown tools get conservative defaults.
func ScoreRisk(toolName string, input json.RawMessage) RiskReport {
	report := RiskReport{Reasons: []string{}}
	add := func(points int, reason string) {
		report.Score += points
		report.Reasons = append(report.Reasons, reason)
	}
	var obj map[string]any
	if len(input) == 0 || json.Unmarshal(input, &obj) != nil || obj == nil {
		add(30, "参数不是 JSON 对象，无法做确定性风险判断")
		return finishRisk(report)
	}
	text := strings.ToLower(fmt.Sprint(obj))
	switch toolName {
	case "Bash", "shell_open", "shell_send", "run_execution":
		report.Score = 10
		cmd := strings.ToLower(stringField(obj, "command", "program", "input"))
		if cmd == "" {
			cmd = text
		}
		signals := []struct {
			points int
			reason string
			match  bool
		}{
			{60, "破坏性命令", containsAny(cmd, "rm -r", "rm -f", " rm ", "dd ", "mkfs", "shred", "truncate", "drop table", "delete from", "shutdown", "wipefs")},
			{40, "提权/权限变更", containsAny(cmd, "sudo", " su ", "chmod 777", "chmod +s", "setcap", "chown root")},
			{40, "读取系统/云凭据", containsAny(cmd, "/etc/shadow", ".ssh/", ".aws/credentials", ".git-credentials", ".kube/config", "id_rsa")},
			{40, "可能外传数据", containsAny(cmd, "curl -d", "curl -f", "curl -t", "scp ", "rsync ", "nc ", "ncat ", "socat ")},
			{60, "反弹/反向 shell", containsAny(cmd, "/dev/tcp/", "nc -e", "ncat -e", "bash -i")},
			{30, "扫描器/批量尝试", containsAny(cmd, "nmap", "nuclei", "sqlmap", "ffuf", "hydra", "masscan", "dalfox", "nikto")},
			{15, "写文件/重定向", hasWriteRedirection(cmd)},
		}
		for _, s := range signals {
			if s.match {
				add(s.points, s.reason)
			}
		}
		if len(report.Reasons) == 0 {
			report.Reasons = append(report.Reasons, "普通只读/查询命令")
		}
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		report.Score = 30
		report.Reasons = append(report.Reasons, "修改文件")
		path := strings.ToLower(stringField(obj, "file_path", "path", "notebook_path"))
		if sensitivePath(path) {
			add(40, "修改敏感路径")
		}
	case "WebFetch", "web_search":
		report.Score = 10
		report.Reasons = append(report.Reasons, "网络读取")
		method := strings.ToUpper(stringField(obj, "method"))
		switch method {
		case "POST", "PUT", "PATCH", "DELETE":
			add(30, "网络写入方法 "+method)
		}
	default:
		report.Score = 30
		report.Reasons = append(report.Reasons, "MCP/未知工具，按中等风险处理")
	}
	return finishRisk(report)
}

func finishRisk(r RiskReport) RiskReport {
	if r.Score > 100 {
		r.Score = 100
	}
	if r.Score < 0 {
		r.Score = 0
	}
	if r.Reasons == nil {
		r.Reasons = []string{}
	}
	r.Level = riskLevel(r.Score)
	return r
}

func stringField(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := obj[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func hasWriteRedirection(cmd string) bool {
	// 2>&1 and /dev/null are the common non-writing redirections in scanner commands.
	stripped := strings.ReplaceAll(cmd, "2>&1", "")
	stripped = strings.ReplaceAll(stripped, "> /dev/null", "")
	stripped = strings.ReplaceAll(stripped, ">/dev/null", "")
	return strings.Contains(stripped, ">") || strings.Contains(stripped, "tee ")
}

func sensitivePath(p string) bool {
	if p == "" {
		return false
	}
	for _, prefix := range []string{"/etc/", "/root/", "/boot/", "/dev/", "/proc/", "/sys/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	for _, marker := range []string{".ssh/", ".aws/", ".gnupg/", "authorized_keys", ".bashrc", ".zshrc", ".profile", ".git-credentials", ".env"} {
		if strings.Contains(p, marker) {
			return true
		}
	}
	return false
}
