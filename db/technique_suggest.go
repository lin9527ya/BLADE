package db

import "strings"

// ATT&CK 技术建议：确定性的关键词映射，只为“补标”提供候选，不自动写库。
// 它不是判定——采纳与否由人/操作者确认；未命中时明确返回空，不硬凑。

var techniqueHints = []struct {
	Techniques []string
	Keywords   []string
}{
	{[]string{"T1046"}, []string{"nmap", "masscan", "端口扫描", "服务发现", "port scan"}},
	{[]string{"T1110"}, []string{"hydra", "nxc", "爆破", "弱口令", "喷洒", "bruteforce", "spray"}},
	{[]string{"T1190"}, []string{"sqlmap", "sqli", "sql注入", "反序列化", "命令注入", "exploit", "rce"}},
	{[]string{"T1595"}, []string{"子域", "subdomain", "资产测绘", "fofa", "空间测绘"}},
	{[]string{"T1003.001"}, []string{"lsass", "mimikatz", "凭据抓取"}},
	{[]string{"T1021.004"}, []string{"ssh", "横向", "lateral"}},
	{[]string{"T1021.002"}, []string{"smb", "psexec", "winrm", "admin share"}},
	{[]string{"T1558.003"}, []string{"kerberoast", "spn"}},
	{[]string{"T1558.004"}, []string{"as-rep", "asrep"}},
	{[]string{"T1003.006"}, []string{"dcsync", "ntds"}},
	{[]string{"T1087.002"}, []string{"ldap", "域内枚举", "目录枚举", "bloodhound"}},
	{[]string{"T1105"}, []string{"上传", "下载", "wget", "curl -o", "ingress tool"}},
	{[]string{"T1505.003"}, []string{"webshell", "web shell"}},
	{[]string{"T1059"}, []string{"反弹shell", "reverse shell", "命令执行", "command execution"}},
	{[]string{"T1070"}, []string{"清理痕迹", "history -c", "日志删除", "anti-forensics"}},
}

// SuggestTechniques 从意图摘要/描述里给出候选技术 ID（最多 5 个，已归一化）。
// 只做子串匹配；没有命中的返回空切片——调用方必须把“无建议”如实展示。
func SuggestTechniques(text string) []string {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, h := range techniqueHints {
		hit := false
		for _, kw := range h.Keywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		for _, tech := range h.Techniques {
			if !seen[tech] {
				seen[tech] = true
				out = append(out, tech)
			}
		}
		if len(out) >= 5 {
			break
		}
	}
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}
