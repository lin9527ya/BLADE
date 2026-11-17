package db

import (
	"fmt"
	"testing"
	"time"
)

func TestAssetLifecycleAndSources(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	as := d.Assets()
	domain := fmt.Sprintf("lifecycle-%d.example.com", time.Now().UnixNano())
	id, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = d.Exec(`DELETE FROM assets WHERE id=$1`, id) }()

	rows, err := as.GetByIDs([]int64{id})
	if err != nil || len(rows) != 1 {
		t.Fatalf("GetByIDs = %v, err=%v", rows, err)
	}
	a := rows[0]
	if a.FirstSeen == "" || a.Status != "active" || a.Provider == "" {
		t.Fatalf("lifecycle fields = first_seen:%q status:%q provider:%q", a.FirstSeen, a.Status, a.Provider)
	}
	sources, err := as.AssetSources(id)
	if err != nil || len(sources) == 0 {
		t.Fatalf("sources = %+v, err=%v", sources, err)
	}

	if err := as.RecordAssetSource(id, "csv", 0); err != nil {
		t.Fatal(err)
	}
	sources, err = as.AssetSources(id)
	if err != nil {
		t.Fatal(err)
	}
	foundCSV := false
	for _, s := range sources {
		if s.Source == "csv" {
			foundCSV = true
		}
	}
	if !foundCSV {
		t.Fatalf("csv source missing: %+v", sources)
	}

	if err := as.UpdateAssetLifecycle(id, "archived", ""); err != nil {
		t.Fatal(err)
	}
	n, err := as.CountFiltered(AssetFilter{Status: "archived", DSL: fmt.Sprintf(`domain="%s"`, domain)})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("archived filter count = %d, want 1", n)
	}
	filtered, err := as.QueryFiltered(AssetFilter{Status: "archived", Provider: "csv"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range filtered {
		if item.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("QueryFiltered did not return archived asset %d", id)
	}
}
