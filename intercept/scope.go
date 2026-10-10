package intercept

// RoE（授权范围）执行层：工具调用携带的网络目标若落在任务 task_scope 之外，在
// intercept 规则与 LLM 兜底裁判之前先行裁决（ask / deny）——授权边界高于一切
// 规则配置。默认 off；模式存 setting `intercept_scope_mode`（off|ask|deny），
// 环境变量 ARTEX_SCOPE_ENFORCEMENT 优先，供 .env 驱动的部署直接开启。
//
// 只在任务声明了可用的范围行（root_domain/subdomain/ip/cidr/keyword）时才生效；
// 纯 company/icp 行不参与主机匹配（公司展开由资产图侧完成），无可用范围行的任务
// 不改变原有行为。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

const settingScopeMode = "intercept_scope_mode"

// scopeCacheTTL bounds how long a task's scope rows are reused between checks
// (Bash calls are frequent; scope rows change rarely).
const scopeCacheTTL = 30 * time.Second

type scopeCacheEntry struct {
	rows []db.TaskScope
	at   time.Time
}

// ScopeMode resolves the enforcement mode: env override > DB setting > off.
func (i *Interceptor) ScopeMode() string {
	if v := strings.TrimSpace(os.Getenv("ARTEX_SCOPE_ENFORCEMENT")); v == "off" || v == "ask" || v == "deny" {
		return v
	}
	return i.getSettingChoice(settingScopeMode, "off", "off", "ask", "deny")
}

// shellCommandOf extracts the command surface a tool call carries, mirroring the
// guard's audit extraction (Bash + the interactive-shell tools).
func shellCommandOf(toolName string, input []byte) string {
	switch toolName {
	case "Bash", "shell_open":
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(input, &in)
		return in.Command
	case "shell_send":
		var in struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(input, &in)
		return in.Text
	}
	return ""
}

var (
	scopeURLRe    = regexp.MustCompile(`(?i)https?://([^\s/?#"'\x60]+)`)
	scopeIPRe     = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?:/\d{1,2})?\b`)
	scopeDomainRe = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:com\.cn|net\.cn|org\.cn|gov\.cn|edu\.cn|com|net|org|cn|io|gov|edu|mil|info|biz|me|co|top|xyz|shop|app|dev|tech|site|online|store|club|vip|pro|cc|tv|us|uk|jp|kr|de|fr|ru|hk|tw|sg|in|au|br|nl|eu|asia)\b`)
)

// localTargets are never treated as out-of-scope targets: loopback and the
// machine's own proxy surface are part of the platform, not the engagement.
func localTarget(host string) bool {
	h := strings.ToLower(host)
	return h == "localhost" || h == "::1" || strings.HasPrefix(h, "127.") || h == "0.0.0.0"
}

// stripBracketsPort drops [v6]:port brackets and a trailing :port from a
// host:port token, leaving the bare host. Bare v4/v6 values pass through.
func stripBracketsPort(v string) string {
	if strings.HasPrefix(v, "[") {
		if end := strings.Index(v, "]"); end > 0 {
			return v[1:end]
		}
		return strings.Trim(v, "[]")
	}
	if strings.Count(v, ":") == 1 {
		if _, _, err := net.SplitHostPort(v); err == nil {
			return v[:strings.LastIndex(v, ":")]
		}
	}
	return v
}

// ExtractTargets pulls the network targets (hosts / IPs / CIDRs) out of a shell
// command. Loopback and the local recording proxy are never treated as targets.
func ExtractTargets(cmd string) []string {
	if cmd == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		host := strings.ToLower(stripBracketsPort(raw))
		host = strings.TrimSuffix(host, ".")
		if host == "" || localTarget(host) || seen[host] {
			return
		}
		seen[host] = true
		out = append(out, host)
	}
	for _, m := range scopeURLRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1])
	}
	for _, m := range scopeIPRe.FindAllString(cmd, -1) {
		if strings.Contains(m, "/") {
			if _, _, err := net.ParseCIDR(m); err == nil {
				add(m)
			}
			continue
		}
		if net.ParseIP(m) != nil {
			add(m)
		}
	}
	for _, m := range scopeDomainRe.FindAllString(cmd, -1) {
		add(m)
	}
	return out
}

