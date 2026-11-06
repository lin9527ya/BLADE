package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 显式给 mechanism 的意图写入契约与方向键；同目标/同资产/同机制再次提交时，
// 不新建第二条，而是返回已有意图并说明如何用 variant 重开。
func TestIntentContractStoredAndDirectionDedup(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	first := lootNodeID(t, callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"sqli","summary":"对 /search?q= 做报错注入并拿 version()","contract":{"mechanism":"sqli-error","route":"search-sqli","produces":["db-error","version()"],"acceptance":"出现数据库报错或 version() 回显","class":0,"estimated_tokens":1200}}`))

	n, err := ts.GetNode(first)
	if err != nil || n == nil {
		t.Fatalf("读取意图失败: %v", err)
	}
	var p map[string]any
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		t.Fatal(err)
	}
	c, _ := p["contract"].(map[string]any)
	if c["mechanism"] != "sqli-error" || c["route"] != "search-sqli" {
		t.Fatalf("契约未落库: %+v", c)
	}
	if c["acceptance"] == "" {
		t.Fatalf("acceptance 未落库: %+v", c)
	}
	if dk, _ := p["direction_key"].(string); dk == "" {
		t.Fatal("显式 mechanism 应写入 direction_key")
	}
	if produces, _ := c["produces"].([]any); len(produces) != 2 {
		t.Fatalf("produces 未落库: %+v", c["produces"])
	}

	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"sqli","summary":"换一种说法再测 /search 的报错注入","contract":{"mechanism":"sqlI-ERROR","route":"search-sqli","produces":["db-error"],"acceptance":"数据库报错"}}`)
	if !strings.Contains(out, "direction_dedup") {
		t.Fatalf("同机制方向应被去重: %s", out)
	}
	if got := lootNodeID(t, out); got != first {
		t.Fatalf("去重应返回已有意图 #%d，实际 #%d", first, got)
	}
}

// variant 构成新的方向键；没有 change_evidence 只告警不拒收（经典模式软契约）。
// 结构性错误仍然报错：class 越界、recheck_of 指向未结束意图。
func TestIntentContractVariantAndValidation(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	first := lootNodeID(t, callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"sqli","summary":"对 /search?q= 做报错注入","contract":{"mechanism":"sqli-error","produces":["db-error"],"acceptance":"数据库报错"}}`))

	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"sqli","summary":"WAF 变化后改用时间盲注","contract":{"mechanism":"sqli-error","variant":"WAF 规则变化后改时间盲注","produces":["time-delay"],"acceptance":"5 秒延迟可重复"}}`)
	if !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("variant 应允许新建方向: %s", out)
	}
	if !strings.Contains(out, "contract_warning") {
		t.Fatalf("variant 缺 change_evidence 应告警: %s", out)
	}
	if got := lootNodeID(t, out); got == first {
		t.Fatalf("不同 variant 不应复用 #%d", first)
	}

	if out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"sqli","summary":"class 越界","contract":{"mechanism":"sqli-error","class":4}}`); !strings.Contains(out, "contract.class") {
		t.Fatalf("class 越界应报错: %s", out)
	}
	if out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"sqli","summary":"复核未结束意图","contract":{"mechanism":"sqli-other","recheck_of":`+itoa(first)+`}}`); !strings.Contains(out, "只能复核已结束") {
		t.Fatalf("recheck_of 未结束意图应报错: %s", out)
	}
}

func TestCompactIntentsCarriesContract(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	id := lootNodeID(t, callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"ssrf","summary":"对 /fetch?url= 测内网回连","contract":{"mechanism":"ssrf-metadata","route":"fetch-ssrf","produces":["metadata-response"],"acceptance":"返回 169.254.169.254 内容"}}`))
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		t.Fatalf("读取意图失败: %v", err)
	}
	items := compactIntents([]*db.Node{n}, nil, nil)
	if len(items) != 1 {
		t.Fatalf("期望一条意图，实际 %d", len(items))
	}
	c, _ := items[0]["contract"].(map[string]any)
	if c["mechanism"] != "ssrf-metadata" || c["route"] != "fetch-ssrf" {
		t.Fatalf("态势投影缺契约: %+v", items[0])
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
