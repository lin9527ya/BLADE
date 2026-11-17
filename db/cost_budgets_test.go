package db

import "testing"

func TestCostBudgetCRUDAndStatus(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	const scopeID int64 = 987654321
	t.Cleanup(func() { d.DeleteCostBudget("company", scopeID) }) //nolint:errcheck
	if err := d.DeleteCostBudget("company", scopeID); err != nil {
		t.Fatal(err)
	}
	item, err := d.UpsertCostBudget("company", scopeID, 12.5, 80)
	if err != nil || item.BudgetUSD != 12.5 || item.AlertPct != 80 {
		t.Fatalf("upsert = %+v, err=%v", item, err)
	}
	items, err := d.ListCostBudgets()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range items {
		if b.ScopeType == "company" && b.ScopeID == scopeID {
			found = true
		}
	}
	if !found {
		t.Fatal("budget not listed")
	}

	for _, tc := range []struct {
		cost, budget float64
		alert        int
		want         string
	}{
		{5, 10, 80, "ok"},
		{8, 10, 80, "warning"},
		{10, 10, 80, "exceeded"},
		{5, 0, 80, "unset"},
	} {
		scope := &CostScope{ID: 1, CostTotals: CostTotals{CostUSD: tc.cost}}
		applyCostBudget(scope, map[string]CostBudget{"company:1": {ScopeType: "company", ScopeID: 1, BudgetUSD: tc.budget, AlertPct: tc.alert}}, "company")
		if scope.BudgetStatus != tc.want {
			t.Fatalf("cost %.1f budget %.1f status=%q want %q", tc.cost, tc.budget, scope.BudgetStatus, tc.want)
		}
	}
}
