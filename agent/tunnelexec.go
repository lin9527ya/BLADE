package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// 隧道登记的标记：写进 tool_executions.description，概览据此把"现在通着哪几条隧道"
// 告诉规划层。隧道是**跨 worker 共享**的能力——A 开的隧道，B 也能用；看不见就没人用得上。
const tunnelDescPrefix = "隧道："

// tunnelDefaultTTL 是隧道的默认存活上限。没有上限的 ssh -D 会一直挂到进程被杀，
// 而收尾门只认"还有执行在跑"，一条被遗忘的隧道能让意图永远无法收尾。
const tunnelDefaultTTL = 2 * time.Hour

// tunnelOpen 经一条 **ssh 据点**开 SOCKS5 隧道，用来访问只有从该主机才能到达的内网目标。
//
// 平台只负责三件平台才做得到的事：用登记过的凭据建连（明文不进命令）、分配本地端口、
// 把"哪条隧道通着、地址是什么"写进概览。**怎么用**交给模型：curl 的
// `--socks5-hostname`、nmap 的 `proxychains4`——不同工具接代理的方式不一样，平台不假装
// 能透明改写所有命令。
func (t *ToolSet) tunnelOpen() actool.CoreTool {
	return writeTool("tunnel_open",
		"经一条 **ssh 据点**开一条 SOCKS5 隧道（ssh -D），用于访问只有从该主机才能到达的内网目标。\n"+
			"★ 据点必须是 protocol=ssh 且挂了凭据（口令/私钥由凭据节点提供，明文不进命令）。\n"+
			"★ 返回 socks5h://127.0.0.1:<port>：curl 用 --socks5-hostname、nmap/其它工具用 proxychains4，"+
			"平台不替你改写命令。\n"+
			"★ 隧道在后台跑：先 wait_execution(execution_id, 3000) 确认它**仍在 running**（转发失败会立刻退出），"+
			"再实际访问一次内网验证。用完用 cancel_execution 关掉——忘记关的隧道会挡住意图收尾。\n"+
			"★ 内网目标仍受授权范围约束：能不能打由 RoE 判定，隧道只解决「够不够得着」。",
		obj(map[string]any{
			"foothold_id": idp("据点节点 id（protocol=ssh，且带 credential_id）"),
			"host":        str("跳板机地址（必填；必须与据点登记的一致）"),
			"ttl_ms":      intp("隧道存活上限毫秒（默认 2 小时；到点进程被杀）"),
		}, "foothold_id", "host"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if !lootExecOn() {
				return actool.Errorf("tunnel_open: 该能力默认关闭（它会用凭据在目标上建立长连接）。" +
					"请让操作者在设置里打开 agent_loot_exec。"), nil
			}
			if !t.execToolsReady() {
				return actool.Errorf("tunnel_open: 执行服务未接线（当前运行环境不支持后台执行）"), nil
			}
			var a struct {
				FootholdID json.RawMessage `json:"foothold_id"`
				Host       string          `json:"host"`
				TTLMs      int             `json:"ttl_ms"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := pid(a.FootholdID)
			if id <= 0 {
				return actool.Errorf("tunnel_open: foothold_id 必填"), nil
			}
			host := strings.TrimSpace(a.Host)
			if host == "" {
				return actool.Errorf("tunnel_open: host 必填——平台要按它做授权范围校验，不替你猜目标"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindFoothold {
				return actool.Errorf(fmt.Sprintf("tunnel_open: %d 不是本任务的据点", id)), nil
			}
			if node.State != db.StateLootActive {
				return actool.Errorf(fmt.Sprintf("tunnel_open: 据点 %d 当前状态是 %q（已失效）", id, node.State)), nil
			}
			v := db.ParseFoothold(node)
			if v.Protocol != "ssh" {
				return actool.Errorf(fmt.Sprintf("tunnel_open: 据点 %d 的协议是 %q；隧道需要 protocol=ssh 且挂了凭据的据点", id, v.Protocol)), nil
			}
			if normalizeHost(v.Host) != normalizeHost(host) {
				return actool.Errorf(fmt.Sprintf("tunnel_open: host=%s 与据点登记的 %s 不一致", host, v.Host)), nil
			}
			cred, cerr := t.ts.GetNode(v.CredentialID)
			if cerr != nil || cred == nil || cred.Kind != db.KindCredential || cred.State != db.StateLootActive {
				return actool.Errorf(fmt.Sprintf("tunnel_open: 据点指向的凭据 %d 已不可用", v.CredentialID)), nil
			}
			cv := db.ParseCredential(cred)
			kind := strings.ToLower(strings.TrimSpace(cv.Kind))
			if kind != "password" && kind != "key" {
				return actool.Errorf(fmt.Sprintf("tunnel_open: 凭据 %d 的类型是 %q（隧道只支持 password / key）", v.CredentialID, kind)), nil
			}
			user := strings.TrimSpace(cv.Username)
			if user == "" {
				return actool.Errorf(fmt.Sprintf("tunnel_open: 凭据 %d 没有登记 username", v.CredentialID)), nil
			}
			localPort, perr := allocSocksPort()
			if perr != nil {
				return actool.Errorf("tunnel_open: 分配本地端口失败：" + perr.Error()), nil
			}
			secretPath, serr := writeLootSecret(cv.Secret)
			if serr != nil {
				return actool.Errorf("tunnel_open: 无法暂存凭据明文：" + serr.Error()), nil
			}
			ttl := tunnelDefaultTTL
			if a.TTLMs > 0 {
				ttl = time.Duration(a.TTLMs) * time.Millisecond
			}
			command := buildTunnelCommand(kind, user, v.Host, v.Port, localPort, secretPath)
			desc := fmt.Sprintf("%ssocks5h://127.0.0.1:%d ← 据点 #%d（%s@%s）", tunnelDescPrefix, localPort, id, user, v.Host)
			st, subErr := t.execService.Submit(ctx, t.taskIDStr(), t.ownerNode, t.worker, command, desc, ttl)
			if subErr != nil {
				_ = removeLootSecret(secretPath)
				return actool.Errorf("tunnel_open: " + subErr.Error()), nil
			}
			t.auditLootExec("tunnel_open",
				fmt.Sprintf("开隧道 socks5h://127.0.0.1:%d 经据点 %d", localPort, id),
				fmt.Sprintf("据点 #%d → %s@%s:%d，execution=%d，TTL=%s", id, user, v.Host, portOf("ssh", v.Port), st.ID, ttl))
			return actool.Text(fmt.Sprintf(
				"tunnel started: %d\nsocks5h://127.0.0.1:%d\n"+
					"用法：curl --socks5-hostname 127.0.0.1:%d <url>；nmap/其它工具用 proxychains4（先确认已安装）。\n"+
					"先 wait_execution(%d, 3000) 确认仍在 running（转发失败会立刻退出并给退出码），再实际访问一次内网验证；"+
					"不用了用 cancel_execution(%d) 关掉——忘记关的隧道会挡住意图收尾。",
				st.ID, localPort, localPort, st.ID, st.ID)), nil
		})
}

// allocSocksPort 向内核要一个空闲的本地端口（绑定 :0 后立刻释放）。
//
// 这里有一个小竞态：释放到 ssh 绑定之间，端口理论上可能被别的进程抢走。用
// `ExitOnForwardFailure=yes` 兜底——抢不到就立刻退出，而不是让一条"看起来通了、
// 其实没转发"的隧道挂在那里骗人。
func allocSocksPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected local address %v", l.Addr())
	}
	return addr.Port, nil
}

// buildTunnelCommand 构造 ssh 动态转发命令（-N -D）。明文处理与 loot_exec 同一套：
// 0600 临时文件、读走即删，命令里只有路径。
func buildTunnelCommand(kind, user, host string, sshPort, localPort int, secretPath string) string {
	sp := shQuote(secretPath)
	target := shQuote(user + "@" + host)
	opts := fmt.Sprintf("-N -D 127.0.0.1:%d -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null "+
		"-o ExitOnForwardFailure=yes -o ConnectTimeout=15 -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -p %d",
		localPort, portOf("ssh", sshPort))
	if kind == "key" {
		// 私钥要活到连接建立且认证完成；命令本身是长连接，退出时再删。
		return fmt.Sprintf("ssh -i %s %s %s; rc=$?; rm -f %s; exit $rc", sp, opts, target, sp)
	}
	return fmt.Sprintf(`SSHPASS="$(cat %s)"; rm -f %s; export SSHPASS; exec sshpass -e ssh %s %s`, sp, sp, opts, target)
}
