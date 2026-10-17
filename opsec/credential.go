// 凭据尝试的识别：这条命令是不是在**对某个账号试口令**，试的是谁、试哪个账号。
//
// 它与 Annotate 是两件事，刻意分开：Annotate 答「这一手会留下什么」（可检出性），这里答
// 「这一手在动谁的账号」（影响面）。后者要能被**计数**，所以必须抽出 (host, account)
// ——只判出「这是爆破」不够：护栏要拦的是「对同一个账号试太多次」，不知道账号就无从计数。
//
// 覆盖范围同样刻意保守：只认**语法可靠**的写法。抽不出 host 就不算识别（ok=false），
// 与 Annotate 的 Uncovered 是同一种诚实——认不出来就说认不出来，不假装覆盖了。
// 已知边界：手搓的 shell 循环（`for p in $(cat list); do ssh ...; done`）认不出来。
//
// 一条关键区分：**单次凭据使用**与**成批尝试**（Attempt.Spray）。
// `sshpass -p X ssh u@h` 是一次尝试；`hydra -P rockyou.txt` 是一次**调用**、几百次尝试。
// 按调用计数会让后者永远撞不到预算——所以成批尝试不参与计数，它单独就构成越界。
package opsec

import (
	"regexp"
	"strings"
)

// Attempt 是一次被识别出来的凭据尝试。
type Attempt struct {
	Host    string `json:"host"`
	Account string `json:"account,omitempty"`
	Tool    string `json:"tool"`
	// Spray 为真 = 这条命令**本身就是成批尝试**：要么是爆破工具，要么带了口令表/用户名表。
	// 一次调用可能是几百次真实尝试，按「次数」计没有意义——它单独就构成越界。
	Spray bool `json:"spray,omitempty"`
}

var (
	reIPLike   = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)
	reUserAt   = regexp.MustCompile(`^([A-Za-z0-9._\\-]+)@([A-Za-z0-9._-]+)`)
	reURLHost  = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://(?:[^/@\s]*@)?([A-Za-z0-9._-]+)`)
	reListFile = regexp.MustCompile(`(?i)\.(txt|lst|list|dict|wordlist|csv)$`)
)

// toolRe 编译工具名的匹配器。边界用显式的空白/分隔符而不是 \b：命令里常见
// `curl|bash`、`;nxc ` 这类紧贴写法，且工具名常带 `.py`/`-` 后缀（\b 在 `-` 处会断）。
func toolRe(alt string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[\s;&|(/])` + `(?:` + alt + `)` + `([\s;&|)]|$)`)
}

// flagVal 取 `-x value` / `-x=value` / `--x value` 形式的值。找不到返回空串。
func flagVal(ws []string, names ...string) string {
	for i, w := range ws {
		for _, n := range names {
			if w == n && i+1 < len(ws) {
				return strings.Trim(ws[i+1], `"'`)
			}
			if strings.HasPrefix(w, n+"=") {
				return strings.Trim(strings.TrimPrefix(w, n+"="), `"'`)
			}
		}
	}
	return ""
}

