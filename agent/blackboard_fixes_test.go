package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestBlackboardRoutesMatchesRecentAndAssetFallback(t *testing.T) {
	// 1. compactIntents preserves top-level route when contract is omitted.
	compacted := compactIntents([]*db.Node{
		{
			ID:      11,
			State:   "open",
			Payload: json.RawMessage(`{"summary":"probe sqli","route":"sqli","asset_ids":[42]}`),
		},
	}, nil, nil)
	if len(compacted) != 1 {
		t.Fatalf("expected 1 compacted intent, got %d", len(compacted))
	}
	if got, _ := compacted[0]["route"].(string); got != "sqli" {
		t.Fatalf("compactIntents dropped top-level route: %+v", compacted[0])
	}

	// 2. blackboardRoutes aggregates top-level route + asset:<id> fallback and populates "recent".
	overview := map[string]any{
		"open_intents": []map[string]any{
			compacted[0],
		},
		"running_intents": []map[string]any{
			{"id": int64(12), "summary": "enum host", "state": "running", "asset_ids": []int64{42}},
		},
		"recent_done_intents": []map[string]any{
			{"id": int64(10), "summary": "union test", "state": "done", "route": "sqli"},
		},
	}
	routes := blackboardRoutes(overview)
	byRoute := map[string]map[string]any{}
	for _, r := range routes {
		name, _ := r["route"].(string)
		byRoute[name] = r
	}

	sqli, ok := byRoute["sqli"]
	if !ok {
		t.Fatalf("missing sqli route in %+v", routes)
	}
	if sqli["intents"] != 2 || sqli["open"] != 1 || sqli["done"] != 1 {
		t.Fatalf("unexpected sqli counts: %+v", sqli)
	}
	sqliRecent, _ := sqli["recent"].([]map[string]any)
	if len(sqliRecent) != 2 {
		t.Fatalf("expected sqli recent len=2 (matching blackboardViewData's l[\"recent\"]), got %+v", sqli["recent"])
	}

	assetRoute, ok := byRoute["asset:42"]
	if !ok {
		t.Fatalf("missing asset:42 fallback route in %+v", routes)
	}
	if assetRoute["intents"] != 1 || assetRoute["running"] != 1 {
		t.Fatalf("unexpected asset:42 counts: %+v", assetRoute)
	}
	assetRecent, _ := assetRoute["recent"].([]map[string]any)
	if len(assetRecent) != 1 {
		t.Fatalf("expected asset:42 recent len=1, got %+v", assetRoute["recent"])
	}
}

func TestWorkerTurnRespectsSessionMode(t *testing.T) {
	intent := &db.Node{ID: 77, Payload: json.RawMessage(`{"summary":"check auth"}`)}

	isolated := renderWorkerIntentTurnWithMode(intent, "", "/data/tasks/1/i77", 1, false)
	if !strings.Contains(isolated, "本意图独立会话") {
		t.Fatalf("isolated turn missing 本意图独立会话: %s", isolated)
	}
	for _, unwanted := range []string{"仅作背景参考", "本槽位会话跨意图复用"} {
		if strings.Contains(isolated, unwanted) {
			t.Fatalf("isolated turn must not include %q: %s", unwanted, isolated)
		}
	}

	shared := renderWorkerIntentTurnWithMode(intent, "", "/data/tasks/1/i77", 1, true)
	for _, want := range []string{"仅作背景参考", "本槽位会话跨意图复用"} {
		if !strings.Contains(shared, want) {
			t.Fatalf("shared turn missing %q: %s", want, shared)
		}
	}
	if strings.Contains(shared, "本意图独立会话") {
		t.Fatalf("shared turn must not claim 本意图独立会话: %s", shared)
	}
}

func TestLinkIntentSourceAcceptsLootAndRecordsUses(t *testing.T) {
	tools, ts, _ := newToolWorker(t)

	footholdID, err := ts.AddNode(db.KindFoothold, map[string]any{
		"host": "10.0.0.5", "level": "shell",
	}, 5, db.StateLootActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	adID, err := ts.AddNode(db.KindADObject, map[string]any{
		"sid": "S-1-5-21-100", "name": "DC01$", "category": "computer",
	}, 5, db.StateADActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Create an intent without parent_ids so it starts anchored to origin.
	out := callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"横向检查域控"}`)
	if !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("add_intent failed: %s", out)
	}
	var intentID int64
	if _, err := fmt.Sscanf(strings.TrimPrefix(out, "intent created: "), "%d", &intentID); err != nil {
		t.Fatalf("parse intent id from %q: %v", out, err)
	}

	// Link both foothold (writes derived_from + uses) and ad_object (writes derived_from).
	args := fmt.Sprintf(`{"intent_id":%d,"source_ids":[%d,%d]}`, intentID, footholdID, adID)
	linkOut := callTool(t, tools.PlannerTools(), "link_intent_source", args)
	if strings.Contains(linkOut, "只能锚在") || strings.Contains(linkOut, "error") {
		t.Fatalf("link_intent_source failed for foothold/ad_object: %s", linkOut)
	}

	edges, err := ts.Edges(100)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := ts.OriginFactID()
	var gotFootholdDerived, gotFootholdUses, gotADDerived, gotOrigin bool
	for _, e := range edges {
		if e.Rel == db.RelDerivedFrom && e.To == intentID {
			switch e.From {
			case footholdID:
				gotFootholdDerived = true
			case adID:
				gotADDerived = true
			case origin:
				gotOrigin = true
			}
		}
		if e.Rel == db.RelUses && e.From == intentID && e.To == footholdID {
			gotFootholdUses = true
		}
	}
	if !gotFootholdDerived || !gotFootholdUses || !gotADDerived {
		t.Fatalf("expected derived_from(foothold+ad) and uses(foothold); edges=%+v", edges)
	}
	if origin > 0 && gotOrigin {
		t.Fatalf("origin fallback derived_from edge was not removed after linking real source: %+v", edges)
	}
}
