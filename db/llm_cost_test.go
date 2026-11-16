package db

import (
	"math"
	"testing"
)

func TestCostSummaryAndBudget(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	model := "cost-test-model"
	unpriced := "cost-test-unpriced"
	profile := "cost-test-profile"
	cleanup := func() {
		_, _ = d.Exec(`DELETE FROM llm_usage WHERE model = ANY($1::text[])`, []string{model, unpriced})
		_, _ = d.Exec(`DELETE FROM llm_prices WHERE model = ANY($1::text[])`, []string{model, "default"})
	}
	cleanup()
	defer cleanup()

	// Preserve operator settings if the test DB happens to have them.
	oldBudget, hadBudget, _ := d.GetSetting(settingLLMMonthlyBudget)
	oldAlert, hadAlert, _ := d.GetSetting(settingLLMBudgetAlert)
	defer func() {
		if hadBudget {
			_ = d.SetSetting(settingLLMMonthlyBudget, oldBudget)
		} else {
			_, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, settingLLMMonthlyBudget)
		}
		if hadAlert {
			_ = d.SetSetting(settingLLMBudgetAlert, oldAlert)
		} else {
			_, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, settingLLMBudgetAlert)
		}
	}()

	if err := d.InsertLLMUsage(&LLMUsage{TaskID: "cost-test-task", Worker: "worker", Model: model, ProfileName: profile,
		InputTokens: 1_000_000, OutputTokens: 500_000, CacheRead: 200_000}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertLLMUsage(&LLMUsage{TaskID: "cost-test-task", Worker: "worker", Model: unpriced, ProfileName: profile,
		InputTokens: 100_000, OutputTokens: 50_000}); err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceLLMPrices([]LLMPrice{{Model: model, InputPerMillion: 10, OutputPerMillion: 20, CacheReadPerMillion: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting(settingLLMMonthlyBudget, "10"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting(settingLLMBudgetAlert, "80"); err != nil {
		t.Fatal(err)
	}

	summary, err := d.CostSummary(30)
	if err != nil {
		t.Fatal(err)
	}
	// (800k*10 + 200k*1 + 500k*20)/1e6 = 18.2
	if math.Abs(summary.Total.CostUSD-18.2) > 0.0001 {
		t.Fatalf("cost = %v, want 18.2: %+v", summary.Total.CostUSD, summary.Total)
	}
	if summary.Total.UnpricedTokens < 150_000 {
		t.Fatalf("unpriced tokens = %d, want at least 150000", summary.Total.UnpricedTokens)
	}
	if summary.BudgetStatus != "exceeded" {
		t.Fatalf("budget status = %q, want exceeded", summary.BudgetStatus)
	}
	if len(summary.ByModel) == 0 || len(summary.Daily) == 0 {
		t.Fatalf("summary empty: %+v", summary)
	}
	items, err := d.ListInboxItems(100)
	if err != nil {
		t.Fatal(err)
	}
	sawBudget := false
	for _, item := range items {
		if item.Kind == "budget" {
			sawBudget = true
		}
	}
	if !sawBudget {
		t.Fatalf("inbox missing budget alert: %+v", items)
	}
}