// colonVal 取 `/v:host` 这种"斜杠键:值"形式（xfreerdp / rdesktop 的写法）。
func colonVal(ws []string, key string) string {
	for _, w := range ws {
		if v, ok := strings.CutPrefix(w, "/"+key+":"); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// firstIPLike 取第一个像 IP 的词（hydra 一类把目标写成位置参数的工具用）。
func firstIPLike(ws []string) string {
	for _, w := range ws {
		w = strings.Trim(w, `"'`)
		if reIPLike.MatchString(w) {
			return w
		}
	}
	return ""
}

// protoHost 从 `ssh://10.0.0.5:22` 这种写法里取主机。
func protoHost(ws []string) string {
	for _, w := range ws {
		i := strings.Index(w, "://")
		if i < 0 {
			continue
		}
		rest := w[i+3:]
		if j := strings.IndexAny(rest, "/:"); j >= 0 {
			rest = rest[:j]
		}
		if rest != "" {
			return rest
		}
	}
	return ""
}

// urlHost 从任意位置的 URL 里取主机（curl/wget/psql 用）。
func urlHost(s string) string {
	if m := reURLHost.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	return ""
}

// userAtHost 从 `user@host` 形态取账号与主机。
func userAtHost(s string) (host, account string) {
	if m := reUserAt.FindStringSubmatch(s); len(m) == 3 {
		return m[2], m[1]
	}
	return "", ""
}

// credUser 从 `[domain/]user:pass` 取账号（去掉域名前缀）。
// 用**第一个**冒号切分：账号里不会出现冒号，而口令里经常有（`P@ss:word`）。
func credUser(s string) string {
	i := strings.Index(s, ":")
	if i < 0 {
		return ""
	}
	u := s[:i]
	if j := strings.LastIndexAny(u, `\/`); j >= 0 {
		u = u[j+1:]
	}
	return u
}

// looksLikeList 判断一个取值像不像"口令表/用户名表"（而不是单个口令）。
// 判据是**保守的**：只认扩展名与路径——把单口令误判成表会让护栏平白多问一句，
// 把表误判成单口令会让一次几百发的喷洒溜过去。前者代价小得多。
func looksLikeList(v string) bool {
	if v == "" {
		return false
	}
	if reListFile.MatchString(v) {
		return true
	}
	return strings.ContainsAny(v, `/\`) && !strings.Contains(v, ":")
}

// hostPortSplit 把 `host:port` 拆开（只取 host）。冒号后面还有冒号 = IPv6 字面量，
// 原样返回（拆不动就不拆，比拆错强）。
func hostPortSplit(s string) string {
	i := strings.LastIndex(s, ":")
	if i > 0 && !strings.Contains(s[i+1:], ":") {
		return s[:i]
	}
	return s
}

// credTool 是一条识别规则。
type credTool struct {
	Name  string
	Re    *regexp.Regexp
	Parse func(cmd string, ws []string) (host, account string)
	// AlwaysSpray：这个工具**本身就是**成批尝试（爆破器——它生来就是拿表打的）。
	AlwaysSpray bool
	// Spray：按调用判定（nxc 带了口令表才算，拿单口令打一发不算）。
	// AlwaysSpray 与 Spray 都为假 = 单次凭据使用。
	Spray func(ws []string) bool
}

// credTools 是识别表。顺序即优先级：先命中先返回。
//
// 词表与 opsec.Knowledge 的 `bruteforce` 条目同源但**不共用**——那条答"有多吵"，
// 这条答"动谁的账号"，两者的覆盖面本来就不一样（比如 sshpass 会动账号但不属于"爆破"）。
var credTools = []credTool{
	// 爆破器：本身就是成批尝试。
	{
		Name: "hydra/medusa/ncrack/patator",
		Re:   toolRe(`hydra|medusa|ncrack|patator`),
		Parse: func(_ string, ws []string) (string, string) {
			h := protoHost(ws)
			if h == "" {
				h = firstIPLike(ws)
			}
			// 只认单账号开关（hydra 的 -l / medusa 的 -u）；-L/-U 给的是**用户表**，
			// 把它当账号写进台账等于记了个不存在的名字。
			return h, flagVal(ws, "-l", "-u")
		},
		AlwaysSpray: true,
	},
	{
		Name: "kerbrute",
		Re:   toolRe(`kerbrute`),
		Parse: func(_ string, ws []string) (string, string) {
			h := flagVal(ws, "-d", "--dc")
			return h, flagVal(ws, "-l", "--user")
		},
		AlwaysSpray: true,
	},
	// NetExec / CrackMapExec：带表才算成批。
	{
		Name: "nxc/netexec/crackmapexec",
		Re:   toolRe(`nxc|netexec|crackmapexec|cme`),
		Parse: func(_ string, ws []string) (string, string) {
			protos := map[string]bool{"smb": true, "ssh": true, "winrm": true, "ldap": true,
				"mssql": true, "rdp": true, "ftp": true, "vnc": true, "wmi": true, "nfs": true}
			host := ""
			if len(ws) > 1 {
				if protos[strings.ToLower(ws[1])] {
					if len(ws) > 2 {
						host = ws[2]
					}
				} else {
					host = ws[1]
				}
			}
			return host, flagVal(ws, "-u", "--user", "--username", "--usernames")
		},
		Spray: func(ws []string) bool {
			for _, f := range []string{"-p", "-u", "--password", "--passwords", "--user", "--username", "--usernames"} {
				if looksLikeList(flagVal(ws, f)) {
					return true
				}
			}
			return false
		},
	},
	// 单次凭据使用（下同）：一次调用 = 一次尝试。
	{
		Name: "sshpass",
		Re:   toolRe(`sshpass`),
		Parse: func(_ string, ws []string) (string, string) {
			for _, w := range ws {
				if h, a := userAtHost(w); h != "" {
					return h, a
				}
			}
			return firstIPLike(ws), ""
		},
	},
	{
		Name: "impacket",
		Re:   toolRe(`impacket-[a-z]+`),
		Parse: func(_ string, ws []string) (string, string) {
			for _, w := range ws {
				w = strings.Trim(w, `"'`)
				if i := strings.LastIndex(w, "@"); i > 0 {
					host := hostPortSplit(w[i+1:])
					if host != "" {
						return host, credUser(w[:i])
					}
				}
			}
			return "", ""
		},
	},
	{
		Name: "evil-winrm",
		Re:   toolRe(`evil-winrm`),
		Parse: func(_ string, ws []string) (string, string) {
			return flagVal(ws, "-i"), flagVal(ws, "-u")
		},
	},
	{
		Name: "xfreerdp/rdesktop",
		Re:   toolRe(`xfreerdp|rdesktop`),
		Parse: func(_ string, ws []string) (string, string) {
			h := colonVal(ws, "v") // xfreerdp 的 /v:host
			if h == "" {
				h = firstIPLike(ws) // rdesktop 把目标写成位置参数
			}
			a := colonVal(ws, "u") // xfreerdp 的 /u:user
			if a == "" {
				a = flagVal(ws, "-u") // rdesktop 的 -u user
			}
			return hostPortSplit(h), a
		},
	},
	{
		Name: "smbclient",
		Re:   toolRe(`smbclient`),
		Parse: func(_ string, ws []string) (string, string) {
			host := ""
			for _, w := range ws {
				if v, ok := strings.CutPrefix(w, "//"); ok {
					if i := strings.Index(v, "/"); i >= 0 {
						v = v[:i]
					}
					host = v
					break
				}
			}
			acct := flagVal(ws, "-U", "--user")
			if i := strings.IndexAny(acct, "%:"); i >= 0 {
				acct = acct[:i]
			}
			if j := strings.LastIndexAny(acct, `\/`); j >= 0 {
				acct = acct[j+1:] // 去掉 `DOMAIN\user` 的域名前缀
			}
			return host, acct
		},
	},
	{
		Name: "mssqlclient",
		Re:   toolRe(`mssqlclient\.py|mssqlclient`),
		Parse: func(_ string, ws []string) (string, string) {
			for _, w := range ws {
				if i := strings.LastIndex(w, "@"); i > 0 {
					host := hostPortSplit(w[i+1:])
					if host != "" {
						return host, credUser(w[:i])
					}
				}
			}
			return "", ""
		},
	},
	{
		Name: "mysql",
		Re:   toolRe(`mysql`),
		Parse: func(_ string, ws []string) (string, string) {
			// 只认带了口令信号的调用：没有 -p 的 mysql 可能是 socket/免密，不算试口令。
			if flagVal(ws, "-p", "--password") == "" && !hasAttachedP(ws) {
				return "", ""
			}
			h := flagVal(ws, "-h", "--host")
			if h == "" {
				h = "localhost" // mysql 的默认目标就是本机
			}
			return hostPortSplit(h), flagVal(ws, "-u", "--user")
		},
	},
	{
		Name: "psql",
		Re:   toolRe(`psql`),
		Parse: func(cmd string, _ []string) (string, string) {
			h := urlHost(cmd)
			if h == "" {
				return "", ""
			}
			acct := ""
			if i := strings.Index(cmd, "://"); i >= 0 {
				rest := cmd[i+3:]
				if j := strings.Index(rest, "@"); j > 0 {
					acct = credUser(rest[:j])
				}
			}
			return h, acct
		},
	},
	{
		Name: "redis-cli",
		Re:   toolRe(`redis-cli`),
		Parse: func(_ string, ws []string) (string, string) {
			if flagVal(ws, "-a", "--pass") == "" {
				return "", ""
			}
			h := flagVal(ws, "-h")
			if h == "" {
				h = "localhost"
			}
			return hostPortSplit(h), ""
		},
	},
	{
		Name: "curl",
		Re:   toolRe(`curl`),
		Parse: func(cmd string, ws []string) (string, string) {
			cred := flagVal(ws, "-u", "--user")
			if cred == "" {
				return "", ""
			}
			acct := cred
			if i := strings.Index(acct, ":"); i >= 0 {
				acct = acct[:i]
			}
			return urlHost(cmd), acct
		},
	},
	{
		Name: "wget",
		Re:   toolRe(`wget`),
		Parse: func(cmd string, ws []string) (string, string) {
			if flagVal(ws, "--password", "--http-password") == "" {
				return "", ""
			}
			return urlHost(cmd), flagVal(ws, "--user", "--http-user")
		},
	},
}

