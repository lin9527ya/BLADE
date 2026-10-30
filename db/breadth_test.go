package db

import (
	"testing"
)

func TestBreadthSummaryAssetStates(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	as := d.Assets()
	st := d.Exploration(expID)

	api, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "api.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := as.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://svc.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	ep, err := as.UpsertEndpoint(UpsertEndpointReq{
		URL: "https://api.matrix-test.example/v1/orders", Method: "POST", TaskID: taskID,
		Params: []map[string]any{{"location": "body", "name": "id", "type": "int"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	openAsset, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "open.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	oosAsset, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "oos.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}

	addIntent := func(asset int64, area, state string) {
		t.Helper()
		id, err := st.AddIntent(map[string]any{"summary": area + " on " + state, "risk_area": area}, 5, []int64{asset}, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetIntentState(id, state); err != nil {
			t.Fatal(err)
		}
	}
	addIntent(api, "sqli", "done")
	addIntent(svc, "auth", "blocked")
	addIntent(openAsset, "authz", "running")
	addIntent(oosAsset, "xss", "out-of-scope")

	summary, err := st.BreadthSummary(taskID)
	if err != nil {
		t.Fatal(err)
	}
	// root_domain is an aggregate, not a point; service is a host:port attribute, not a
	// test surface (its coverage is carried by the endpoint/subdomain below it).
	// Concrete assets: api/svc/open/oos subdomains + endpoint = 5.
	if summary.AssetsTotal != 5 {
		t.Fatalf("assets_total = %d, want 5 (root/service excluded): %+v", summary.AssetsTotal, summary.TopGaps)
	}
	if summary.ServicesExcluded != 1 || summary.RootDomainsExcluded != 1 {
		t.Fatalf("excluded counts = root:%d service:%d, want 1/1", summary.RootDomainsExcluded, summary.ServicesExcluded)
	}
	if summary.AssetsTested != 1 || summary.AssetsFailed != 1 || summary.AssetsInFlight != 1 ||
		summary.AssetsUntouched != 1 || summary.AssetsOutOfScope != 1 {
		t.Fatalf("states = %+v", summary)
	}
	if summary.Debt != 2 || summary.AssetsTerminal != 2 || summary.NeedAssetDiscovery {
		t.Fatalf("debt/terminal = %+v", summary)
	}
	if summary.Pct == nil || *summary.Pct != 0.25 { // 1 tested / (5-1 oos)
		t.Fatalf("pct = %v, want 0.25", summary.Pct)
	}
	if len(summary.TopGaps) == 0 || summary.TopGaps[0].State != "untouched" ||
		summary.TopGaps[0].AssetID != ep {
		t.Fatalf("endpoint should be the first untouched gap: %+v", summary.TopGaps)
	}
	// service 的覆盖并入同 host 的 subdomain，债务也记在宿主资产上（不是 service id）。
	var svcHostID int64
	if err := d.QueryRow(`SELECT id FROM assets WHERE type='subdomain' AND domain='svc.matrix-test.example'`).Scan(&svcHostID); err != nil {
		t.Fatal(err)
	}
	_ = svc
	if !summary.DebtIDs[svcHostID] || !summary.DebtIDs[ep] || summary.DebtIDs[api] || summary.DebtIDs[oosAsset] {
		t.Fatalf("debt ids = %+v (svcHost=%d)", summary.DebtIDs, svcHostID)
	}
	// The endpoint is a concrete point; it must carry the URL label and params-level identity.
	if summary.TopGaps[0].Type != "endpoint" || summary.TopGaps[0].Label == "" || summary.TopGaps[0].Facts != 0 {
		t.Fatalf("endpoint gap = %+v", summary.TopGaps[0])
	}
}

func TestBreadthSummaryNeedDiscoveryAndRootOnly(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	as := d.Assets()
	if _, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: "matrix-test.example"}); err != nil {
		t.Fatal(err)
	}
	summary, err := d.Exploration(expID).BreadthSummary(taskID)
	if err != nil {
		t.Fatal(err)
	}
	// Only the root_domain asset exists → no concrete point yet, but the scope is anchored.
	if summary.AssetsTotal != 0 || summary.ScopeRows == 0 || !summary.NeedAssetDiscovery || summary.RootDomainsExcluded != 1 {
		t.Fatalf("root-only summary = %+v", summary)
	}
	if summary.Pct != nil || summary.Debt != 0 {
		t.Fatalf("root-only ledger must not be debt: %+v", summary)
	}
}
