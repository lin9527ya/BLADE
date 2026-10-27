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
	// root_domain is auto-created by the scope upserts but is an aggregate, not a point.
	// root_domain is excluded; endpoint upsert also creates its service parent, so
	// the five concrete inputs become six concrete assets (api/svc/open/oos + service+endpoint).
	if summary.AssetsTotal != 6 {
		t.Fatalf("assets_total = %d, want 6 (root excluded): %+v", summary.AssetsTotal, summary.TopGaps)
	}
	if summary.AssetsTested != 1 || summary.AssetsFailed != 1 || summary.AssetsInFlight != 1 ||
		summary.AssetsUntouched != 2 || summary.AssetsOutOfScope != 1 {
		t.Fatalf("states = %+v", summary)
	}
	if summary.Debt != 3 || summary.AssetsTerminal != 2 || summary.NeedAssetDiscovery {
		t.Fatalf("debt/terminal = %+v", summary)
	}
	if summary.Pct == nil || *summary.Pct != 0.2 { // 1 tested / (6-1 oos)
		t.Fatalf("pct = %v, want 0.2", summary.Pct)
	}
	if len(summary.TopGaps) == 0 || summary.TopGaps[0].State != "untouched" ||
		summary.TopGaps[0].AssetID != ep {
		t.Fatalf("endpoint should be the first untouched gap: %+v", summary.TopGaps)
	}
	if !summary.DebtIDs[svc] || !summary.DebtIDs[ep] || summary.DebtIDs[api] || summary.DebtIDs[oosAsset] {
		t.Fatalf("debt ids = %+v", summary.DebtIDs)
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
