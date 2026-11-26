package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestReprioritizeAndFocusTools(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	tools.SetWorkerConcurrency(2)

	lowOut := callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"泛侦察资产 A","route":"recon-a","priority":4}`)
	highOut := callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"深挖 /admin RCE","route":"admin-rce","priority":6}`)
	lowID := lootNodeID(t, lowOut)
	highID := lootNodeID(t, highOut)

	focusOut := callTool(t, tools.PlannerTools(), "set_exploration_focus", `{
		"mode":"deepen",
		"focus_routes":["admin-rce"],
		"focus_note":"命中后台认证绕过，集中打穿",
		"reason":"发现强突破信号"
	}`)
	if !strings.Contains(focusOut, `"mode":"deepen"`) {
		t.Fatalf("set_exploration_focus unexpected output: %s", focusOut)
	}

	repriOut := callTool(t, tools.PlannerTools(), "reprioritize_intents", fmt.Sprintf(`{
		"items":[
			{"intent_id":%d,"priority":10,"action":"keep"},
			{"intent_id":%d,"priority":2,"action":"park"}
		],
		"reason":"集中算力攻克 admin-rce，暂时挂起外围侦察"
	}`, highID, lowID))
	if !strings.Contains(repriOut, `"updated":2`) {
		t.Fatalf("reprioritize_intents unexpected output: %s", repriOut)
	}

	lowNode, err := ts.GetNode(lowID)
	if err != nil || lowNode == nil || lowNode.State != "paused" || lowNode.Priority != 2 {
		t.Fatalf("expected parked paused P2 node, got %+v (%v)", lowNode, err)
	}
	highNode, err := ts.GetNode(highID)
	if err != nil || highNode == nil || highNode.State != "open" || highNode.Priority != 10 {
		t.Fatalf("expected open P10 node, got %+v (%v)", highNode, err)
	}

	// Delta overview must surface priority, schedule, focus, budget, and scorecard.
	ver, _ := ts.BlackboardVersion(t.Context())
	delta := tools.blackboardDeltaData(0, 20, ver.Cursor())
	openRows, _ := delta["open_intents"].([]map[string]any)
	if len(openRows) == 0 || openRows[0]["id"] != highID || openRows[0]["priority"] != 10 {
		t.Fatalf("delta open_intents missing dispatch head / priority: %+v", delta["open_intents"])
	}
	if _, ok := delta["exploration_focus"]; !ok {
		t.Fatalf("delta missing exploration_focus: %+v", delta)
	}
	if budget, ok := delta["frontier_budget"].(map[string]any); !ok || budget["paused_deferred"] != 1 {
		t.Fatalf("delta missing paused_deferred count in frontier_budget: %+v", delta["frontier_budget"])
	}
	if _, ok := delta["route_scorecard"]; !ok {
		t.Fatalf("delta missing route_scorecard: %+v", delta)
	}
}

func TestAddIntentBackpressureAllowsHighPriorityBreakthroughs(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	tools.SetWorkerConcurrency(2) // softCap=12, hardCap=24

	// Fill up to hardCap directly via store (already includes 1 open intent from newToolWorker).
	for i := 0; i < 23; i++ {
		if _, err := ts.AddIntent(map[string]any{"summary": fmt.Sprintf("backlog %d", i)}, 5, nil, "planner"); err != nil {
			t.Fatal(err)
		}
	}

	// Low/medium breadth intent must be rejected cleanly at hardCap.
	out := callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"常规低优先广度","priority":5}`)
	if !strings.Contains(out, "硬上限") {
		t.Fatalf("expected hard cap refusal for P5 filler, got: %s", out)
	}

	// Priority >= 8 breakthrough must bypass hardCap.
	highOut := callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"高优漏洞利用链","priority":9}`)
	if !strings.HasPrefix(highOut, "intent created:") {
		t.Fatalf("expected P9 breakthrough to bypass hardCap, got: %s", highOut)
	}

	// Finding-derived child intent must bypass hardCap even at default priority.
	findingID, err := ts.AddNode(db.KindFinding, map[string]any{"summary": "confirmed sqli", "vulnclass": "sqli", "severity": "high"}, 0, "confirmed", "work#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	childOut := callTool(t, tools.PlannerTools(), "add_intent", fmt.Sprintf(`{"summary":"利用 sqli 拿凭据","parent_ids":[%d]}`, findingID))
	if !strings.HasPrefix(childOut, "intent created:") {
		t.Fatalf("expected finding-derived child to bypass hardCap, got: %s", childOut)
	}
}
