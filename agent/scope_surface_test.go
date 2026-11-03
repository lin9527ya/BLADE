package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 范围面必须每轮摆给 planner：范围内注册域 + 同根域下**一条意图都没派过**的资产。
// 这是「正面打不进去就换同主体资产横向推进」的依据，靠模型自己回忆是靠不住的。
func TestScopeSurfaceListsUntouchedSiblings(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	taskID := tools.taskID
	as := testDB(t).Assets()
	tools.SetAssetStore(as, as.Companies())

	for _, host := range []string{"mail.scope-surface-test.example", "vpn.scope-surface-test.example", "other.scope-surface-other.example"} {
		if _, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: host, TaskID: taskID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := as.AddAgentScope(taskID, "subdomain", "www.scope-surface-test.example", "任务目标", "agent"); err != nil {
		t.Fatal(err)
	}
	// 一个已经派过意图的兄弟 → 不应出现在 untouched 里。
	if _, err := ts.AddIntent(map[string]any{
		"summary": "测 mail 的认证面", "asset_ids": []int64{},
	}, 5, nil, "planner"); err != nil {
		t.Fatal(err)
	}

	data := tools.graphOverviewData()
	raw, _ := json.Marshal(data["scope_surface"])
	var ss struct {
		RootDomains []string `json:"root_domains"`
		Untouched   []struct {
			ID    int64  `json:"id"`
			Type  string `json:"type"`
			Label string `json:"label"`
		} `json:"untouched_siblings"`
		UntouchedTotal int `json:"untouched_siblings_total"`
	}
	if err := json.Unmarshal(raw, &ss); err != nil {
		t.Fatalf("scope_surface 解析失败: %v\n%s", err, raw)
	}
	if len(ss.RootDomains) != 1 || ss.RootDomains[0] != "scope-surface-test.example" {
		t.Fatalf("范围内注册域不对: %+v", ss.RootDomains)
	}
	if ss.UntouchedTotal < 2 {
		t.Fatalf("同根未触达资产应 ≥2，实际 %d（%s）", ss.UntouchedTotal, raw)
	}
	for _, u := range ss.Untouched {
		if strings.Contains(u.Label, "scope-surface-other.example") {
			t.Fatalf("不同注册域的资产不该出现在范围面里: %+v", u)
		}
	}
}

// 范围里还有没试过的同主体资产时，mark_strategy_exhausted 必须被拒——
// 正面受阻不等于无路可走。
func TestMarkStrategyExhaustedBlockedByUntouchedSiblings(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	taskID := tools.taskID
	as := testDB(t).Assets()
	tools.SetAssetStore(as, as.Companies())
	if _, err := as.AddAgentScope(taskID, "subdomain", "www.exhaust-gate.example", "任务目标", "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: "mail.exhaust-gate.example", TaskID: taskID}); err != nil {
		t.Fatal(err)
	}

	out := callTool(t, tools.PlannerTools(), "mark_strategy_exhausted",
		`{"reason":"正面入口全部封死","evidence_ids":[`+strconv.FormatInt(intent, 10)+`]}`)
	if !strings.Contains(out, "同主体资产") {
		t.Fatalf("还有未试过的同根资产时应拒绝穷尽并给出资产：%s", out)
	}

	// 把同根资产都派过意图后，穷尽才被接受。锚点必须显式传给 AddIntent——
	// untouched 的判定看的是 exploration_anchors，不是 payload 里的 asset_ids。
	ids, err := as.IDsByHosts([]string{"mail.exhaust-gate.example"})
	if err != nil {
		t.Fatal(err)
	}
	var anchors []int64
	for _, id := range ids {
		anchors = append(anchors, id)
	}
	if len(anchors) == 0 {
		t.Fatal("找不到 mail.exhaust-gate.example 的资产 id")
	}
	if _, err := ts.AddIntent(map[string]any{"summary": "试 mail.exhaust-gate.example"}, 5, anchors, "planner"); err != nil {
		t.Fatal(err)
	}
	out = callTool(t, tools.PlannerTools(), "mark_strategy_exhausted",
		`{"reason":"范围内同主体资产都已试过且无可行方向","evidence_ids":[`+strconv.FormatInt(intent, 10)+`]}`)
	if !strings.Contains(out, "strategy exhausted recorded") {
		t.Fatalf("范围内都试过后应允许穷尽：%s", out)
	}
}
