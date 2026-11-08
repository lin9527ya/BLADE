package db

import "testing"

func TestCompletionCoverageWaiverDoesNotExemptOtherAssets(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	st := d.Exploration(expID)
	a, err := d.Assets().UpsertSubdomain(UpsertSubdomainReq{Domain: "a.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Assets().UpsertSubdomain(UpsertSubdomainReq{Domain: "b.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range []int64{a, b} {
		id, err := st.AddIntent(map[string]any{"summary": "sqli check", "risk_area": "sqli"}, 5, []int64{asset}, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := st.CompareAndSetIntentState(id, "open", "blocked"); err != nil || !ok {
			t.Fatalf("settle: %v %v", ok, err)
		}
	}
	if _, err := st.AddCoverageWaiver("coverage", &a, "sqli", "A 没有可达输入，明确接受不做此格"); err != nil {
		t.Fatal(err)
	}
	sum, err := st.CoverageGapSummary(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Failed) != 1 || sum.Failed[0].AssetID != b {
		t.Fatalf("point waiver exempted unrelated cells: %+v", sum.Failed)
	}
	gaps, err := st.CompletionGaps(d.Assets(), CompletionOptions{TaskID: taskID, CoverageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	hasCoverage := func(gaps []CompletionGap) bool {
		for _, g := range gaps {
			if g.Kind == GapCoverage {
				return true
			}
		}
		return false
	}
	if !hasCoverage(gaps) {
		t.Fatal("point waiver bypassed whole-task coverage audit")
	}
	// 资产 A 整体放行仍然不能放行 B。
	if _, err := st.AddCoverageWaiver("coverage", &a, "", "仅 A 全部风险类被客户明确排除"); err != nil {
		t.Fatal(err)
	}
	sum, err = st.CoverageGapSummary(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.ShallowTotal != 1 || len(sum.ShallowSamples) != 1 || sum.ShallowSamples[0].AssetID != b {
		t.Fatalf("asset waiver spread to B: %+v", sum)
	}
	if _, err := st.AddCoverageWaiver("coverage", nil, "", "客户明确接受本轮剩余全部覆盖风险"); err != nil {
		t.Fatal(err)
	}
	gaps, err = st.CompletionGaps(d.Assets(), CompletionOptions{TaskID: taskID, CoverageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if hasCoverage(gaps) {
		t.Fatal("explicit blanket waiver should permit coverage completion")
	}
}
