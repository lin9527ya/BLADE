package server

import "testing"

// TestBudgetStage pins the threshold ladder: 70/85/95% notices, 100% exhaustion,
// and off-by-default behavior.
func TestBudgetStage(t *testing.T) {
	cases := []struct {
		used, budget int64
		want         string
	}{
		{0, 0, ""}, {100, 0, ""}, {0, 1000, ""},
		{699, 1000, ""}, {700, 1000, "notice"}, {849, 1000, "notice"},
		{850, 1000, "urgent"}, {949, 1000, "urgent"},
		{950, 1000, "critical"}, {999, 1000, "critical"},
		{1000, 1000, "exhausted"}, {1200, 1000, "exhausted"},
	}
	for _, c := range cases {
		if got := BudgetStage(c.used, c.budget); got != c.want {
			t.Fatalf("BudgetStage(%d,%d)=%q want %q", c.used, c.budget, got, c.want)
		}
	}
	for _, stage := range []string{"notice", "urgent", "critical"} {
		if BudgetNoticeText(stage, 900, 1000) == "" {
			t.Fatalf("notice text empty for stage %s", stage)
		}
	}
}
