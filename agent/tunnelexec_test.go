package agent

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 隧道：经 ssh 据点开 SOCKS，明文不进命令，返回可用的 socks 地址。
func TestTunnelOpenStartsSocksForward(t *testing.T) {
	withLootExec(t, true)
	tools, _, _ := newToolWorker(t)
	const secret = "Sup3rS3cret!"
	credID := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_credential",
		`{"summary":"web01 的 app 口令","kind":"password","username":"app","secret":"`+secret+`",`+
			`"evidence":"cat /etc/app.conf","confidence":"high",`+
			`"targets":[{"host":"web01","port":22,"service":"ssh"}]}`))
	credJSON, _ := json.Marshal(credID)
	fid := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_foothold",
		`{"summary":"web01 上的 app","evidence":"id","confidence":"high","protocol":"ssh","host":"web01","port":22,`+
			`"credential_id":`+string(credJSON)+`}`))

	fake := &fakeExecService{submitID: 21}
	tools.SetExecService(fake)
	fidJSON, _ := json.Marshal(fid)
	out := callTool(t, tools.WorkerTools(), "tunnel_open", `{"foothold_id":`+string(fidJSON)+`,"host":"web01"}`)
	if !strings.Contains(out, "tunnel started:") {
		t.Fatalf("应起一条隧道：%s", out)
	}
	m := regexp.MustCompile(`socks5h://127\.0\.0\.1:(\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("回执里应给出 socks 地址：%s", out)
	}
	port := m[1]
	cmd := fake.submits[0].command
	for _, want := range []string{"-N -D 127.0.0.1:" + port, "ExitOnForwardFailure=yes", "sshpass -e", "app@web01"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("隧道命令缺少 %q：%s", want, cmd)
		}
	}
	if strings.Contains(cmd, secret) {
		t.Fatalf("明文漏进了命令：%s", cmd)
	}
	if !strings.HasPrefix(fake.submits[0].desc, tunnelDescPrefix) {
		t.Fatalf("描述应带隧道标记（概览据此登记）：%q", fake.submits[0].desc)
	}
	if sm := regexp.MustCompile(`'([^']*blade-loot-[^']*/secret)'`).FindStringSubmatch(cmd); sm != nil {
		t.Cleanup(func() { _ = os.RemoveAll(strings.TrimSuffix(sm[1], "/secret")) })
	}
}

// 隧道只对 ssh 据点有意义；别的形态要当场说清。
func TestTunnelOpenRefusals(t *testing.T) {
	withLootExec(t, true)
	tools, _, _ := newToolWorker(t)
	tools.SetExecService(&fakeExecService{submitID: 22})
	ws := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_foothold",
		`{"summary":"webshell","evidence":"curl","confidence":"high","protocol":"webshell","url":"http://web01/x.php"}`))
	wsJSON, _ := json.Marshal(ws)
	out := callTool(t, tools.WorkerTools(), "tunnel_open", `{"foothold_id":`+string(wsJSON)+`,"host":"web01"}`)
	if !strings.Contains(out, "隧道需要 protocol=ssh") {
		t.Fatalf("webshell 据点应被拒：%s", out)
	}
	withLootExec(t, false)
	out = callTool(t, tools.WorkerTools(), "tunnel_open", `{"foothold_id":`+string(wsJSON)+`,"host":"web01"}`)
	if !strings.Contains(out, "默认关闭") {
		t.Fatalf("开关关闭时应明确拒绝：%s", out)
	}
}

// 概览要能告诉规划层"现在通着哪几条隧道"——跨 worker 共享的能力，看不见就没人用。
func TestGraphOverviewListsTunnels(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	fake := &fakeExecService{
		submitID: 23,
		active: []*db.ToolExecution{
			{ID: 7, Description: tunnelDescPrefix + "socks5h://127.0.0.1:1080 ← 据点 #3", Status: db.ExecRunning},
			{ID: 8, Description: "run_execution：普通扫描", Status: db.ExecRunning},
		},
	}
	tools.SetExecService(fake)
	out := tools.graphOverviewData()
	tunnels, ok := out["tunnels"].([]map[string]any)
	if !ok || len(tunnels) != 1 {
		t.Fatalf("概览应只登记隧道类执行：%#v", out["tunnels"])
	}
	if tunnels[0]["execution_id"] != int64(7) {
		t.Fatalf("隧道执行 id 不对：%#v", tunnels[0])
	}
}
