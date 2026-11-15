package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestGlobalSearchFindsEntities(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()

	stamp := time.Now().UnixNano()
	marker := fmt.Sprintf("palette%d", stamp)
	task, err := m.CreateTaskWithOptions("任务 "+marker, "目标 "+marker, db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	assetID, err := m.PG().Assets().UpsertRootDomain(db.UpsertRootDomainReq{Domain: marker + ".example"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.PG().Assets().DeleteByIDs([]int64{assetID}) }()

	s := &Server{m: m}
	rec := httptest.NewRecorder()
	s.globalSearch(rec, httptest.NewRequest(http.MethodGet, "/api/search?q="+marker+"&limit=5", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out GlobalSearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Tasks) == 0 || len(out.Assets) == 0 {
		t.Fatalf("search missed seeded entities: %+v", out)
	}
}

func TestAssetDetailReturnsIdentityAndNeighbors(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()

	stamp := time.Now().UnixNano()
	root := fmt.Sprintf("detail-%d.example", stamp)
	assetID, err := m.PG().Assets().UpsertRootDomain(db.UpsertRootDomainReq{Domain: root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.PG().Assets().DeleteByIDs([]int64{assetID}) }()
	childID, err := m.PG().Assets().UpsertSubdomain(db.UpsertSubdomainReq{Domain: "www." + root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.PG().Assets().DeleteByIDs([]int64{childID}) }()
	fid, err := m.PG().AddFinding(0, 0, "XSS", "详情页测试", "high", "summary", "evidence", "tester", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.PG().DeleteFinding(fid) }()

	s := &Server{m: m}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/assets/%d", assetID), nil)
	req.SetPathValue("id", i64s(assetID))
	s.getAssetDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out AssetDetailDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Asset == nil || out.Asset.ID != assetID {
		t.Fatalf("asset = %+v", out.Asset)
	}
	if len(out.Neighbors) == 0 {
		t.Fatalf("neighbors empty for root with child: %+v", out)
	}
	if out.FindingsTotal != 1 {
		t.Fatalf("findings total=%d want 1", out.FindingsTotal)
	}
}