// usableScope keeps only the scope rows that can be matched against a network
// host; company/icp rows do not participate (see the package comment).
func usableScope(rows []db.TaskScope) []db.TaskScope {
	out := make([]db.TaskScope, 0, len(rows))
	for _, r := range rows {
		switch r.Kind {
		case "root_domain", "subdomain", "ip", "cidr", "keyword":
			if scopeValue(r) != "" {
				out = append(out, r)
			}
		}
	}
	return out
}

func scopeValue(r db.TaskScope) string {
	for _, v := range []string{r.Domain, r.Net, r.Value} {
		if s := strings.TrimSpace(v); s != "" {
			return strings.ToLower(s)
		}
	}
	return ""
}

// targetInScope reports whether one target is covered by the usable scope rows:
// domain/subdomain suffix match, ip equality, cidr containment (a cidr TARGET
// must sit fully inside a scope cidr), keyword substring.
func targetInScope(rows []db.TaskScope, target string) bool {
	t := strings.ToLower(target)
	for _, r := range rows {
		v := scopeValue(r)
		if v == "" {
			continue
		}
		switch r.Kind {
		case "root_domain", "subdomain":
			if t == v || strings.HasSuffix(t, "."+v) {
				return true
			}
		case "ip":
			if t == v {
				return true
			}
		case "cidr":
			_, rn, err := net.ParseCIDR(v)
			if err != nil {
				continue
			}
			if ip := net.ParseIP(t); ip != nil {
				if rn.Contains(ip) {
					return true
				}
				continue
			}
			if _, tn, err := net.ParseCIDR(t); err == nil {
				tOnes, _ := tn.Mask.Size()
				rOnes, _ := rn.Mask.Size()
				if rn.Contains(tn.IP) && tOnes >= rOnes {
					return true
				}
			}
		case "keyword":
			if strings.Contains(t, v) {
				return true
			}
		}
	}
	return false
}

// scopeRows loads one task's scope rows through a tiny TTL cache.
func (i *Interceptor) scopeRows(taskID int64) ([]db.TaskScope, error) {
	i.scopeMu.Lock()
	entry, ok := i.scopeCache[taskID]
	i.scopeMu.Unlock()
	if ok && time.Since(entry.at) < scopeCacheTTL {
		return entry.rows, nil
	}
	rows, err := i.db.Assets().ListTaskScope(taskID)
	if err != nil {
		return nil, err
	}
	i.scopeMu.Lock()
	if i.scopeCache == nil {
		i.scopeCache = map[int64]scopeCacheEntry{}
	}
	i.scopeCache[taskID] = scopeCacheEntry{rows: rows, at: time.Now()}
	i.scopeMu.Unlock()
	return rows, nil
}

// CheckScope gates one tool call on the task's authorization scope. It returns
// (decision, true) only when the call must be gated (ask/deny); in-scope calls,
// tasks without usable scope rows, unsupported tools, mode=off, and DB errors
// all return (zero, false) — enforcement fails open, matching the judge's
// failure behavior.
func (i *Interceptor) CheckScope(ctx context.Context, taskID int64, toolName string, input json.RawMessage) (Decision, bool) {
	mode := i.ScopeMode()
	if mode == "off" || taskID == 0 {
		return Decision{}, false
	}
	cmd := shellCommandOf(toolName, input)
	if cmd == "" {
		return Decision{}, false
	}
	targets := ExtractTargets(cmd)
	if len(targets) == 0 {
		return Decision{}, false
	}
	rows, err := i.scopeRows(taskID)
	if err != nil {
		return Decision{}, false
	}
	usable := usableScope(rows)
	if len(usable) == 0 {
		return Decision{}, false
	}
	var outside []string
	for _, t := range targets {
		if !targetInScope(usable, t) {
			outside = append(outside, t)
		}
	}
	if len(outside) == 0 {
		return Decision{}, false
	}
	return Decision{
		Action:   mode,
		RuleName: "[RoE] 授权范围",
		Message:  fmt.Sprintf("越界：%s 不在任务授权范围（task_scope）内", strings.Join(outside, ", ")),
	}, true
}
