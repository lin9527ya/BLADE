package db

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestExplorationFocusNormalizeAndMatches(t *testing.T) {
	f := NormalizeExplorationFocus(ExplorationFocus{
		Mode:          "invalid",
		FocusRoutes:   []string{" admin-rce ", "ADMIN-RCE", "", "auth"},
		FocusAssetIDs: []int64{0, -1, 42, 42, 7},
	})
	if f.Mode != ExploreModeExplore {
		t.Fatalf("invalid mode should fall back to explore, got %q", f.Mode)
	}
	if len(f.FocusRoutes) != 2 || len(f.FocusAssetIDs) != 2 {
		t.Fatalf("unexpected dedup/clean result: %+v", f)
	}
	if !f.Matches("Admin-RCE", nil) || !f.Matches("", []int64{7}) || f.Matches("other", []int64{99}) {
		t.Fatalf("focus match mismatch: %+v", f)
	}
}

func TestScoreIntentWithSignalsTierAndBoundedAging(t *testing.T) {
	now := time.Now()
	pLow, _ := json.Marshal(map[string]any{"summary": "old recon", "asset_ids": []int64{10}})
	pFocus, _ := json.Marshal(map[string]any{"summary": "deep admin exploit", "asset_ids": []int64{42}})

	oldLow := &Node{ID: 1, Priority: 1, State: "open", CreatedAt: now.Add(-60 * time.Minute), Payload: pLow}
	focusHigh := &Node{ID: 2, Priority: 8, State: "open", CreatedAt: now, Payload: pFocus}

	focus := ExplorationFocus{Mode: ExploreModeDeepen, FocusRoutes: []string{"admin-rce"}, FocusAssetIDs: []int64{42}}
	signals := IntentDispatchSignals{
		AncestryBonus:  map[int64]float64{2: 140},
		RouteMomentum:  map[string]float64{"admin-rce": 80, "recon": -40},
		RunningByRoute: map[string]int{"admin-rce": 1},
	}

	scLow, tierLow := scoreIntentWithSignals(oldLow, now, "recon", 3, 5, false, focus, signals)
	scFocus, tierFocus := scoreIntentWithSignals(focusHigh, now, "admin-rce", 5, 5, false, focus, signals)

	if scLow.Components.Aging != 80 {
		t.Fatalf("aging should cap at 80, got %.1f", scLow.Components.Aging)
	}
	if tierFocus <= tierLow || scFocus.Score <= scLow.Score {
		t.Fatalf("focused high chain must dominate aged low recon: focus=(tier=%d, score=%.1f) vs low=(tier=%d, score=%.1f)",
			tierFocus, scFocus.Score, tierLow, scLow.Score)
	}
	if scFocus.Components.Route != 0 {
		t.Fatalf("deepen mode must not apply breadth Route penalty to focused route, got %.1f", scFocus.Components.Route)
	}
	if scFocus.Components.Focus != 200 || scFocus.Components.YieldMomentum != 220 || scFocus.Components.Congestion != -25 {
		t.Fatalf("unexpected focus/yield/congestion components: %+v", scFocus.Components)
	}
	sum := scFocus.Components.Priority + scFocus.Components.Focus + scFocus.Components.YieldMomentum +
		scFocus.Components.Dependency + scFocus.Components.Restart + scFocus.Components.Congestion +
		scFocus.Components.Route + scFocus.Components.Aging + scFocus.Components.Retries
	if math.Abs(sum-scFocus.Score) > 0.0001 {
		t.Fatalf("score mismatch sum=%.4f total=%.4f", sum, scFocus.Score)
	}
}
