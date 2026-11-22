package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// 规划者可以用 report_finding 记录它自己只读复现的漏洞，但不能写事实；
// 未复现的观察必须走 add_intent 的 observation，worker 收到的标签要明确
// 「待验证，不是事实/证据」。正文、工具面与默认绑定三者必须一致。
func TestPlannerRoleMatchesToolSurfaceAndObservationHandoff(t *testing.T) {
	have := map[string]bool{}
	for _, tool := range NewToolSet(nil, "").PlannerTools() {
		have[tool.Name()] = true
	}
	if !have["report_finding"] {
		t.Fatal("planner 应保留 report_finding：只读复现的漏洞需要能直接记录")
	}
	if have["record_fact"] {
		t.Fatal("planner 不应直接写事实：观察只能作为待验证 observation 交给 worker")
	}
	seedBound := map[string][]string{}
	for _, seed := range BuiltinToolSeeds() {
		seedBound[seed.Key] = seed.Agents
	}
	if !containsString(seedBound["report_finding"], "planner") {
		t.Fatalf("report_finding 默认绑定与 PlannerTools 不一致: %v", seedBound["report_finding"])
	}
	if containsString(seedBound["record_fact"], "planner") {
		t.Fatalf("record_fact 不应默认绑给 planner: %v", seedBound["record_fact"])
	}
	body := PlannerPromptBody()
	for _, want := range []string{
		"唯一例外", "report_finding", "observation", "不能写成事实或漏洞",
		"直接复现", "待验证",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("planner 正文缺少角色/交接说明「%s」", want)
		}
	}

	tools, ts, _ := newToolWorker(t)
	out := callTool(t, tools.PlannerTools(), "add_intent", `{
		"summary":"核对 /login 的 user 参数是否存在账号枚举",
		"observation":"登录失败回显里不同用户名的响应时间略有差异，尚未复现",
		"hypothesis":"user 参数存在可测量的账号枚举",
		"stop_condition":"三种用户名的响应时间无稳定差异"
	}`)
	if !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("observation 意图应创建成功: %s", out)
	}
	id := lootNodeID(t, out)
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		t.Fatalf("intent read: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["planner_observation"] != "登录失败回显里不同用户名的响应时间略有差异，尚未复现" {
		t.Fatalf("planner_observation 未落 payload: %v", payload)
	}
	turn := renderWorkerIntentTurn(n, "", "", 0)
	if !strings.Contains(turn, "规划者观察（待验证，不是事实/证据）") {
		t.Fatalf("worker 回合缺少观察标签:\n%s", turn)
	}
	if !strings.Contains(turn, "不要把它直接当结论写回") {
		t.Fatalf("worker 回合未区分观察与证据:\n%s", turn)
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
