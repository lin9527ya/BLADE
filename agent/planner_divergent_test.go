package agent

import (
	"strings"
	"testing"
)

func TestPlannerDivergentModeBlock(t *testing.T) {
	p := &Planner{}
	p.SetExplorationModeResolver(func() string { return "divergent" })
	if !p.isDivergent() {
		t.Fatal("planner should be divergent")
	}
	if !strings.Contains(plannerDivergentBlock, "覆盖度是参考信号") ||
		!strings.Contains(plannerDivergentBlock, "本次运行没有写回") {
		t.Fatalf("divergent block missing guidance:\n%s", plannerDivergentBlock)
	}
	p.SetExplorationModeResolver(func() string { return "balanced" })
	if p.isDivergent() {
		t.Fatal("balanced planner marked divergent")
	}
}
