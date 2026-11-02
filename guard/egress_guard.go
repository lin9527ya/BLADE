package guard

import (
	"regexp"
	"strings"
)

// 直连防护：测试流量必须走平台配置的出口代理。命令里出现**显式绕过代理**的写法时，
// 目标看到的会是本机真实 IP —— 这既泄漏操作者位置，也绕过了流量留痕。这类命令不做
// 硬拒，而是转人工审批：确有直连必要（内网目标、本地调试）时由人放行。
//
// 只认**明确的绕过写法**，不猜测命令去连哪里：
//   - curl/wget 的 --noproxy / --no-proxy（值为 * 或非本机主机时）
//   - NO_PROXY/no_proxy 赋值为 * 或非本机主机
//   - 把 HTTP(S)_PROXY/ALL_PROXY 赋空，或 env -u / unset 掉
//   - curl -x '' / --proxy ''（清空代理）
//
// 目标纯为本机地址（localhost/127.0.0.1/::1）时放行：本机调试不涉及真实出口 IP。

var (
	// --noproxy / --no-proxy，值可带引号。
	bypassFlagRE = regexp.MustCompile(`(?i)(?:^|[\s;|&])(?:--no[_-]?proxy|--no-proxy)(?:=|\s+)?("[^"]*"|'[^']*'|[^\s;|&]*)`)
	// NO_PROXY=...（curl/wget/python 都读这个变量）。
	bypassNoProxyRE = regexp.MustCompile(`(?i)(?:^|[\s;|&])(?:no_proxy)\s*=\s*("[^"]*"|'[^']*'|[^\s;|&]*)`)
	// HTTP_PROXY='' 之类的清空写法。
	bypassEmptyProxyRE = regexp.MustCompile(`(?i)(?:^|[\s;|&])(?:https?_proxy|all_proxy|http_proxy)\s*=\s*("[^"]*"|'[^']*')`)
	// env -u HTTP_PROXY / unset https_proxy
	bypassUnsetRE = regexp.MustCompile(`(?i)\b(?:env\s+(?:-u|--unset)[\s=]+|unset\s+)(?:https?_proxy|all_proxy|http_proxy)\b`)
	// curl -x '' / --proxy ''
	bypassClearProxyRE = regexp.MustCompile(`(?i)(?:^|[\s;|&])(?:-x|--proxy)(?:=|\s+)(""|'')`)
	commandURLHostRE   = regexp.MustCompile(`(?i)\bhttps?://([^/\s'"<>]+)`)
)

// detectProxyBypass 返回人话原因（命中时）。只在命令**显式**绕过代理且目标不是纯本机
// 地址时命中。
func detectProxyBypass(cmd string) (string, bool) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", false
	}
	hit := false
	for _, m := range bypassFlagRE.FindAllStringSubmatch(cmd, -1) {
		if !localOnlyList(trimQuotes(m[1])) {
			hit = true
			break
		}
	}
	if !hit {
		for _, m := range bypassNoProxyRE.FindAllStringSubmatch(cmd, -1) {
			if !localOnlyList(trimQuotes(m[1])) {
				hit = true
				break
			}
		}
	}
	if !hit && (bypassUnsetRE.MatchString(cmd) || bypassClearProxyRE.MatchString(cmd)) {
		hit = true
	}
	if !hit {
		for _, m := range bypassEmptyProxyRE.FindAllStringSubmatch(cmd, -1) {
			if strings.TrimSpace(trimQuotes(m[1])) == "" {
				hit = true
				break
			}
		}
	}
	if !hit {
		return "", false
	}
	if commandOnlyLocalTargets(cmd) {
		return "", false
	}
	return "命令显式绕过平台出口代理（--noproxy / NO_PROXY / 清空代理变量），会以本机真实 IP 直连目标并绕过流量留痕", true
}

// localOnlyList 判断 --noproxy / NO_PROXY 的值是否只包含本机地址。
// 空值不是绕过（没有排除任何主机）；`*` 一定不是。
func localOnlyList(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	for _, part := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		if !isLocalHost(part) {
			return false
		}
	}
	return true
}

// commandOnlyLocalTargets 在命令里能抽到的主机**全是**本机地址时返回 true。
// 抽不到任何主机时返回 false（保守：宁可问一次人，也不要让真实 IP 直接出网）。
func commandOnlyLocalTargets(cmd string) bool {
	found := false
	for _, m := range commandURLHostRE.FindAllStringSubmatch(cmd, -1) {
		found = true
		if !isLocalHost(m[1]) {
			return false
		}
	}
	return found
}

func isLocalHost(h string) bool {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.LastIndex(h, "@"); i >= 0 { // user:pass@host
		h = h[i+1:]
	}
	if strings.HasPrefix(h, "[") { // [::1]:8080
		if end := strings.Index(h, "]"); end > 0 {
			h = h[1:end]
		}
	} else if i := strings.LastIndex(h, ":"); i > 0 && strings.Count(h, ":") == 1 {
		h = h[:i] // host:port
	}
	switch {
	case h == "localhost", h == "::1", h == "0.0.0.0", h == "127.0.0.1":
		return true
	case strings.HasPrefix(h, "127."):
		return true
	case strings.HasSuffix(h, ".localhost"):
		return true
	}
	return false
}

func trimQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