// hasAttachedP 认 `-pPASS` 这种紧贴写法（mysql 常见）。
func hasAttachedP(ws []string) bool {
	for _, w := range ws {
		if len(w) > 2 && strings.HasPrefix(w, "-p") && !strings.HasPrefix(w, "--") {
			return true
		}
	}
	return false
}

// CredentialAttempt 识别一条命令是不是在试凭据，并抽出 (host, account)。
//
// 抽不出 host 时返回 ok=false：**认不出来就说认不出来**。宁可漏掉一条手搓的循环，
// 也不要凭猜测记一条"某人试了某账号"——这张表会进交付物，编不得。
func CredentialAttempt(command string) (Attempt, bool) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return Attempt{}, false
	}
	ws := strings.Fields(cmd)
	for _, ct := range credTools {
		if !ct.Re.MatchString(cmd) {
			continue
		}
		host, account := ct.Parse(cmd, ws)
		host = strings.Trim(host, `"'`)
		if host == "" {
			continue // 这一条认得出工具、认不出目标：继续试下一条，都不行就如实不识别
		}
		at := Attempt{Host: host, Account: account, Tool: ct.Name}
		at.Spray = ct.AlwaysSpray || (ct.Spray != nil && ct.Spray(ws))
		return at, true
	}
	return Attempt{}, false
}
