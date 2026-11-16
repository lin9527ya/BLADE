package db

import (
	"math"
	"testing"
	"time"
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

func TestCostSummaryOffpeakWindow(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	model := "cost-test-offpeak-model"
	peakWorker := "cost-test-peak"
	offpeakWorker := "cost-test-offpeak"
	cleanup := func() {
		_, _ = d.Exec(`DELETE FROM llm_usage WHERE model=$1`, model)
		_, _ = d.Exec(`DELETE FROM llm_prices WHERE model=$1`, model)
	}
	cleanup()
	defer cleanup()

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	// Beijing 10:00 (peak) and 03:00 (offpeak), with an offpeak window 00:30–08:30.
	peakAt := time.Date(2026, 11, 16, 10, 0, 0, 0, loc).UTC()
	offpeakAt := time.Date(2026, 11, 16, 3, 0, 0, 0, loc).UTC()
	for _, item := range []struct {
		worker string
		at     time.Time
	}{
		{peakWorker, peakAt},
		{offpeakWorker, offpeakAt},
	} {
		if err := d.InsertLLMUsage(&LLMUsage{TaskID: "cost-test-offpeak-task", Worker: item.worker, Model: model,
			ProfileName: "cost-test-offpeak-profile", InputTokens: 1_000_000, OutputTokens: 500_000, CacheRead: 200_000}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`UPDATE llm_usage SET ts=$1 WHERE model=$2 AND worker=$3`, item.at, model, item.worker); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.ReplaceLLMPrices([]LLMPrice{{
		Model: model, InputPerMillion: 10, OutputPerMillion: 20, CacheReadPerMillion: 1,
		OffpeakStart: "00:30", OffpeakEnd: "08:30", OffpeakTimezone: "Asia/Shanghai",
		OffpeakInputPerMillion: 4, OffpeakOutputPerMillion: 8, OffpeakCacheReadPerMillion: 0.5,
	}}); err != nil {
		t.Fatal(err)
	}
	summary, err := d.CostSummary(30)
	if err != nil {
		t.Fatal(err)
	}
	var row *CostModel
	for _, m := range summary.ByModel {
		if m.Model == model {
			row = &m
			break
		}
	}
	if row == nil {
		t.Fatalf("model %s missing from summary: %+v", model, summary.ByModel)
	}
	// Peak: (800k*10 + 200k*1 + 500k*20)/1e6 = 18.2
	// Offpeak: (800k*4 + 200k*0.5 + 500k*8)/1e6 = 7.3
	if math.Abs(row.PeakCostUSD-18.2) > 0.0001 || math.Abs(row.OffpeakCostUSD-7.3) > 0.0001 {
		t.Fatalf("peak/offpeak = %.4f/%.4f, want 18.2/7.3 (%+v)", row.PeakCostUSD, row.OffpeakCostUSD, row)
	}
	if math.Abs(row.CostUSD-25.5) > 0.0001 {
		t.Fatalf("total cost = %.4f, want 25.5", row.CostUSD)
	}
}

func TestCostSummaryOffpeakCrossMidnight(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	model := "cost-test-offpeak-midnight"
	peakWorker := "cost-test-midnight-peak"
	offpeakWorker := "cost-test-midnight-offpeak"
	cleanup := func() {
		_, _ = d.Exec(`DELETE FROM llm_usage WHERE model=$1`, model)
		_, _ = d.Exec(`DELETE FROM llm_prices WHERE model=$1`, model)
	}
	cleanup()
	defer cleanup()

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	// Beijing 12:00 (peak) and 23:00 (offpeak) with a 22:30–08:30 window.
	peakAt := time.Date(2026, 11, 16, 12, 0, 0, 0, loc).UTC()
	offpeakAt := time.Date(2026, 11, 16, 23, 0, 0, 0, loc).UTC()
	for _, item := range []struct {
		worker string
		at     time.Time
	}{
		{peakWorker, peakAt},
		{offpeakWorker, offpeakAt},
	} {
		if err := d.InsertLLMUsage(&LLMUsage{TaskID: "cost-test-offpeak-midnight-task", Worker: item.worker, Model: model,
			ProfileName: "cost-test-offpeak-midnight-profile", InputTokens: 1_000_000, OutputTokens: 500_000, CacheRead: 200_000}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`UPDATE llm_usage SET ts=$1 WHERE model=$2 AND worker=$3`, item.at, model, item.worker); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.ReplaceLLMPrices([]LLMPrice{{
		Model: model, InputPerMillion: 10, OutputPerMillion: 20, CacheReadPerMillion: 1,
		OffpeakStart: "22:30", OffpeakEnd: "08:30", OffpeakTimezone: "Asia/Shanghai",
		OffpeakInputPerMillion: 4, OffpeakOutputPerMillion: 8, OffpeakCacheReadPerMillion: 0.5,
	}}); err != nil {
		t.Fatal(err)
	}
	summary, err := d.CostSummary(30)
	if err != nil {
		t.Fatal(err)
	}
	var row *CostModel
	for _, m := range summary.ByModel {
		if m.Model == model {
			row = &m
			break
		}
	}
	if row == nil {
		t.Fatalf("model %s missing from summary: %+v", model, summary.ByModel)
	}
	if math.Abs(row.PeakCostUSD-18.2) > 0.0001 || math.Abs(row.OffpeakCostUSD-7.3) > 0.0001 {
		t.Fatalf("cross-midnight peak/offpeak = %.4f/%.4f, want 18.2/7.3 (%+v)", row.PeakCostUSD, row.OffpeakCostUSD, row)
	}
}

func TestNormalizeLLMPriceOffpeakValidation(t *testing.T) {
	valid := LLMPrice{Model: "deepseek-chat", InputPerMillion: 1, OffpeakStart: "22:30", OffpeakEnd: "08:30", OffpeakTimezone: "Asia/Shanghai"}
	if _, err := normalizeLLMPrice(valid); err != nil {
		t.Fatalf("valid cross-midnight window rejected: %v", err)
	}
	noWindow := LLMPrice{Model: "plain", InputPerMillion: 1}
	if got, err := normalizeLLMPrice(noWindow); err != nil || got.OffpeakTimezone != "Asia/Shanghai" {
		t.Fatalf("default timezone = %q, err=%v", got.OffpeakTimezone, err)
	}
	for name, item := range map[string]LLMPrice{
		"start only":     {Model: "m", OffpeakStart: "22:30"},
		"end only":       {Model: "m", OffpeakEnd: "08:30"},
		"equal":          {Model: "m", OffpeakStart: "08:30", OffpeakEnd: "08:30"},
		"bad format":     {Model: "m", OffpeakStart: "8:30", OffpeakEnd: "09:30"},
		"bad timezone":   {Model: "m", OffpeakStart: "22:30", OffpeakEnd: "08:30", OffpeakTimezone: "Mars/Olympus"},
		"negative price": {Model: "m", OffpeakInputPerMillion: -1},
	} {
		if _, err := normalizeLLMPrice(item); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}
