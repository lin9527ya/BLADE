package agent

import (
	"strconv"
	"strings"
	"testing"
)

// 常驻索引要能回答「手上还有什么、哪条还没用」：压缩之后模型靠这一行避免重复去拿
// 已经拿到手的能力。未动用的排在前面——它是这段索引存在的理由。
func TestContextIndexShowsLootAndOutstanding(t *testing.T) {
	tools, ts, _ := newToolWorker(t)

	usedID := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_foothold",
		`{"summary":"web01 上的 root 反弹 shell","access":"反弹 shell","privilege":"root",`+
			`"evidence":"nc -lvnp 4444","confidence":"high"}`))
	freeID := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_foothold",
		`{"summary":"db02 的 ssh 登录","access":"ssh","privilege":"app",`+
			`"evidence":"ssh db02","confidence":"high"}`))

	if out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"summary":"拿 web01 当跳板","uses_loot_ids":[`+strconv.FormatInt(usedID, 10)+`]}`); !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("意图没建上：%s", out)
	}

	block := BuildContextIndex(ContextIndexVars{Store: ts})
	usedLine := "foothold-" + strconv.FormatInt(usedID, 10) + " [已动用]"
	freeLine := "foothold-" + strconv.FormatInt(freeID, 10) + " [未动用]"
	if !strings.Contains(block, usedLine) {
		t.Fatalf("索引缺少已动用据点的标记（want %q）：\n%s", usedLine, block)
	}
	if !strings.Contains(block, freeLine) {
		t.Fatalf("索引缺少未动用据点的标记（want %q）：\n%s", freeLine, block)
	}
	if !strings.Contains(block, "1 条未动用") {
		t.Fatalf("未动用计数不对：\n%s", block)
	}
	if strings.Index(block, freeLine) > strings.Index(block, usedLine) {
		t.Fatalf("未动用的该排在前面：\n%s", block)
	}
}

// 索引会进每一轮提示词与日志，凭据明文**不能**出现在里面；能指认身份的是指纹。
func TestContextIndexCredentialNeverLeaksSecret(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	const secret = "Sup3rS3cretPassw0rd"
	if out := callTool(t, tools.WorkerTools(), "report_credential",
		`{"summary":"db02 上的 app 账号","kind":"password","username":"app","secret":"`+secret+`",`+
			`"evidence":"cat /etc/app.cnf → password=x","confidence":"high",`+
			`"targets":[{"host":"db02","port":22,"service":"ssh","verified":true,"evidence":"ssh app@db02 → ok"}]}`); !strings.HasPrefix(out, "credential recorded: ") {
		t.Fatalf("凭据没记上：%s", out)
	}

	block := BuildContextIndex(ContextIndexVars{Store: ts})
	if strings.Contains(block, secret) {
		t.Fatalf("凭据明文漏进了常驻索引：\n%s", block)
	}
	if !strings.Contains(block, "credential-") || !strings.Contains(block, "未动用") {
		t.Fatalf("索引缺少凭据行：\n%s", block)
	}
}

// 打法命中段：≤2 行、单行截断，并写明"先确认指纹"的定性——它是先验不是结论。
func TestRenderPlaybookSection(t *testing.T) {
	if renderPlaybookSection(nil) != "" {
		t.Fatal("没有命中时不该注入空段")
	}
	got := renderPlaybookSection([]string{"[打法 #3] 某 CMS 的任意文件读取"})
	for _, want := range []string{"打法", "lookup_playbooks", "[打法 #3]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q: %s", want, got)
		}
	}
	long := renderPlaybookSection([]string{strings.Repeat("很", 400)})
	if !strings.Contains(long, "…") {
		t.Fatalf("长行应截断: %s", long)
	}
}
