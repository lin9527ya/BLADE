package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// 文件传输：上传载荷/脚本、取回战利品（配置、SAM、数据库导出）。
//
// 为什么不做"交互式 shell 会话"：agent 的调用模型是一次一条命令，pty 会话既没有台账
// 也没有收尾门；evil-winrm 那类交互 shell 能做的事，nxc winrm -x 一条命令已经覆盖。
// RDP 更是只对人有用。真正缺的是**把文件送上去/拿回来**——此前只能让模型自己拼
// base64/curl 管道，SMB 侧则完全没有原语。

const lootTransferMaxBytes = 64 << 20 // 单次传输上限（默认 64MB）

// lootUpload 把**本意图工作目录里的文件**送到目标上。
func (t *ToolSet) lootUpload() actool.CoreTool {
	return t.writeExpTool("loot_upload",
		"把本意图工作目录里的文件送到目标上（ssh→scp / smb→smbclient）。用于上传载荷、脚本、工具。"+
			"\n★ local_path 必须是**相对路径**（相对本意图工作目录）：不允许绝对路径或 ..，平台不会把宿主机的任意文件送出去。"+
			"\n★ 凭据沿用 loot_id（report_credential 登记过、且 targets 里有这台 host）。"+
			"\n★ 后台执行：返回 execution_id，用 get_execution 看结果。",
		obj(map[string]any{
			"loot_id":     idp("凭据节点 id（必须是在手且有效的）"),
			"host":        str("目标主机（必填；必须在凭据 targets 里）"),
			"local_path":  str("本地文件（**相对路径**，如 payloads/x.sh）"),
			"remote_path": str("目标上的路径（如 /tmp/x.sh 或 C$/temp/x.exe 里的文件名部分）"),
			"share":       str("SMB 共享名（protocol=smb 时必填，如 C$、ADMIN$、share）"),
			"timeout_ms":  intp("可选硬超时毫秒"),
		}, "loot_id", "host", "local_path", "remote_path"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return t.lootTransfer(ctx, in, true)
		})
}

// lootDownload 把目标上的文件取回本意图工作目录。
func (t *ToolSet) lootDownload() actool.CoreTool {
	return t.writeExpTool("loot_download",
		"从目标上取回文件（ssh→scp / smb→smbclient），落在本意图工作目录里。用于取回配置、凭据文件、数据库导出等战利品。"+
			"\n★ local_path 必须是**相对路径**（相对本意图工作目录）。"+
			"\n★ 单次上限 64MB（max_bytes 可调，上限 256MB）：超限会删掉半截文件并报错，避免把磁盘写满。"+
			"\n★ 后台执行：返回 execution_id；取回后用 Read/Bash 读它，或直接 grep_execution 查传输日志。",
		obj(map[string]any{
			"loot_id":     idp("凭据节点 id（必须是在手且有效的）"),
			"host":        str("目标主机（必填；必须在凭据 targets 里）"),
			"remote_path": str("目标上的文件路径"),
			"local_path":  str("本地落点（**相对路径**，如 loot/web.config）"),
			"share":       str("SMB 共享名（protocol=smb 时必填）"),
			"max_bytes":   intp("单次上限字节数（默认 64MB，最大 256MB）"),
			"timeout_ms":  intp("可选硬超时毫秒"),
		}, "loot_id", "host", "remote_path", "local_path"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return t.lootTransfer(ctx, in, false)
		})
}

