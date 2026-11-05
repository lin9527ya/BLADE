package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// TestRecordProbeFactWritesGraphFact 验证 planner 的开局侦察事实：
// - 没有 evidence 拒收；
// - 批量写入的 fact 带 source=opening_probe；
// - add_intent 用 parent_ids 挂到该 fact 后产生 derived_from 血缘。
func TestRecordProbeFactWritesGraphFact(t *testing.T) {
	_, ts, _ := newToolWorker(t)
	planner := NewToolSet(ts, "planner")

	out := callTool(t, planner.PlannerTools(), "record_probe_fact",
		`{"facts":[{"summary":"/admin 302 到 /login?ReturnUrl=/admin","evidence":"curl -sSI /admin -> 302 Location: /login?ReturnUrl=/admin","confidence":"observed"},`+
			`{"summary":"首页带 X-MiniProfiler-Ids","detail":"生产环境开启 MiniProfiler","evidence":"curl -sSI / -> X-MiniProfiler-Ids: [\"abc\"]","confidence":"observed"}]}`)
	var rec struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.Unmarshal([]byte(out), &rec); err != nil || len(rec.IDs) != 2 {
		t.Fatalf("record_probe_fact 返回不对: %s (err=%v)", out, err)
	}
	for _, id := range rec.IDs {
		n, err := ts.GetNode(id)
		if err != nil || n == nil || n.Kind != db.KindFact {
			t.Fatalf("fact %d 未落库: %+v err=%v", id, n, err)
		}
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		if p["source"] != "opening_probe" || p["evidence"] == "" {
			t.Fatalf("fact %d payload 缺 source/evidence: %+v", id, p)
		}
	}

	// 没有 evidence 必须拒收（防止 planner 把推测写成事实）。
	out = callTool(t, planner.PlannerTools(), "record_probe_fact", `{"summary":"疑似后台","confidence":"inferred"}`)
	if !strings.Contains(out, "evidence 必填") {
		t.Fatalf("缺 evidence 应被拒: %s", out)
	}

	out = callTool(t, planner.PlannerTools(), "add_intent",
		`{"risk_area":"auth","summary":"对 /login 测弱口令","parent_ids":[`+strconv.FormatInt(rec.IDs[0], 10)+`]}`)
	if !strings.Contains(out, "intent created") {
		t.Fatalf("add_intent 应成功: %s", out)
	}
	edges, err := ts.Edges(100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range edges {
		if e.From == rec.IDs[0] && e.Rel == db.RelDerivedFrom {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("意图没有 derived_from 到侦察事实 %d: %+v", rec.IDs[0], edges)
	}
}
