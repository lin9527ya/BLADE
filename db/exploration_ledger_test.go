package db

import "testing"

// 台账投影是纯函数：状态优先级、豁免折叠、缺口排序都要能单独钉住。
func TestBuildCoverageLedger(t *testing.T) {
	cov := []CoverageMatrixAsset{
		{ID: 1, Type: "subdomain", Label: "a.test", Cells: []CoverageMatrixCell{
			{RiskArea: "sqli", Tested: 1},
			{RiskArea: "xss", Failed: 1},
		}},
		{ID: 2, Type: "subdomain", Label: "b.test"}, // 一格都没有 = 全部未触及
	}
	asset2 := int64(2)
	waivers := []CoverageWaiver{
		{Scope: "coverage", AssetID: &asset2, RiskArea: "rce", Reason: "该资产没有可达入口"},
	}
	rep := BuildCoverageLedger(cov, waivers, []string{"sqli", "xss", "rce"})

	if rep.Assets != 2 || rep.CellsTotal != 6 || rep.Waived != 1 {
		t.Fatalf("assets/cells/waived = %d/%d/%d，want 2/6/1", rep.Assets, rep.CellsTotal, rep.Waived)
	}
	if rep.Counts[LedgerTested] != 1 || rep.Counts[LedgerFailed] != 1 {
		t.Fatalf("counts = %v", rep.Counts)
	}
	// counts 按状态如实统计（waived 是独立标记）：6 格 = 1 tested + 1 failed + 4 untested，
	// 其中 1 个 untested 被豁免。
	if rep.Counts[LedgerUntested] != 4 || rep.Counts[LedgerTested] != 1 || rep.Counts[LedgerFailed] != 1 {
		t.Fatalf("counts = %v", rep.Counts)
	}
	// 豁免的格子不算缺口；failed 排在 untested 前面。
	if rep.GapsTotal != 4 {
		t.Fatalf("gaps_total = %d，want 4", rep.GapsTotal)
	}
	if rep.Gaps[0].Status != LedgerFailed {
		t.Fatalf("failed 应排在最前：%+v", rep.Gaps)
	}
	for _, g := range rep.Gaps {
		if g.AssetID == 2 && g.RiskArea == "rce" {
			t.Fatalf("已豁免的格子不该出现在缺口里：%+v", g)
		}
	}
}

// 全资产/全风险类的豁免（两个维度都为空）要能覆盖所有格子。
func TestBuildCoverageLedgerBlanketWaiver(t *testing.T) {
	cov := []CoverageMatrixAsset{{ID: 1, Type: "subdomain", Label: "a.test"}}
	rep := BuildCoverageLedger(cov, []CoverageWaiver{{Scope: "coverage", Reason: "客户要求只做被动"}}, []string{"sqli", "xss"})
	if rep.GapsTotal != 0 || rep.Waived != 2 {
		t.Fatalf("整类豁免应清空缺口：gaps=%d waived=%d", rep.GapsTotal, rep.Waived)
	}
}

func TestCoverageWaiverCRUD(t *testing.T) {
	d, st := auditFixture(t)
	asset := int64(42)
	if _, err := st.AddCoverageWaiver("coverage", &asset, "sqli", "客户明确要求不做注入类测试"); err != nil {
		t.Fatal(err)
	}
	// 空风险类 = 该资产全部风险类放行；空字符串必须落成 NULL，而不是被归一成 "other"。
	if _, err := st.AddCoverageWaiver("coverage", &asset, "", "整台资产不纳入本轮范围"); err != nil {
		t.Fatal(err)
	}
	list, err := st.CoverageWaivers()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("waivers = %d，want 2", len(list))
	}
	if list[0].RiskArea != "sqli" {
		t.Fatalf("第一条例外风险类 = %q", list[0].RiskArea)
	}
	if list[1].RiskArea != "" {
		t.Fatalf("空风险类应存为 NULL（读回空串），得到 %q", list[1].RiskArea)
	}
	if _, err := st.AddCoverageWaiver("nope", nil, "", "x"); err == nil {
		t.Fatal("未知 scope 应被拒绝")
	}
	if _, err := st.AddCoverageWaiver("coverage", &asset, "sqli", "   "); err == nil {
		t.Fatal("空理由应被拒绝")
	}
	// 删除探索时豁免随 CASCADE 清掉，不留孤儿。
	if _, err := d.Exec(`DELETE FROM explorations WHERE id=$1`, st.expID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM completion_waivers WHERE exploration_id=$1`, st.expID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("探索删除后豁免残留 %d 条", n)
	}
}
