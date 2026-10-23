package agent

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func withLootExec(t *testing.T, on bool) {
	t.Helper()
	old := LootExecEnabled
	LootExecEnabled = func() bool { return on }
	t.Cleanup(func() { LootExecEnabled = old })
}

// 默认关：这条能力把库里的凭据直接变成对目标的动作，出厂不该是打开的。
func TestLootExecOffByDefault(t *testing.T) {
	old := LootExecEnabled
	LootExecEnabled = nil
	t.Cleanup(func() { LootExecEnabled = old })

	tools, _, _ := newToolWorker(t)
	out := callTool(t, tools.PlannerTools(), "loot_exec", `{"loot_id":1,"host":"h","command":"id"}`)
	if !strings.Contains(out, "默认关闭") {
		t.Fatalf("未启用时应明确说明：%s", out)
	}
}

// 正常路径：凭据明文不进命令；命令里出现的是 0600 临时文件与目标主机。
func TestLootExecBuildsConnectionWithoutSecret(t *testing.T) {
	withLootExec(t, true)
	tools, ts, _ := newToolWorker(t)
	const secret = "Sup3rS3cret!"
	credID, err := ts.AddNode(db.KindCredential, map[string]any{
		"summary": "web01 上的 app 账号", "kind": "password", "username": "app", "secret": secret,
		"confidence": "high",
		"targets":    []any{map[string]any{"host": "web01", "port": 22, "service": "ssh", "verified": true}},
	}, 0, db.StateLootActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeExecService{submitID: 7}
	tools.SetExecService(fake)

	raw, _ := json.Marshal(credID)
	out := callTool(t, tools.PlannerTools(), "loot_exec",
		`{"loot_id":`+string(raw)+`,"host":"web01","command":"id; uname -a"}`)
	if !strings.Contains(out, "execution started:") {
		t.Fatalf("应起一条后台执行：%s", out)
	}
	if len(fake.submits) != 1 {
		t.Fatalf("应提交一次：%d", len(fake.submits))
	}
	cmd := fake.submits[0].command
	if strings.Contains(cmd, secret) {
		t.Fatalf("明文漏进了命令：%s", cmd)
	}
	if !strings.Contains(cmd, "sshpass -e") || !strings.Contains(cmd, "app@web01") {
		t.Fatalf("命令应走 sshpass + 用户@主机：%s", cmd)
	}
	// 提交时那个 0600 文件必须真实存在（客户端启动后才会读走并删除）。
	m := regexp.MustCompile(`'([^']*blade-loot-[^']*/secret)'`).FindStringSubmatch(cmd)
	if m == nil {
		t.Fatalf("命令里应有临时凭据文件路径：%s", cmd)
	}
	path := m[1]
	t.Cleanup(func() { _ = os.RemoveAll(strings.TrimSuffix(path, "/secret")) })
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("提交时凭据文件应存在：%v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("凭据文件权限应为 0600，实际 %o", perm)
	}
	// 回执与台账里都不该有明文。
	if strings.Contains(out, secret) {
		t.Fatalf("回执漏了明文：%s", out)
	}
	acts, _, err := ts.ActivityList(nil, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		if a.Kind == "loot_exec" {
			found = true
			if strings.Contains(a.Detail, secret) || strings.Contains(a.Summary, secret) {
				t.Fatalf("留痕里不该有明文：%+v", a)
			}
		}
	}
	if !found {
		t.Fatal("动用凭据应留痕（谁在什么时候用了哪条）")
	}
}

// 不能用的战果要当场说清为什么，而不是发一条注定失败的连接。
func TestLootExecRejectsUnusableLoot(t *testing.T) {
	withLootExec(t, true)
	tools, ts, _ := newToolWorker(t)
	tools.SetExecService(&fakeExecService{submitID: 9})
	call := func(id int64, host string) string {
		raw, _ := json.Marshal(id)
		return callTool(t, tools.PlannerTools(), "loot_exec",
			`{"loot_id":`+string(raw)+`,"host":"`+host+`","command":"id"}`)
	}

	footholdID, err := ts.AddNode(db.KindFoothold, map[string]any{
		"summary": "web01 的 root 反弹 shell", "access": "反弹 shell", "privilege": "root",
	}, 0, db.StateLootActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out := call(footholdID, "web01"); !strings.Contains(out, "据点(foothold)") {
		t.Fatalf("据点应说明没有结构化连法：%s", out)
	}

	credID, err := ts.AddNode(db.KindCredential, map[string]any{
		"summary": "web01 账号", "kind": "password", "username": "app", "secret": "x",
		"targets": []any{map[string]any{"host": "web01", "port": 22, "service": "ssh"}},
	}, 0, db.StateLootActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	// host 没登记过：不猜，直接说清已登记哪些。
	if out := call(credID, "db99"); !strings.Contains(out, "没有 host=db99") || !strings.Contains(out, "web01") {
		t.Fatalf("未登记的目标应被拒并列出已知目标：%s", out)
	}
	// 已失效的凭据不能用。
	if err := ts.SetNodeState(credID, "lost"); err != nil {
		t.Fatal(err)
	}
	if out := call(credID, "web01"); !strings.Contains(out, "已失效") {
		t.Fatalf("失效凭据应被拒：%s", out)
	}

	// hash 型不支持自动连接。
	hashID, err := ts.AddNode(db.KindCredential, map[string]any{
		"summary": "web01 的 NTLM", "kind": "hash", "username": "app", "secret": "aad3b435",
		"targets": []any{map[string]any{"host": "web01", "port": 445, "service": "smb"}},
	}, 0, db.StateLootActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out := call(hashID, "web01"); !strings.Contains(out, "不支持自动连接") {
		t.Fatalf("hash 型应说明不支持：%s", out)
	}
}
