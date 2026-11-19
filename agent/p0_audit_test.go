package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// 高价值写回必须即时唤醒 planner：据点/凭据/能力事实/线索事实/扩面请求不能被
// 普通 fact 的 45s 合并窗口吞掉。
func TestHighValueWritebacksWakeImmediately(t *testing.T) {
	tools, _, intent := newToolWorker(t)
	var kinds []string
	tools.SetNotifyFact(func(kind string, _, _ int64, _ string, _ []int64) {
		kinds = append(kinds, kind)
	})
	callTool(t, tools.WorkerTools(), "report_foothold",
		fmt.Sprintf(`{"summary":"web01 shell","evidence":"id","confidence":"high","intent_id":%d}`, intent))
	callTool(t, tools.WorkerTools(), "report_credential",
		fmt.Sprintf(`{"summary":"web01 口令","evidence":"cat /etc/shadow","confidence":"high","kind":"password","intent_id":%d}`, intent))
	callTool(t, tools.WorkerTools(), "record_fact",
		`{"summary":"拿到可用令牌","evidence":"curl -H Authorization","confidence":"observed","enables":["credential"]}`)
	callTool(t, tools.WorkerTools(), "record_fact",
		`{"summary":"旁路入口","evidence":"GET /admin -> 200","confidence":"observed","leads":[{"text":"/admin 可能需要复核"}]}`)
	callTool(t, tools.WorkerTools(), "request_scope_expansion",
		`{"kind":"subdomain","value":"vpn.example.com","reason":"证书 SAN","evidence":"openssl s_client"}`)
	for _, want := range []string{"foothold", "credential", "capability_fact", "lead_fact", "scope_request"} {
		found := false
		for _, k := range kinds {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("缺少即时触发 %q，got %v", want, kinds)
		}
	}
}

// 据点可以作为 parent 派生意图，并自动补 uses；据点也可以直接 prove_goal。
func TestIntentFromFootholdUsesAndProvesGoal(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	out := callTool(t, tools.WorkerTools(), "report_foothold",
		fmt.Sprintf(`{"summary":"web01 shell","evidence":"id","confidence":"high","intent_id":%d}`, intent))
	loot := lootNodeID(t, out)
	childOut := callTool(t, tools.PlannerTools(), "add_intent", fmt.Sprintf(
		`{"summary":"从 web01 横向","parent_ids":[%d]}`, loot))
	if !strings.HasPrefix(childOut, "intent created: ") {
		t.Fatalf("parent foothold rejected: %s", childOut)
	}
	edges, err := ts.Edges(2000)
	if err != nil {
		t.Fatal(err)
	}
	hasDerived, hasUses := false, false
	for _, e := range edges {
		if e.Rel == db.RelDerivedFrom && e.From == loot {
			hasDerived = true
		}
		if e.Rel == db.RelUses && e.To == loot {
			hasUses = true
		}
	}
	if !hasDerived || !hasUses {
		t.Fatalf("foothold lineage edges missing: derived=%v uses=%v", hasDerived, hasUses)
	}
	goal, err := ts.AddGoal(map[string]any{"text": "拿到 web01 shell"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	callTool(t, tools.PlannerTools(), "prove_goal", fmt.Sprintf(
		`{"goal_id":%d,"evidence_id":%d,"reason":"foothold evidence"}`, goal, loot))
	n, err := ts.GetNode(goal)
	if err != nil || n == nil || n.State != "met" {
		t.Fatalf("goal not met by foothold: node=%+v err=%v", n, err)
	}
}

func TestSingleAddIntentResumesTask(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	called := false
	tools.SetResumeTask(func() { called = true })
	callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"单条补派"}`)
	if !called {
		t.Fatal("单条 add_intent 成功时必须调用 resumeTask")
	}
}

func TestPolarityMixedSignals(t *testing.T) {
	if got := normalizeFactPolarity("", "管理员登录失败返回详细 MySQL 语法报错堆栈", "observed", nil, nil); got != factPolarityMaybe {
		t.Fatalf("混合正负信号应判 inconclusive，got %q", got)
	}
	if got := normalizeFactPolarity("", "端口关闭", "observed", nil, nil); got != factPolarityNeg {
		t.Fatalf("纯否定应判 negative，got %q", got)
	}
	if got := normalizeFactPolarity("", "认证失败", "observed", []string{"access"}, nil); got != factPolarityPos {
		t.Fatalf("高价值 enables 不应被关键词降级，got %q", got)
	}
}

func TestDomainTemplatesDistinctAndClean(t *testing.T) {
	seeds := PlannerDomainSeeds()
	if len(seeds) < 7 {
		t.Fatalf("want 7 domain templates, got %d", len(seeds))
	}
	seen := map[string]bool{}
	for name, body := range seeds {
		if seen[body] {
			t.Fatalf("领域模板 %s 与其它模板完全相同", name)
		}
		seen[body] = true
		for _, bad := range []string{"add_intent 的 goal_id", "no_output"} {
			if strings.Contains(body, bad) {
				t.Fatalf("领域模板 %s 仍引用已删除字段/状态 %q", name, bad)
			}
		}
	}
}

func TestHotPrefixKeepsTriggerNotOverview(t *testing.T) {
	input := "STATIC RULES\n【本次触发本轮的实际变动】\n- 新据点\n\n【本轮态势】\n" + strings.Repeat("HUGE-OVERVIEW-", 1000)
	msgs := hotPrefixMessages(input, "上一轮推理")
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	first := msgs[0].Text()
	if !strings.Contains(first, "新据点") || strings.Contains(first, "HUGE-OVERVIEW") {
		t.Fatalf("hot prefix should keep trigger and drop overview: %q", first[:minInt(300, len(first))])
	}
	if !strings.Contains(msgs[1].Text(), "上一轮推理") {
		t.Fatal("assistant reasoning lost")
	}
}

func TestWorkerPromptMentionsLateralChannels(t *testing.T) {
	for _, want := range []string{"propose_intents", "record_fact.leads", "request_scope_expansion"} {
		if !strings.Contains(workerDefaultTmpl, want) {
			t.Fatalf("worker prompt missing lateral channel %q", want)
		}
	}
}

// 有界视图的 routes 必须来自完整图，而不是 open_intents 的 30 条窗口。
func TestBoundedRoutesUseFullGraph(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	for i := 0; i < 35; i++ {
		if _, err := ts.AddIntent(map[string]any{
			"summary": fmt.Sprintf("热路线 %d", i), "route": "hot",
		}, 9, nil, "planner"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ts.AddIntent(map[string]any{
		"summary": "冷门长尾路线", "route": "cold-tail",
	}, 0, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.PlannerTools(), "graph_overview", `{}`)
	var view map[string]any
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	routes, _ := view["routes"].([]any)
	for _, raw := range routes {
		if r, ok := raw.(map[string]any); ok && r["route"] == "cold-tail" {
			return
		}
	}
	t.Fatalf("长尾路线被窗口截断后仍应出现在全局 routes：%v", view["routes"])
}

// 同一工具名不能在任一 agent 工具面里重复注册（会污染 schema 与调度目录）。
func TestBuiltinToolListsHaveNoDuplicates(t *testing.T) {
	ts := NewToolSet(nil, "")
	for name, tools := range map[string][]actool.CoreTool{
		"planner": ts.PlannerTools(), "worker": ts.WorkerTools(), "main": ts.MainAgentTools(),
	} {
		seen := map[string]bool{}
		for _, tl := range tools {
			if seen[tl.Name()] {
				t.Fatalf("%s 工具面重复注册 %q", name, tl.Name())
			}
			seen[tl.Name()] = true
		}
	}
}
