package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestCompanyOverviewAggregatesCompanyFacts(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()

	stamp := time.Now().UnixNano()
	name := fmt.Sprintf("Overview Co %d", stamp)
	domain := fmt.Sprintf("overview-%d.example", stamp)
	companyID, _, _, _, _, err := m.PG().Companies().CreateCompanyWithScope(name, "", []db.ScopeInput{{Kind: "domain", Value: domain}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.PG().Companies().DeleteCompany(companyID) }()

	assetID, err := m.PG().Assets().UpsertRootDomain(db.UpsertRootDomainReq{Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.PG().Assets().DeleteByIDs([]int64{assetID}) }()
	task, err := m.CreateTaskWithOptions("overview task", "goal", db.TaskCreateOptions{CompanyIDs: []int64{companyID}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	fid, err := m.PG().AddFinding(taskID, 0, "XSS", "overview finding", "high", "summary", "evidence", "tester", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.PG().DeleteFinding(fid) }()

	s := &Server{m: m, engine: &Engine{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/companies/%d/overview", companyID), nil)
	req.SetPathValue("id", strconv.FormatInt(companyID, 10))
	s.companyOverview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out CompanyOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Company == nil || out.Company.ID != companyID || out.AssetTotal == 0 {
		t.Fatalf("company/assets = %+v total=%d", out.Company, out.AssetTotal)
	}
	if out.Findings == nil || out.Findings.Total != 1 || out.Findings.High != 1 {
		t.Fatalf("findings = %+v", out.Findings)
	}
	if len(out.Tasks) != 1 || out.Tasks[0].ID != task.ID {
		t.Fatalf("tasks = %+v", out.Tasks)
	}
}
