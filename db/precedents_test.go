package db

import (
	"fmt"
	"testing"
	"time"
)

func TestSearchPrecedentsFindsCrossTaskFacts(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	tag := fmt.Sprintf("precedent-%d", time.Now().UnixNano())
	a, err := d.CreateTaskWithOptions("source "+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.CreateTaskWithOptions("consumer "+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(a.ID); _ = d.DeleteTask(b.ID) })
	if _, err := d.Exploration(a.ExplorationID).AddFactGraph(map[string]any{
		"summary":  "unique-precedent-" + tag,
		"detail":   "negative result from another task",
		"evidence": "curl -> 403",
	}, "worker", nil, 0, nil); err != nil {
		t.Fatal(err)
	}
	hits, err := d.Exploration(b.ExplorationID).SearchPrecedents("unique-precedent-"+tag, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].TaskID != a.ID || hits[0].Summary == "" {
		t.Fatalf("precedent search miss: %+v", hits)
	}
}
