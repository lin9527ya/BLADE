package db

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestCostSummaryAttributesCompanyAndCampaign(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	suffix := time.Now().UnixNano()
	companyID, _, err := d.Companies().UpsertCompany(fmt.Sprintf("cost-attr-company-%d", suffix), "")
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := d.CreateCampaign(fmt.Sprintf("cost-attr-campaign-%d", suffix), "", &companyID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions("cost attribution task", "fixture", TaskCreateOptions{CompanyIDs: []int64{companyID}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTaskCascade(task.ID, false, false) //nolint:errcheck
	if err := d.SetTaskCampaign(task.ID, &campaign.ID); err != nil {
		t.Fatal(err)
	}
	model := fmt.Sprintf("cost-attr-model-%d", suffix)
	if err := d.InsertLLMUsage(&LLMUsage{TaskID: fmt.Sprint(task.ID), Model: model, InputTokens: 1_000_000, OutputTokens: 500_000}); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM llm_usage WHERE model=$1`, model) //nolint:errcheck
	if err := d.ReplaceLLMPrices([]LLMPrice{{Model: model, InputPerMillion: 10, OutputPerMillion: 20}}); err != nil {
		t.Fatal(err)
	}
	summary, err := d.CostSummary(30)
	if err != nil {
		t.Fatal(err)
	}
	var companyCost, campaignCost float64
	for _, item := range summary.ByCompany {
		if item.ID == companyID {
			companyCost = item.CostUSD
		}
	}
	for _, item := range summary.ByCampaign {
		if item.ID == campaign.ID {
			campaignCost = item.CostUSD
		}
	}
	if math.Abs(companyCost-20) > 0.0001 || math.Abs(campaignCost-20) > 0.0001 {
		t.Fatalf("company/campaign cost = %.4f/%.4f, want 20/20", companyCost, campaignCost)
	}
}
