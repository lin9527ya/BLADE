package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func breadthToolSet(t *testing.T) (*ToolSet, *db.AssetStore, int64, int64, int64) {
	t.Helper()
	d := testDB(t)
	task, err := d.CreateTask("breadth-test", "cover every concrete asset", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var expID int64
	if err := d.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task.ID).Scan(&expID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE domain LIKE '%breadth-test.example%' OR url LIKE '%breadth-test.example%'`)
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
		d.Close()
	})
	as := d.Assets()
	if _, err := as.AddAgentScope(task.ID, "root_domain", "breadth-test.example", "test", "agent"); err != nil {
		t.Fatal(err)
	}
	covered, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://covered.breadth-test.example", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	debt, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://debt.breadth-test.example", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	st := d.Exploration(expID)
	id, err := st.AddIntent(map[string]any{"summary": "auth baseline", "risk_area": "auth"}, 5, []int64{covered}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetIntentState(id, "negative"); err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(st, "planner")
	ts.SetAssetStore(as, d.Companies())
	ts.SetTaskID(task.ID)
	return ts, as, task.ID, covered, debt
}

func TestGraphOverviewBreadthAndWorkerIsolation(t *testing.T) {
	ts, _, _, _, debt := breadthToolSet(t)
	overview := ts.graphOverviewData()
	b, ok := overview["breadth"].(*db.BreadthSummary)
	if !ok || b == nil {
		t.Fatalf("breadth missing: %#v", overview["breadth"])
	}
	// Each service URL also creates a subdomain asset; both are concrete points.
	if b.Debt != 3 || len(b.DebtIDs) != 3 || !b.DebtIDs[debt] {
		t.Fatalf("breadth debt = %+v", b)
	}
	if _, old := overview["coverage_gap"]; old {
		t.Fatal("coverage_gap must be replaced by breadth")
	}
	worker := ts.graphOverviewDataForWorker()
	if _, exists := worker["breadth"]; exists {
		t.Fatal("worker overview must not carry breadth")
	}
	if _, exists := worker["coverage"]; !exists {
		t.Fatal("worker overview still keeps the host_count coverage block")
	}
}

func TestGraphOverviewBreadthDisabled(t *testing.T) {
	ts, _, _, _, _ := breadthToolSet(t)
	ts.SetCoverageEnabled(false)
	if _, exists := ts.graphOverviewData()["breadth"]; exists {
		t.Fatal("coverage disabled must not expose breadth")
	}
}

func TestAddIntentBreadthWarning(t *testing.T) {
	ts, _, _, covered, debt := breadthToolSet(t)
	depth := map[string]any{"intents": []map[string]any{{
		"summary": "深挖已覆盖服务的认证链", "asset_ids": []int64{covered}, "risk_area": "auth", "priority": 7,
	}}}
	raw, _ := json.Marshal(depth)
	res, err := ts.addIntent().Call(t.Context(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := res.Flatten()
	if !strings.Contains(out, "breadth_warning") || !strings.Contains(out, "debt=3") {
		t.Fatalf("expected breadth warning, got: %s", out)
	}

	breadth := map[string]any{"intents": []map[string]any{{
		"summary": "对未覆盖服务做认证面基线验证并给出结论", "asset_ids": []int64{debt}, "risk_area": "auth", "priority": 6,
	}}}
	raw, _ = json.Marshal(breadth)
	res, err = ts.addIntent().Call(t.Context(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out := res.Flatten(); strings.Contains(out, "breadth_warning") {
		t.Fatalf("targeting a debt asset must not warn: %s", out)
	}
}

func TestAddIntentSingleBreadthWarningKeepsPrefix(t *testing.T) {
	ts, _, _, covered, _ := breadthToolSet(t)
	raw, _ := json.Marshal(map[string]any{
		"summary": "继续深挖已覆盖服务", "asset_ids": []int64{covered}, "risk_area": "auth", "priority": 6,
	})
	res, err := ts.addIntent().Call(t.Context(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := res.Flatten()
	if !strings.HasPrefix(out, "intent created: ") || !strings.Contains(out, "\nbreadth_warning: ") {
		t.Fatalf("single output = %q", out)
	}
}

func TestPlannerPromptBreadthContract(t *testing.T) {
	p := plannerSystem("g", "/d", "/w")
	for _, needle := range []string{
		"广度保底与深度并行", "breadth.debt", "assets_total", "need_asset_discovery",
		"no_output", "asset_ids", "risk_area", "coverage_matrix",
	} {
		if !strings.Contains(p, needle) {
			t.Fatalf("planner prompt missing %q", needle)
		}
	}
	if strings.Contains(plannerDefaultTmpl, "**深度优先于覆盖度**") {
		t.Fatal("default prompt still declares depth-first over coverage")
	}
}
