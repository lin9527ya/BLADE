package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 复盘轮：代码持有段必须出现，且 add_intent 最多 3 条。
func TestReviewRoundCapsIntents(t *testing.T) {
	if !strings.Contains(plannerSystem("g", "/d", "/w", "", true), "复盘轮（代码持有）") {
		t.Fatal("复盘轮的代码持有段没进系统提示")
	}
	tools, _, _ := newToolWorker(t)
	tools.reviewRound = true
	summaries := []string{
		"对 /login 的 Email 参数测账号枚举",
		"对 /admin/login 的 ReturnUrl 测开放重定向",
		"对 /register 的角色字段测批量赋值",
	}
	for i, summary := range summaries {
		out := callTool(t, tools.PlannerTools(), "add_intent",
			`{"risk_area":"auth","summary":"`+summary+`"}`)
		if !strings.Contains(out, "intent created") {
			t.Fatalf("第 %d 条应放行：%s", i+1, out)
		}
	}
	out := callTool(t, tools.PlannerTools(), "add_intent", `{"risk_area":"auth","summary":"对 /passwordrecovery 的 token 测可预测性"}`)
	if !strings.Contains(out, "复盘轮最多派 3 条") {
		t.Fatalf("第 4 条应被复盘轮上限拒绝：%s", out)
	}
}

// 单点 verify 意图给 chain_warning，不硬拦。
func TestVerifyChainWarning(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	out := callTool(t, tools.PlannerTools(), "add_intent", `{"risk_area":"auth","kind":"verify","summary":"对 /login 测账号枚举"}`)
	if !strings.Contains(out, "intent created") || !strings.Contains(out, "chain_warning") {
		t.Fatalf("单点 verify 应放行并带 chain_warning：%s", out)
	}
	out = callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"auth","kind":"verify","summary":"先确认 nopCommerce 版本，再按版本查 CVE，命中后拿后台会话"}`)
	if strings.Contains(out, "chain_warning") {
		t.Fatalf("有验证链的意图不该带 chain_warning：%s", out)
	}
}

// 收尾时本次 run 零写回要给提醒（不拒绝）。
func TestConcludeIntentWarnsOnNoWriteback(t *testing.T) {
	tools, _, intent := newToolWorker(t)
	tools.SetOwnerNode(intent)
	out := callTool(t, tools.WorkerTools(), "conclude_intent", `{"outcome":"blocked","note":"出口不可用"}`)
	if !strings.Contains(out, "还没有任何写回") {
		t.Fatalf("零写回收尾应提醒：%s", out)
	}
}

// 前置以 blocked 收场时，下游应被审计标成 dead_dependency。
func TestDeadDependencyAudit(t *testing.T) {
	_, ts, _ := newToolWorker(t)
	pre, err := ts.AddIntent(map[string]any{"summary": "前置方向"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := ts.AddIntent(map[string]any{"summary": "依赖前置的下游"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(down, db.RelDependsOn, pre); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetIntentState(pre, "blocked"); err != nil {
		t.Fatal(err)
	}
	rep, err := ts.AuditGraph()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["dead_dependency"] == 0 {
		t.Fatalf("应报 dead_dependency：%+v", rep.Counts)
	}
}

// insert_assets.auth 自动转凭据，并按 kind+username+secret 去重。
func TestInsertAssetsAuthCreatesCredential(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	as := testDB(t).Assets()
	tools.SetAssetStore(as, as.Companies())
	in := `{"assets":[{"type":"service","url":"https://auth-test.example","auth":[{"username":"admin","password":"p@ssw0rd"}]}]}`
	out := callTool(t, tools.WorkerTools(), "insert_assets", in)
	if !strings.Contains(out, `"results"`) {
		t.Fatalf("insert_assets 应成功：%s", out)
	}
	creds, _ := ts.ListByKind(db.KindCredential, 10)
	if len(creds) != 1 {
		t.Fatalf("应自动登记 1 条凭据，实际 %d", len(creds))
	}
	var p struct {
		Kind         string `json:"kind"`
		Username     string `json:"username"`
		Secret       string `json:"secret"`
		Verification string `json:"verification"`
		Source       string `json:"source"`
	}
	_ = json.Unmarshal(creds[0].Payload, &p)
	if p.Kind != "password" || p.Username != "admin" || p.Secret != "p@ssw0rd" || p.Verification != "unverified" || p.Source != "asset_registration" {
		t.Fatalf("凭据字段不对：%+v", p)
	}
	// 重复登记同一 auth 不应再造一条。
	callTool(t, tools.WorkerTools(), "insert_assets", in)
	creds, _ = ts.ListByKind(db.KindCredential, 10)
	if len(creds) != 1 {
		t.Fatalf("重复 auth 不应重复建凭据，实际 %d", len(creds))
	}
}
