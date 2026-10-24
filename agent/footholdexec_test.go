package agent

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 据点结构化连法：只有**平台真的能执行**的协议才允许登记；填不了就别填，
// 不要留一个"看起来能用、实际用不了"的战果。
func TestReportFootholdValidatesStructuredAccess(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	call := func(body string) string {
		return callTool(t, tools.WorkerTools(), "report_foothold", body)
	}
	const base = `"summary":"web01 上的 root","evidence":"id → uid=0","confidence":"high"`
	if out := call(`{` + base + `,"protocol":"smb","host":"web01"}`); !strings.Contains(out, "暂不支持代为执行") {
		t.Fatalf("不支持的协议应被拒：%s", out)
	}
	if out := call(`{` + base + `,"host":"web01"}`); !strings.Contains(out, "却没写 protocol") {
		t.Fatalf("给了 host 却没给 protocol 应被拒：%s", out)
	}
	if out := call(`{` + base + `,"protocol":"ssh","host":"web01"}`); !strings.Contains(out, "必须给 credential_id") {
		t.Fatalf("ssh 缺凭据应被拒：%s", out)
	}
	if out := call(`{` + base + `,"protocol":"webshell"}`); !strings.Contains(out, "必须给 url") {
		t.Fatalf("webshell 缺 url 应被拒：%s", out)
	}

	credID := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_credential",
		`{"summary":"web01 的 app 口令","kind":"password","username":"app","secret":"Sup3rS3cret!",`+
			`"evidence":"cat /etc/app.conf","confidence":"high",`+
			`"targets":[{"host":"web01","port":22,"service":"ssh"}]}`))
	raw, _ := json.Marshal(credID)
	if out := call(`{` + base + `,"protocol":"ssh","host":"web01","credential_id":` + string(raw) + `}`); !strings.HasPrefix(out, "foothold recorded: ") {
		t.Fatalf("带凭据的 ssh 据点应可登记：%s", out)
	}
	// 结构化字段要真的落进 payload（否则 foothold_exec 拿不到连法）。
	nodes, err := ts.ListByKind(db.KindFoothold, 10)
	if err != nil || len(nodes) == 0 {
		t.Fatalf("据点未落库：%v", err)
	}
	v := db.ParseFoothold(nodes[0])
	if v.Protocol != "ssh" || v.Host != "web01" || v.Port != 22 || v.CredentialID != credID {
		t.Fatalf("结构化连法没落进 payload：%+v", v)
	}
}

// ssh 据点执行：走据点指向的凭据，明文不进命令与回执。
func TestFootholdExecSSHUsesCredential(t *testing.T) {
	withLootExec(t, true)
	tools, _, _ := newToolWorker(t)
	const secret = "Sup3rS3cret!"
	credID := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_credential",
		`{"summary":"web01 的 app 口令","kind":"password","username":"app","secret":"`+secret+`",`+
			`"evidence":"cat /etc/app.conf","confidence":"high",`+
			`"targets":[{"host":"web01","port":22,"service":"ssh"}]}`))
	credJSON, _ := json.Marshal(credID)
	fid := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_foothold",
		`{"summary":"web01 上的 app","evidence":"id","confidence":"high","protocol":"ssh","host":"web01","port":2222,`+
			`"credential_id":`+string(credJSON)+`}`))

	fake := &fakeExecService{submitID: 11}
	tools.SetExecService(fake)
	fidJSON, _ := json.Marshal(fid)
	out := callTool(t, tools.PlannerTools(), "foothold_exec",
		`{"foothold_id":`+string(fidJSON)+`,"host":"web01","command":"id; uname -a"}`)
	if !strings.Contains(out, "execution started:") {
		t.Fatalf("应起一条后台执行：%s", out)
	}
	cmd := fake.submits[0].command
	if strings.Contains(cmd, secret) {
		t.Fatalf("明文漏进了命令：%s", cmd)
	}
	if !strings.Contains(cmd, "sshpass -e") || !strings.Contains(cmd, "app@web01") || !strings.Contains(cmd, "-p 2222") {
		t.Fatalf("ssh 命令应带凭据账号、主机与登记端口：%s", cmd)
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

// webshell 据点执行：命令作为 --data-urlencode 的参数值整体交给 curl，
// 本地 shell 不解释其中的分号/引号（不把模型输出当代码执行）。
func TestFootholdExecWebshellQuotesCommand(t *testing.T) {
	withLootExec(t, true)
	tools, _, _ := newToolWorker(t)
	fid := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_foothold",
		`{"summary":"web01 的 upload 目录有 webshell","evidence":"curl → uid=33","confidence":"high",`+
			`"protocol":"webshell","url":"http://web01/uploads/x.php","param":"c"}`))
	fake := &fakeExecService{submitID: 12}
	tools.SetExecService(fake)
	fidJSON, _ := json.Marshal(fid)
	out := callTool(t, tools.PlannerTools(), "foothold_exec",
		`{"foothold_id":`+string(fidJSON)+`,"host":"web01","command":"id; cat /etc/passwd"}`)
	if !strings.Contains(out, "execution started:") {
		t.Fatalf("应起一条后台执行：%s", out)
	}
	cmd := fake.submits[0].command
	if !strings.Contains(cmd, "curl -sS --max-time") || !strings.Contains(cmd, "--data-urlencode") {
		t.Fatalf("应构造 curl 调用：%s", cmd)
	}
	if !strings.Contains(cmd, "'c=id; cat /etc/passwd'") || !strings.Contains(cmd, "'http://web01/uploads/x.php'") {
		t.Fatalf("命令应整体作为参数值、URL 单独引号包住：%s", cmd)
	}
}

// 不能用的情况要当场说清：没连法、host 不一致、开关关闭。
func TestFootholdExecRefusals(t *testing.T) {
	withLootExec(t, true)
	tools, _, _ := newToolWorker(t)
	tools.SetExecService(&fakeExecService{submitID: 13})
	free := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_foothold",
		`{"summary":"只有自由文本的据点","evidence":"id","confidence":"high","detail":"反弹 shell 在 4444"}`))
	ws := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_foothold",
		`{"summary":"webshell","evidence":"curl","confidence":"high","protocol":"webshell","url":"http://web01/x.php"}`))
	call := func(id int64, host string) string {
		raw, _ := json.Marshal(id)
		return callTool(t, tools.PlannerTools(), "foothold_exec", `{"foothold_id":`+string(raw)+`,"host":"`+host+`","command":"id"}`)
	}
	if out := call(free, "web01"); !strings.Contains(out, "没有结构化连法") {
		t.Fatalf("自由文本据点应说明无法代为执行：%s", out)
	}
	if out := call(ws, "db02"); !strings.Contains(out, "不一致") {
		t.Fatalf("host 不一致应被拒：%s", out)
	}
	withLootExec(t, false)
	if out := call(ws, "web01"); !strings.Contains(out, "默认关闭") {
		t.Fatalf("开关关闭时应明确拒绝：%s", out)
	}
}
