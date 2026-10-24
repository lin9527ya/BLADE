package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func reportSSHCred(t *testing.T, tools *ToolSet, secret string) int64 {
	t.Helper()
	return lootNodeID(t, callTool(t, tools.WorkerTools(), "report_credential",
		`{"summary":"web01 的口令","kind":"password","username":"app","secret":"`+secret+`",`+
			`"evidence":"cat","confidence":"high","targets":[{"host":"web01","port":22,"service":"ssh"}]}`))
}

// ssh 上传：scp + 明文文件（命令串里没有明文），本地路径限定在工作目录内。
func TestLootUploadSSH(t *testing.T) {
	withLootExec(t, true)
	tools, _, _ := newToolWorker(t)
	const secret = "Sup3rS3cret!"
	credID := reportSSHCred(t, tools, secret)
	fake := &fakeExecService{submitID: 41}
	tools.SetExecService(fake)
	raw, _ := json.Marshal(credID)
	out := callTool(t, tools.PlannerTools(), "loot_upload",
		`{"loot_id":`+string(raw)+`,"host":"web01","local_path":"payloads/x.sh","remote_path":"/tmp/x.sh"}`)
	if !strings.Contains(out, "execution started:") {
		t.Fatalf("应起一条后台传输：%s", out)
	}
	cmd := fake.submits[len(fake.submits)-1].command
	if strings.Contains(cmd, secret) {
		t.Fatalf("明文漏进了命令：%s", cmd)
	}
	for _, want := range []string{"scp", "-P 22", "'app@web01':'/tmp/x.sh'", "'payloads/x.sh'", "sshpass -e"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("scp 命令缺少 %q：%s", want, cmd)
		}
	}
	if m := regexp.MustCompile(`'([^']*blade-loot-[^']*/secret)'`).FindStringSubmatch(cmd); m != nil {
		t.Cleanup(func() { _ = os.RemoveAll(strings.TrimSuffix(m[1], "/secret")) })
		if _, err := os.Stat(m[1]); err != nil {
			t.Fatalf("提交时凭据文件应存在：%v", err)
		}
	} else {
		t.Fatalf("命令里应有临时凭据文件：%s", cmd)
	}
}

// 本地路径是安全边界：绝对路径 / .. 一律拒绝——提示注入不能把它变成宿主机外泄通道。
func TestLootTransferRejectsEscapingPaths(t *testing.T) {
	withLootExec(t, true)
	tools, _, _ := newToolWorker(t)
	credID := reportSSHCred(t, tools, "x")
	tools.SetExecService(&fakeExecService{submitID: 42})
	raw, _ := json.Marshal(credID)
	call := func(local string) string {
		return callTool(t, tools.PlannerTools(), "loot_download",
			`{"loot_id":`+string(raw)+`,"host":"web01","remote_path":"/etc/passwd","local_path":"`+local+`"}`)
	}
	for _, bad := range []string{"/etc/passwd", "~/x", "../etc/passwd", "a/../../b"} {
		if out := call(bad); !strings.Contains(out, "相对路径") && !strings.Contains(out, "..") {
			t.Fatalf("逃逸路径 %q 应被拒：%s", bad, out)
		}
	}
	// 正常相对路径放行，且下载分支带大小守卫。
	out := call("loot/passwd.txt")
	if !strings.Contains(out, "execution started:") {
		t.Fatalf("正常相对路径应放行：%s", out)
	}
}

// smb 下载：凭据走 -A 文件（不进命令行），smbclient 的 -c 里用它的命令语法。
func TestLootDownloadSMB(t *testing.T) {
	withLootExec(t, true)
	tools, _, _ := newToolWorker(t)
	credID := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_credential",
		`{"summary":"web01 的域账号","kind":"password","username":"CORP\\app","secret":"P@ss",`+
			`"evidence":"dump","confidence":"high","targets":[{"host":"web01","port":445,"service":"smb"}]}`))
	fake := &fakeExecService{submitID: 43}
	tools.SetExecService(fake)
	raw, _ := json.Marshal(credID)
	out := callTool(t, tools.PlannerTools(), "loot_download",
		`{"loot_id":`+string(raw)+`,"host":"web01","share":"C$","remote_path":"Windows\\win.ini","local_path":"loot/win.ini"}`)
	if !strings.Contains(out, "execution started:") {
		t.Fatalf("smb 下载应可执行：%s", out)
	}
	cmd := fake.submits[len(fake.submits)-1].command
	if strings.Contains(cmd, "P@ss") {
		t.Fatalf("明文漏进了命令：%s", cmd)
	}
	if !strings.Contains(cmd, "smbclient '//web01/C$'") || !strings.Contains(cmd, `-c 'get "loot/win.ini" "Windows\win.ini"'`) {
		t.Fatalf("smbclient 命令形状不对：%s", cmd)
	}
	if !strings.Contains(cmd, "download exceeds") {
		t.Fatalf("下载应带大小守卫：%s", cmd)
	}
	m := regexp.MustCompile(`-A '([^']+)'`).FindStringSubmatch(cmd)
	if m == nil {
		t.Fatalf("凭据应走 -A 文件：%s", cmd)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(m[1])) })
	body, err := os.ReadFile(m[1])
	if err != nil {
		t.Fatalf("读凭据文件：%v", err)
	}
	if !strings.Contains(string(body), "username = CORP\\app") || !strings.Contains(string(body), "password = P@ss") {
		t.Fatalf("凭据文件内容不对：%s", body)
	}
	if fi, err := os.Stat(m[1]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("凭据文件应为 0600：%v %v", fi, err)
	}
}

// 哈希不能用于 smbclient：直接说清，不猜。
func TestLootTransferRejectsHashForSMB(t *testing.T) {
	withLootExec(t, true)
	tools, _, _ := newToolWorker(t)
	credID := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_credential",
		`{"summary":"web01 的哈希","kind":"hash","username":"app","secret":"aad3b435b51404ee:31d6cfe0d16ae931b73c59d7e0c089c0",`+
			`"evidence":"dump","confidence":"high","targets":[{"host":"web01","port":445,"service":"smb"}]}`))
	tools.SetExecService(&fakeExecService{submitID: 44})
	raw, _ := json.Marshal(credID)
	out := callTool(t, tools.PlannerTools(), "loot_upload",
		`{"loot_id":`+string(raw)+`,"host":"web01","share":"C$","local_path":"x.sh","remote_path":"x.sh"}`)
	if !strings.Contains(out, "只支持 password") {
		t.Fatalf("哈希传输应被拒并说明：%s", out)
	}
}
