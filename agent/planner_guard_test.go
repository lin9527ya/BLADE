package agent

import (
	"testing"

	"github.com/lin9527ya/blade/guard"
)

func TestPlannerUsesTaskGuard(t *testing.T) {
	p := NewPlanner(nil, "test", t.TempDir(), nil, 200000, 1)
	if p.guard != nil {
		t.Fatal("new planner must not invent a guard")
	}
	g := guard.New()
	p.SetGuard(g)
	if p.guard != g {
		t.Fatal("SetGuard must retain the exact per-task guard")
	}
	if g.Hooks() == nil {
		t.Fatal("guard hooks must be available for planner runs")
	}
}