// lootTransfer 是上传/下载的共同实现（两个工具的输入形状几乎一样）。
func (t *ToolSet) lootTransfer(ctx context.Context, in json.RawMessage, upload bool) (actool.Result, error) {
	name := "loot_download"
	if upload {
		name = "loot_upload"
	}
	if !lootExecOn() {
		return actool.Errorf(name + ": 该能力默认关闭（它会把库里的凭据直接变成对目标的动作）。" +
			"请让操作者在设置里打开 agent_loot_exec。"), nil
	}
	if !t.execToolsReady() {
		return actool.Errorf(name + ": 执行服务未接线（当前运行环境不支持后台执行）"), nil
	}
	var a struct {
		LootID     json.RawMessage `json:"loot_id"`
		Host       string          `json:"host"`
		LocalPath  string          `json:"local_path"`
		RemotePath string          `json:"remote_path"`
		Share      string          `json:"share"`
		MaxBytes   int             `json:"max_bytes"`
		TimeoutMs  int             `json:"timeout_ms"`
	}
	if err := json.Unmarshal(in, &a); err != nil {
		return actool.Errorf(err.Error()), nil
	}
	host, err := transferHost(name, a.Host)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	local, err := safeRelativePath(name, "local_path", a.LocalPath)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	remote := strings.TrimSpace(a.RemotePath)
	if remote == "" {
		return actool.Errorf(name + ": remote_path 必填"), nil
	}
	node, cerr := t.ts.GetNode(pid(a.LootID))
	if cerr != nil || node == nil || node.Kind != db.KindCredential {
		return actool.Errorf(name + ": loot_id 必须是一条本任务的凭据节点"), nil
	}
	if node.State != db.StateLootActive {
		return actool.Errorf(fmt.Sprintf("%s: 凭据 %d 当前状态是 %q（已失效）", name, node.ID, node.State)), nil
	}
	v := db.ParseCredential(node)
	kind := strings.ToLower(strings.TrimSpace(v.Kind))
	target, terr := pickCredentialTarget(v, host)
	if terr != nil {
		return actool.Errorf(name + ": " + terr.Error()), nil
	}
	service := strings.ToLower(strings.TrimSpace(target.Service))
	if service == "" {
		service = defaultServiceForPort(target.Port)
	}
	user := strings.TrimSpace(v.Username)
	if user == "" {
		return actool.Errorf(fmt.Sprintf("%s: 凭据 %d 没有登记 username", name, node.ID)), nil
	}

	maxBytes := a.MaxBytes
	if maxBytes <= 0 {
		maxBytes = lootTransferMaxBytes
	}
	if maxBytes > 4*lootTransferMaxBytes {
		maxBytes = 4 * lootTransferMaxBytes
	}

	var command string
	var cleanup func()
	switch service {
	case "ssh":
		if kind != "password" && kind != "key" {
			return actool.Errorf(fmt.Sprintf("%s: ssh 传输只支持 password / key 型凭据（收到 %q）", name, kind)), nil
		}
		sp, serr := writeLootSecret(v.Secret)
		if serr != nil {
			return actool.Errorf(name + ": 无法暂存凭据明文：" + serr.Error()), nil
		}
		cleanup = func() { _ = removeLootSecret(sp) }
		command = buildSCPCommand(kind, user, target.Host, portOf("ssh", target.Port), sp, local, remote, upload, maxBytes)
	case "smb":
		if kind != "password" {
			// smbclient 不支持传递哈希（impacket-smbclient 支持）。不猜，直接说清。
			return actool.Errorf(fmt.Sprintf("%s: smb 文件传输只支持 password 型凭据（收到 %q）；"+
				"哈希请用 impacket-smbclient 自己构造", name, kind)), nil
		}
		share := strings.TrimSpace(a.Share)
		if share == "" {
			return actool.Errorf(name + ": protocol=smb 时必须给 share（如 C$、ADMIN$ 或共享名）"), nil
		}
		if strings.ContainsAny(remote+local+share, "\"\n") {
			return actool.Errorf(name + ": 路径里不能有双引号或换行"), nil
		}
		authPath, aerr := writeLootScript("username = " + user + "\npassword = " + v.Secret + "\n")
		if aerr != nil {
			return actool.Errorf(name + ": 无法暂存凭据：" + aerr.Error()), nil
		}
		cleanup = func() { _ = removeLootSecret(authPath) }
		command = buildSmbClientCommand(target.Host, share, authPath, local, remote, upload, maxBytes)
	default:
		return actool.Errorf(fmt.Sprintf("%s: 目标服务是 %q；文件传输支持 ssh / smb，其它服务请用 Bash 自己构造", name, service)), nil
	}

	var timeout time.Duration
	if a.TimeoutMs > 0 {
		timeout = time.Duration(a.TimeoutMs) * time.Millisecond
	}
	desc := fmt.Sprintf("%s：%s %s → %s@%s", name, map[bool]string{true: "上传", false: "下载"}[upload], local, user, target.Host)
	st, subErr := t.execService.Submit(ctx, t.taskIDStr(), t.ownerNode, t.worker, command, desc, timeout)
	if subErr != nil {
		cleanup()
		return actool.Errorf(name + ": " + subErr.Error()), nil
	}
	t.auditLootExec(name,
		fmt.Sprintf("文件传输：%s %s@%s:%s", map[bool]string{true: "上传", false: "下载"}[upload], user, target.Host, remote),
		fmt.Sprintf("凭据 #%d（%s），本地 %s，远端 %s", node.ID, v.Fingerprint(), local, remote))
	raw, _ := json.Marshal(st)
	return actool.Text(fmt.Sprintf("execution started: %d\n%s\n%s 已在后台执行；用 get_execution(%d) 看结果（失败原因写在输出里）。",
		st.ID, raw, name, st.ID)), nil
}

