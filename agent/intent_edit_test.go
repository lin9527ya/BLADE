package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// planner 必须能在不删意图的情况下改依赖模式、拆一条边、重开失败意图。
func TestDependencyEditingTools(t *testing.T) {
	tools, ts, pre := newToolWorker(t)
	down, err := ts.AddIntentGraph(map[string]any{
		"summary": "下游", "depends_on": []int64{pre}, "depends_on_mode": "strict",
	}, 5, nil, "planner", []db.IntentEdge{{From: 0, To: pre, Rel: db.RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}

	out := callTool(t, tools.PlannerTools(), "set_dependency_mode", fmt.Sprintf(
		`{"intent_id":%d,"mode":"advisory","reason":"test"}`, down))
	if !strings.Contains(out, "advisory") {
		t.Fatalf("set_dependency_mode output: %s", out)
	}
	n, err := ts.GetNode(down)
	if err != nil || n == nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	if p["depends_on_mode"] != db.DependencyModeAdvisory {
		t.Fatalf("mode not persisted: %+v", p)
	}

	out = callTool(t, tools.PlannerTools(), "drop_dependency", fmt.Sprintf(
		`{"intent_id":%d,"prerequisite_id":%d,"reason":"test"}`, down, pre))
	if !strings.Contains(out, "dropped dependency") {
		t.Fatalf("drop_dependency output: %s", out)
	}
	fr, err := ts.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, n := range fr {
		if n.ID == down {
			seen = true
		}
	}
	if !seen {
		t.Fatal("拆依赖后下游应可领")
	}

	if err := ts.SetIntentState(down, "blocked"); err != nil {
		t.Fatal(err)
	}
	out = callTool(t, tools.PlannerTools(), "reopen_intent", fmt.Sprintf(
		`{"intent_id":%d,"reason":"retry"}`, down))
	if !strings.Contains(out, "reopened") {
		t.Fatalf("reopen_intent output: %s", out)
	}
	n, err = ts.GetNode(down)
	if err != nil || n == nil || n.State != "open" {
		t.Fatalf("reopen state: node=%+v err=%v", n, err)
	}
}

// 提案只作为候选事实，不自动创建意图；重复提案去重，并立即触发 planner。
func TestProposeIntentsProvenanceAndDedupe(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	var triggerKind string
	tools.SetNotifyFact(func(kind string, _, _ int64, _ string, _ []int64) { triggerKind = kind })

	body := `{"proposals":[{"summary":"检查 /upload 的参数污染","mechanism":"param-pollution","asset_ids":[1]},{"summary":"检查 /upload 的参数污染","mechanism":"param-pollution","asset_ids":[1]},{"summary":"另一条方向","mechanism":"other"}]}`
	out := callTool(t, tools.WorkerTools(), "propose_intents", body)
	if !strings.Contains(out, `"proposals":2`) || !strings.Contains(out, `"dropped":1`) {
		t.Fatalf("proposal dedupe output: %s", out)
	}
	if triggerKind != "proposal" {
		t.Fatalf("proposal trigger kind=%q, want proposal", triggerKind)
	}
	facts, err := ts.ListByKind(db.KindFact, 20)
	if err != nil {
		t.Fatal(err)
	}
	var proposalFact *db.Node
	for _, n := range facts {
		var p struct {
			Origin string `json:"origin"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if p.Origin == "proposal" {
			proposalFact = n
			break
		}
	}
	if proposalFact == nil {
		t.Fatal("proposal fact missing")
	}
	var p struct {
		Origin     string `json:"origin"`
		Confidence string `json:"confidence"`
		Detail     string `json:"detail"`
	}
	_ = json.Unmarshal(proposalFact.Payload, &p)
	if p.Origin != "proposal" || p.Confidence != "inferred" {
		t.Fatalf("proposal provenance: %+v", p)
	}
	if strings.Count(p.Detail, "检查 /upload") != 1 || !strings.Contains(p.Detail, "另一条方向") {
		t.Fatalf("detail should contain the duplicate once plus the distinct proposal: %s", p.Detail)
	}

	out = callTool(t, tools.WorkerTools(), "propose_intents", body)
	if !strings.Contains(out, "no new proposals") {
		t.Fatalf("second identical proposal should dedupe: %s", out)
	}
}
