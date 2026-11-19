package agent

import (
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/agentcore"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

func TestExplorationProtocolHasNoConflictingInstructions(t *testing.T) {
	body := plannerSystem("goal", "/d", "/w", "") + plannerScopeRequestBlock
	for _, old := range []string{"仍是必填核心", "本轮预取的是有界视图", "否则忽略", "写一条 fact 说明拒绝"} {
		if strings.Contains(body, old) {
			t.Fatalf("stale instruction %q", old)
		}
	}
	for _, want := range []string{"只需明确 summary", "done 只表示执行结束", "TodoWrite"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing protocol %q", want)
		}
	}
	if strings.Contains(NewToolSet(nil, "").recordFact().Description(), "一条意图通常只产出一条事实") {
		t.Fatal("fact tool still suppresses independent branches")
	}
}

func TestOldToolSchemaKeepsNewFieldsAndUserAnnotations(t *testing.T) {
	tool := NewToolSet(nil, "").recordFact()
	old := map[string]any{"type": "object", "properties": map[string]any{
		"summary": map[string]any{"type": "integer", "description": "用户自己的描述", "default": "my default"},
		"detail":  map[string]any{"type": "string", "description": "本事实的相关细节：把这次探索的多个观察事实都写进这里"},
	}}
	decorated := DecorateStoredTool(tool, "custom", old)
	props := decorated.InputSchema()["properties"].(map[string]any)
	for _, name := range []string{"leads", "enables", "evidence_meta", "facts"} {
		if props[name] == nil {
			t.Fatalf("new field %s hidden by old schema", name)
		}
	}
	summary := props["summary"].(map[string]any)
	if summary["type"] != "string" || summary["description"] != "用户自己的描述" || summary["default"] != "my default" {
		t.Fatalf("annotations/structure = %v", summary)
	}
	if old["properties"].(map[string]any)["summary"].(map[string]any)["type"] != "integer" {
		t.Fatal("mutated stored schema")
	}
	if strings.Contains(props["detail"].(map[string]any)["description"].(string), "多个观察事实都写进这里") {
		t.Fatal("old builtin detail wording retained")
	}
}

func TestHotSurfaceChangeInvalidatesSession(t *testing.T) {
	tools := NewToolSet(nil, "").PlannerTools()
	fingerprint := plannerSurfaceFingerprint("system", tools)
	reversed := append([]actool.CoreTool(nil), tools...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	if plannerSurfaceFingerprint("system", reversed) != fingerprint {
		t.Fatal("tool order should not invalidate hot cache")
	}
	p := &Planner{hotSurface: fingerprint, hot: agentcore.NewSession(agentcore.Options{}), hotTools: &ToolSet{}, hotRounds: 2}
	p.invalidateHotSurface(fingerprint)
	if p.hot == nil {
		t.Fatal("unchanged surface lost hot session")
	}
	p.invalidateHotSurface(plannerSurfaceFingerprint("new system", tools))
	if p.hot != nil || p.hotTools != nil || p.hotRounds != 0 {
		t.Fatal("changed instructions retained old registry")
	}
}

func TestLinkedFactRetainsIndependentBranchPreview(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	f, err := ts.AddNode(db.KindFact, map[string]any{"summary": "两个独立观察", "detail": "第一入口已接手；第二入口仍需验证", "polarity": "inconclusive"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := ts.AddIntent(map[string]any{"summary": "只验证第一入口"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(f, db.RelDerivedFrom, child); err != nil {
		t.Fatal(err)
	}
	view := tools.graphOverviewData()
	rows, _ := view["branch_review_facts"].([]map[string]any)
	found := false
	for _, row := range rows {
		if row["id"] == f {
			found = true
			if !strings.Contains(row["detail_excerpt"].(string), "第二入口") {
				t.Fatal("independent observation lost")
			}
		}
	}
	if !found {
		t.Fatal("a child intent hid the whole parent fact")
	}
	if _, ok := tools.graphOverviewDataForWorker()["branch_review_facts"]; ok {
		t.Fatal("worker received planner's followup queue")
	}
}
