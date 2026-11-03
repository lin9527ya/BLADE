package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

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
	// 再登记一个兄弟，让「同根发现」这道门先过去，才测得到「未试资产」那道门。
	if _, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: "vpn.exhaust-gate.example", TaskID: taskID}); err != nil {
		t.Fatal(err)
	}

	out := callTool(t, tools.PlannerTools(), "mark_strategy_exhausted",
		`{"reason":"正面入口全部封死","evidence_ids":[`+strconv.FormatInt(intent, 10)+`]}`)
	if !strings.Contains(out, "同主体资产") {
		t.Fatalf("还有未试过的同根资产时应拒绝穷尽并给出资产：%s", out)
	}

	// 把同根资产都派过意图后，撞上第二道门（深度）：只终结 1 类风险仍算浅。
	ids, err := as.IDsByHosts([]string{"mail.exhaust-gate.example", "vpn.exhaust-gate.example"})
	if err != nil {
		t.Fatal(err)
	}
	var anchors []int64
	for _, id := range ids {
		anchors = append(anchors, id)
	}
	if len(anchors) == 0 {
		t.Fatal("找不到 exhaust-gate 的资产 id")
	}
	for _, id := range anchors {
		ni, err := ts.AddIntent(map[string]any{"summary": "试一个面", "risk_area": "auth"}, 5, []int64{id}, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ts.CompareAndSetIntentState(ni, "open", "done"); err != nil {
			t.Fatal(err)
		}
	}
	out = callTool(t, tools.PlannerTools(), "mark_strategy_exhausted",
		`{"reason":"范围内同主体资产都已试过且无可行方向","evidence_ids":[`+strconv.FormatInt(intent, 10)+`]}`)
	if !strings.Contains(out, "只测了很少几类风险") {
		t.Fatalf("每个资产只终结 1 类时应被深度门拦下：%s", out)
	}

	// 把每个资产补到 3 类终态后，穷尽才被接受。
	for _, id := range anchors {
		for _, area := range []string{"sqli", "xss"} {
			ni, err := ts.AddIntent(map[string]any{"summary": "补 " + area, "risk_area": area}, 5, []int64{id}, "planner")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ts.CompareAndSetIntentState(ni, "open", "done"); err != nil {
				t.Fatal(err)
			}
		}
	}
	out = callTool(t, tools.PlannerTools(), "mark_strategy_exhausted",
		`{"reason":"范围内同主体资产都已按面测过且无可行方向","evidence_ids":[`+strconv.FormatInt(intent, 10)+`]}`)
	if !strings.Contains(out, "strategy exhausted recorded") {
		t.Fatalf("三道门都过了应允许穷尽：%s", out)
	}
}

// 同根子域一条都没登记时，态势要明确说「先做发现」，穷尽也要被拒：
// 「没找到别的资产」不等于「没有别的资产」（任务 18 的现场）。
func TestScopeSurfaceFlagsSubdomainDiscoveryPending(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	taskID := tools.taskID
	as := testDB(t).Assets()
	tools.SetAssetStore(as, as.Companies())
	// 唯一根域：共享测试库里别的用例可能留下过同名根域资产，用带随机后缀的域名。
	root := "discovery-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".example"

	if _, err := as.AddAgentScope(taskID, "subdomain", "www."+root, "任务目标", "agent"); err != nil {
		t.Fatal(err)
	}
	// 资产图里只有目标主机本身 → 还没做过同根发现。
	if _, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: "www." + root, TaskID: taskID}); err != nil {
		t.Fatal(err)
	}
	ss := tools.scopeSurface()
	if ss == nil || !ss.SubdomainDiscoveryPending {
		t.Fatalf("同根只有一个主机时应标记 discovery pending：%+v", ss)
	}
	if len(ss.DiscoveryHints) == 0 || !strings.Contains(ss.DiscoveryHints[0], root) {
		t.Fatalf("应给出可用的查询表达式：%+v", ss.DiscoveryHints)
	}
	// 登记一个同根兄弟后，pending 消失。
	if _, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: "mail." + root, TaskID: taskID}); err != nil {
		t.Fatal(err)
	}
	tools.BeginRound(nil, 0, TaskClock{}, 0) // 清掉轮级缓存
	if ss := tools.scopeSurface(); ss == nil || ss.SubdomainDiscoveryPending {
		t.Fatalf("已有同根兄弟后不应再要求发现：%+v", ss)
	}
}

