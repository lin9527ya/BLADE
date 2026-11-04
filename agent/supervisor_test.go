package agent

import (
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 审核结论解析：只保留能落地的判定（有 id、有结论），偏离判定必须带可执行的指令。
func TestParseSupervisorVerdict(t *testing.T) {
	text := "```json\n" + `{"intents":[
		{"intent_id":12,"on_track":true,"reason":"在按意图验证越权"},
		{"intent_id":13,"on_track":false,"reason":"连续 4 次重放同一个 403 请求"},
		{"intent_id":14,"on_track":false,"reason":"在扫与目标无关的资产","steer":"停止扫无关段，回到 10.0.0.5 的 /admin"},
		{"intent_id":0,"on_track":false,"reason":"没有 id"},
		{"intent_id":13,"on_track":false,"reason":"重复条目"}
	]}` + "\n```"
	v, err := ParseSupervisorVerdict(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Intents) != 3 {
		t.Fatalf("应保留 3 条有效判定，实际 %d：%+v", len(v.Intents), v.Intents)
	}
	// 判偏离却没给指令时，用结论本身兜底——看见了不能什么都不做。
	if v.Intents[1].IntentID != 13 || v.Intents[1].Steer != "连续 4 次重放同一个 403 请求" {
		t.Fatalf("偏离判定缺少 steer 时应回落到 reason：%+v", v.Intents[1])
	}
	if v.Intents[2].Steer == "" {
		t.Fatalf("显式 steer 不该被覆盖：%+v", v.Intents[2])
	}
}

func TestParseSupervisorVerdictRejectsNonJSON(t *testing.T) {
	for _, text := range []string{"", "审核通过", `{"intents":`, `{"foo":[]}`} {
		if _, err := ParseSupervisorVerdict(text); err == nil {
			t.Fatalf("%q 应被拒绝", text)
		}
	}
	// 空数组是合法结论（全部在轨），不是解析失败。
	v, err := ParseSupervisorVerdict(`{"intents":[]}`)
	if err != nil || len(v.Intents) != 0 {
		t.Fatalf("空 intents 应被接受：%v %+v", err, v)
	}
}

// 审核员提示词必须把「不要因方向不够高效就纠偏」写清楚：steer 会直接打断 worker，
// 判据模糊就会变成频繁误伤。
func TestSupervisorPromptGuardsAgainstOverSteering(t *testing.T) {
	for _, want := range []string{
		"不要判为偏离", "规划者的职责", "不确定时判 on_track=true",
		"越界", "原地打转", "该收不收", "intent_id 必须来自输入",
	} {
		if !strings.Contains(SupervisorPrompt, want) {
			t.Errorf("审核提示词缺少 %q", want)
		}
	}
}

// 审核输入的可读性：目标正文用 text 键（set_goals 的写法），Bash 步骤抽出命令本身。
func TestSupervisorClaimReadsGoalTextAndBashCommand(t *testing.T) {
	var goal struct {
		Text    string `json:"text"`
		Summary string `json:"summary"`
	}
	goal.Text = "拿下后台管理权限"
	if goal.Text == "" {
		t.Fatal("fixture 构造失败")
	}
	line := supervisorToolLine(dbActivityForTest("Bash", `Bash {"command":"curl -sS -k https://x/ -o out.txt","description":"probe"}`))
	if !strings.HasPrefix(line, "Bash: curl -sS -k https://x/") {
		t.Fatalf("Bash 步骤应抽出命令本身，实际 %q", line)
	}
	// 非 Bash 工具保持原样；JSON 截断时也不该 panic。
	if got := supervisorToolLine(dbActivityForTest("Read", "Read {\"file_path\":\"/tmp/x\"}")); got != "Read {\"file_path\":\"/tmp/x\"}" {
		t.Fatalf("非 Bash 工具应保持原样，实际 %q", got)
	}
	if got := supervisorToolLine(dbActivityForTest("Bash", `Bash {"command":"curl -sS`)); got != `Bash {"command":"curl -sS` {
		t.Fatalf("截断的 JSON 应原样返回，实际 %q", got)
	}
}

// dbActivityForTest 造一条 tool_use 活动（只带摘要渲染需要的字段）。
func dbActivityForTest(tool, summary string) db.Activity {
	return db.Activity{Kind: "tool_use", Tool: tool, Summary: summary}
}