// transferHost 校验 host 必填（RoE 校验的对象）。
func transferHost(tool, host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("%s: host 必填——平台要按它做授权范围校验，不替你猜目标", tool)
	}
	return host, nil
}

// safeRelativePath 把本地路径限制在**本意图工作目录**内。
//
// 这是本工具的安全边界：托管执行的 cwd 就是意图工作目录，所以相对路径天然落在里面；
// 而绝对路径会让 agent 把宿主机上的任意文件（配置、密钥、数据库）传出去——一条
// 提示注入就能变成外泄通道。宁可直接拒绝，也不做"猜测用户意图"的路径解析。
func safeRelativePath(tool, field, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("%s: %s 必填", tool, field)
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.Contains(p, ":\\") {
		return "", fmt.Errorf("%s: %s 必须是**相对路径**（相对本意图工作目录）：绝对路径会让平台把宿主机文件送出去，已拒绝", tool, field)
	}
	for _, part := range strings.Split(path.Clean(p), "/") {
		if part == ".." {
			return "", fmt.Errorf("%s: %s 不能包含 ..（逃出工作目录），已拒绝", tool, field)
		}
	}
	return path.Clean(p), nil
}

// buildSCPCommand 构造 scp 传输命令。明文沿用 loot_exec 的处理：0600 文件 + 读完即删。
func buildSCPCommand(kind, user, host string, port int, secretPath, local, remote string, upload bool, maxBytes int) string {
	sp := shQuote(secretPath)
	target := shQuote(user + "@" + host)
	opts := fmt.Sprintf("-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -P %d", port)
	var args string
	if upload {
		args = fmt.Sprintf("%s %s:%s", shQuote(local), target, shQuote(remote))
	} else {
		args = fmt.Sprintf("%s:%s %s", target, shQuote(remote), shQuote(local))
	}
	guard := ""
	if !upload {
		guard = sizeGuard(local, maxBytes)
	}
	if kind == "key" {
		// 私钥要活到传输结束；命令退出时再删。
		return fmt.Sprintf("scp -i %s %s %s; rc=$?; rm -f %s%s; exit $rc", sp, opts, args, sp, guard)
	}
	// 口令通过环境变量交给 **sshpass**（scp 本身不读 SSHPASS）：明文文件读走即删。
	prefix := fmt.Sprintf(`SSHPASS="$(cat %s)"; rm -f %s; export SSHPASS; `, sp, sp)
	return prefix + fmt.Sprintf("sshpass -e scp %s %s%s; rc=$?; exit $rc", opts, args, guard)
}

// sizeGuard 生成下载后的大小检查：超限删掉半截文件并失败，避免把磁盘写满。
// 注意 %%s 是给 shell 的 stat 格式串转义。
func sizeGuard(local string, maxBytes int) string {
	return fmt.Sprintf(`; f=%s; if [ -f "$f" ] && [ "$(stat -c %%s "$f")" -gt %d ]; then rm -f "$f"; echo "download exceeds %d bytes"; exit 1; fi`,
		shQuote(local), maxBytes, maxBytes)
}

// buildSmbClientCommand 构造 smbclient 传输命令。凭据走 -A 文件（**不进命令行**）。
//
// 注意 -c 里是 smbclient 自己的命令语法（不是 shell）：文件名用双引号包住，
// 整个 -c 参数再由 shQuote 交给 shell——两层都不会把模型输出当代码执行。
func buildSmbClientCommand(host, share, authPath, local, remote string, upload bool, maxBytes int) string {
	verb := "put"
	if !upload {
		verb = "get"
	}
	cmd := fmt.Sprintf(`%s "%s" "%s"`, verb, local, remote)
	guard := ""
	if !upload {
		guard = sizeGuard(local, maxBytes)
	}
	return fmt.Sprintf("smbclient %s -A %s -c %s%s; rc=$?; rm -f %s; exit $rc",
		shQuote("//"+host+"/"+share), shQuote(authPath), shQuote(cmd), guard, shQuote(authPath))
}