// 深度门：一个资产只要有一类风险终态就从 debt 里消失、pct 变 100%，但另外十几类
// 根本没碰。这种「广度达标、深度很浅」的状态不许宣告穷尽。
func TestMarkStrategyExhaustedBlockedByShallowAssets(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	taskID := tools.taskID
	as := testDB(t).Assets()
	tools.SetAssetStore(as, as.Companies())
	root := "depth-gate-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".example"

	if _, err := as.AddAgentScope(taskID, "subdomain", "www."+root, "任务目标", "agent"); err != nil {
		t.Fatal(err)
	}
	// 两个同根主机：一个测过 1 类，一个完全没碰；先让「同根发现」门过去。
	for _, host := range []string{"www." + root, "mail." + root, "vpn." + root} {
		if _, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: host, TaskID: taskID}); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := as.IDsByHosts([]string{"www." + root, "mail." + root, "vpn." + root})
	if err != nil {
		t.Fatal(err)
	}
	var anchors []int64
	for _, id := range ids {
		anchors = append(anchors, id)
	}
	// 每个资产各派一条已终结的 auth 意图 → 广度上"都测过了"。
	for _, id := range anchors {
		ni, err := ts.AddIntent(map[string]any{"summary": "测认证面", "risk_area": "auth"}, 5, []int64{id}, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ts.CompareAndSetIntentState(ni, "open", "done"); err != nil {
			t.Fatal(err)
		}
	}

	out := callTool(t, tools.PlannerTools(), "mark_strategy_exhausted",
		`{"reason":"所有资产都测过了","evidence_ids":[`+strconv.FormatInt(intent, 10)+`]}`)
	if !strings.Contains(out, "只测了很少几类风险") {
		t.Fatalf("每个资产只终结 1 类风险时应拒绝穷尽：%s", out)
	}
}

// scope_surface 必须把「测过但很浅」的资产单独列出来，而不是混在 untouched 里。
func TestScopeSurfaceListsShallowAssets(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	taskID := tools.taskID
	as := testDB(t).Assets()
	tools.SetAssetStore(as, as.Companies())
	root := "shallow-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".example"
	if _, err := as.AddAgentScope(taskID, "subdomain", "www."+root, "任务目标", "agent"); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"www." + root, "mail." + root} {
		if _, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: host, TaskID: taskID}); err != nil {
			t.Fatal(err)
		}
	}
	ids, _ := as.IDsByHosts([]string{"www." + root})
	for _, id := range ids {
		ni, err := ts.AddIntent(map[string]any{"summary": "测注入", "risk_area": "sqli"}, 5, []int64{id}, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ts.CompareAndSetIntentState(ni, "open", "done"); err != nil {
			t.Fatal(err)
		}
	}
	ss := tools.scopeSurface()
	if ss == nil || ss.ShallowTotal == 0 || len(ss.Shallow) == 0 {
		t.Fatalf("应列出深度不足的资产：%+v", ss)
	}
	if ss.DepthThreshold != db.DefaultDepthAreas {
		t.Fatalf("阈值应为 %d，实际 %d", db.DefaultDepthAreas, ss.DepthThreshold)
	}
	found := false
	for _, sh := range ss.Shallow {
		if strings.Contains(sh.Label, "www."+root) {
			found = true
			if len(sh.Done) != 1 || sh.Done[0] != "sqli" {
				t.Fatalf("已终结风险类记录不对：%+v", sh)
			}
			if len(sh.Missing) == 0 {
				t.Fatalf("应给出还没碰的风险类：%+v", sh)
			}
		}
	}
	if !found {
		t.Fatalf("测过 1 类的资产应出现在 shallow 列表里：%+v", ss.Shallow)
	}
}
