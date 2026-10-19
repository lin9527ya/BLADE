package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// cleanup helpers to remove test data
func cleanupCompany(d *DB, id int64) {
	d.Exec(`DELETE FROM company_scope WHERE company_id = $1`, id)
	d.Exec(`DELETE FROM companies WHERE id = $1`, id)
}

func TestCompanyUpsertAndGet(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, created, err := cs.UpsertCompany("Test Corp", "https://example.com/logo.png")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	if !created {
		t.Error("first upsert should report created=true")
	}

	// duplicate: same nkey, should not create new
	id2, created2, err := cs.UpsertCompany("Test Corp", "")
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Errorf("dedup failed: %d != %d", id2, id)
	}
	if created2 {
		t.Error("second upsert should report created=false")
	}

	c, err := cs.GetCompany(id)
	if err != nil || c == nil {
		t.Fatalf("GetCompany: %v", err)
	}
	if c.Name != "Test Corp" {
		t.Errorf("name: %q", c.Name)
	}

	// GetCompanyByName
	c2, err := cs.GetCompanyByName("test corp") // normalised
	if err != nil || c2 == nil {
		t.Fatalf("GetCompanyByName: %v", err)
	}
	if c2.ID != id {
		t.Errorf("GetCompanyByName id mismatch: %d vs %d", c2.ID, id)
	}
}

func TestCreateCompanyWithScopeRejectsNormalizedDuplicate(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	stamp := time.Now().UnixNano()
	name := fmt.Sprintf("Strict Company %d", stamp)
	id, added, _, _, validationErrors, err := cs.CreateCompanyWithScope(name, "", []ScopeInput{
		{Kind: "domain", Value: fmt.Sprintf("strict-%d.example", stamp)},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	if added != 1 || len(validationErrors) != 0 {
		t.Fatalf("initial scope: added=%d errors=%v", added, validationErrors)
	}

	_, _, _, _, _, err = cs.CreateCompanyWithScope(
		"  "+strings.ToUpper(strings.ReplaceAll(name, " ", "   "))+"  ",
		"",
		[]ScopeInput{{Kind: "domain", Value: fmt.Sprintf("replacement-%d.example", stamp)}},
		"test",
	)
	if !errors.Is(err, ErrCompanyNameConflict) {
		t.Fatalf("duplicate create error=%v want ErrCompanyNameConflict", err)
	}
	scope, err := cs.GetScope(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 1 || scope[0].Domain != fmt.Sprintf("strict-%d.example", stamp) {
		t.Fatalf("duplicate create changed existing scope: %+v", scope)
	}
}

func TestCreateCompanyWithScopeRollsBackOnScopeWriteFailure(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	name := fmt.Sprintf("Atomic Create %d", time.Now().UnixNano())
	_, _, _, _, _, err = cs.CreateCompanyWithScope(name, "", []ScopeInput{
		{Kind: "keyword", Value: "invalid\x00postgres-text"},
	}, "test")
	if err == nil {
		t.Fatal("expected scope database write to fail")
	}
	company, getErr := cs.GetCompanyByName(name)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if company != nil {
		defer cleanupCompany(d, company.ID)
		t.Fatalf("company row survived failed initial scope transaction: %+v", company)
	}
}

func TestCompanyScope(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("ScopeTestCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	lines := []string{"example.com", "192.168.1.0/24", "10.0.0.1"}
	added, skipped, invalid, errs := cs.AddScope(id, lines, "test")
	if added != 3 {
		t.Errorf("want 3 added, got %d (errs: %v)", added, errs)
	}
	if skipped != 0 || invalid != 0 {
		t.Errorf("unexpected skipped=%d invalid=%d", skipped, invalid)
	}

	// Adding again should skip (duplicate)
	added2, skipped2, invalid2, _ := cs.AddScope(id, lines, "test")
	if added2 != 0 || skipped2 != 3 {
		t.Errorf("want 0 added 3 skipped, got %d added %d skipped %d invalid", added2, skipped2, invalid2)
	}

	scope, err := cs.GetScope(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 3 {
		t.Errorf("want 3 scope rules, got %d", len(scope))
	}
}

func TestCompanyScopeInvalid(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("InvalidScopeCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	// Explicitly typed TLD-only domains and overly broad CIDRs should be
	// rejected. Untyped plain text is intentionally classified as a keyword.
	inputs := []ScopeInput{
		{Kind: "domain", Value: "com"},
		{Kind: "cidr", Value: "1.2.3.4/8"},
	}
	added, _, invalid, _ := cs.AddScopeInputs(id, inputs, "test")
	if added != 0 {
		t.Errorf("want 0 added for invalid lines, got %d", added)
	}
	if invalid != 2 {
		t.Errorf("want 2 invalid, got %d", invalid)
	}
}

func TestResolveCompany(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("ResolveCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	cs.AddScope(id, []string{"resolve-test.io", "10.20.0.0/16"}, "test")

	// domain match
	cid, err := cs.ResolveCompany("resolve-test.io", "")
	if err != nil || cid == nil || *cid != id {
		t.Errorf("domain resolve: want %d, got %v (err %v)", id, cid, err)
	}

	// no match
	cid2, err := cs.ResolveCompany("notinscope.com", "")
	if err != nil || cid2 != nil {
		t.Errorf("no-match: want nil, got %v", cid2)
	}

	// IP/CIDR match
	cid3, err := cs.ResolveCompany("", "10.20.5.1")
	if err != nil || cid3 == nil || *cid3 != id {
		t.Errorf("cidr resolve: want %d, got %v (err %v)", id, cid3, err)
	}

	// IP outside CIDR
	cid4, err := cs.ResolveCompany("", "10.30.0.1")
	if err != nil || cid4 != nil {
		t.Errorf("cidr no-match: want nil, got %v", cid4)
	}
}

func TestUpdateScope(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("UpdateScopeCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	cs.AddScope(id, []string{"old-domain.com"}, "initial")

	// UpdateScope replaces
	added, invalid, errs := cs.UpdateScope(id, []string{"new-domain.com"}, "replacement")
	if added != 1 || invalid != 0 || len(errs) != 0 {
		t.Errorf("UpdateScope: added=%d invalid=%d errs=%v", added, invalid, errs)
	}

	scope, _ := cs.GetScope(id)
	if len(scope) != 1 || scope[0].Domain != "new-domain.com" {
		t.Errorf("UpdateScope: expected new-domain.com only, got %+v", scope)
	}

	// Invalid replacement input must not turn a partial validation response into
	// a destructive replacement of the existing rules.
	added, invalid, validationErrors, err := cs.UpdateScopeInputsChecked(id, []ScopeInput{
		{Kind: "domain", Value: "co.uk"},
	}, "invalid replacement")
	var validationErr *CompanyScopeValidationError
	if added != 0 || invalid != 1 || len(validationErrors) != 1 || !errors.As(err, &validationErr) {
		t.Fatalf("invalid replacement: added=%d invalid=%d validation=%v err=%v", added, invalid, validationErrors, err)
	}
	scope, err = cs.GetScope(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 1 || scope[0].Domain != "new-domain.com" {
		t.Fatalf("invalid replacement changed existing scope: %+v", scope)
	}
}

func TestUpdateScopeRollsBackOnInsertFailure(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	name := fmt.Sprintf("Atomic Scope Update %d", time.Now().UnixNano())
	id, _, err := cs.UpsertCompany(name, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	oldDomain := fmt.Sprintf("old-%d.example", time.Now().UnixNano())
	added, _, invalid, addErrors := cs.AddScopeInputs(id, []ScopeInput{{Kind: "domain", Value: oldDomain}}, "initial")
	if added != 1 || invalid != 0 || len(addErrors) != 0 {
		t.Fatalf("seed scope: added=%d invalid=%d errors=%v", added, invalid, addErrors)
	}

	added, invalid, updateErrors := cs.UpdateScopeInputs(id, []ScopeInput{
		{Kind: "keyword", Value: "invalid\x00postgres-text"},
	}, "replacement")
	if added != 0 || invalid != 0 || len(updateErrors) == 0 {
		t.Fatalf("failed update result: added=%d invalid=%d errors=%v", added, invalid, updateErrors)
	}
	scope, err := cs.GetScope(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 1 || scope[0].Domain != oldDomain {
		t.Fatalf("failed replacement did not preserve old scope: %+v", scope)
	}
}

func TestDeleteCompany(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("DeleteMeCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	cs.AddScope(id, []string{"deletetest.com"}, "test")

	if err := cs.DeleteCompany(id); err != nil {
		t.Fatal(err)
	}
	c, err := cs.GetCompany(id)
	if err != nil || c != nil {
		t.Error("expected company to be gone")
	}
	// scope should be cascade-deleted
	scope, _ := cs.GetScope(id)
	if len(scope) != 0 {
		t.Errorf("expected scope cascade-deleted, got %d rules", len(scope))
	}
}

func TestDeleteCompanyWithAssetsDeletesBoth(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	stamp := time.Now().UnixNano()
	id, _, err := cs.UpsertCompany(fmt.Sprintf("Delete Assets Company %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	var assetID int64
	domain := fmt.Sprintf("delete-assets-%d.example", stamp)
	if err := d.QueryRow(`
INSERT INTO assets(type, domain, root_domain, company_id, company_source)
VALUES ('root_domain', $1, $1, $2, 'explicit')
RETURNING id`, domain, id).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id = $1`, assetID) //nolint:errcheck

	deleted, err := cs.DeleteCompanyWithAssets(id, true)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("assets deleted=%d want 1", deleted)
	}
	company, err := cs.GetCompany(id)
	if err != nil {
		t.Fatal(err)
	}
	if company != nil {
		t.Fatalf("company still exists: %+v", company)
	}
	var assetsRemaining int
	if err := d.QueryRow(`SELECT COUNT(*) FROM assets WHERE id = $1`, assetID).Scan(&assetsRemaining); err != nil {
		t.Fatal(err)
	}
	if assetsRemaining != 0 {
		t.Fatalf("asset %d survived company deletion", assetID)
	}
}

func TestRecomputeAttribution(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()
	as := d.Assets()

	id, _, err := cs.UpsertCompany("AttributeTestCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	defer d.Exec(`DELETE FROM assets WHERE root_domain = 'attr-test.com'`)

	// insert asset before adding scope
	assetID, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: "attr-test.com"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id = $1`, assetID)

	// asset should not be attributed yet
	var companyID *int64
	d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, assetID).Scan(&companyID)
	if companyID != nil {
		t.Error("expected no company before scope added")
	}

	// add scope and recompute
	cs.AddScope(id, []string{"attr-test.com"}, "test")
	if err := cs.RecomputeAttribution(); err != nil {
		t.Fatal(err)
	}

	d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, assetID).Scan(&companyID)
	if companyID == nil || *companyID != id {
		t.Errorf("RecomputeAttribution: expected company %d, got %v", id, companyID)
	}
}

func TestListCompanies(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	withScope, _, err := cs.UpsertCompany("ListTestCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, withScope)
	noScope, _, err := cs.UpsertCompany("ListTestCorpEmpty", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, noScope)

	if added, _, _, _ := cs.AddScope(withScope, []string{"example.com", "10.0.0.1"}, "test"); added != 2 {
		t.Fatalf("seed scope: added %d, want 2", added)
	}

	companies, err := cs.ListCompanies()
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[int64]*CompanyWithScope, len(companies))
	for _, c := range companies {
		byID[c.ID] = c
	}
	if byID[withScope] == nil {
		t.Fatal("ListCompanies: created company not found")
	}

	// scope 必须被**一次批量取回**并挂到对应公司上（此前是逐公司 GetScope 的 N+1）。
	// 与 GetScope 逐条比对，确保批量路径没有漏挂或串行。
	want, err := cs.GetScope(withScope)
	if err != nil {
		t.Fatal(err)
	}
	got := byID[withScope].Scope
	if len(got) != len(want) || len(got) != 2 {
		t.Fatalf("withScope.Scope = %d 条，want %d（与 GetScope 同口径）", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Kind != want[i].Kind {
			t.Fatalf("scope[%d] = %+v，want %+v", i, got[i], want[i])
		}
	}

	// 没有规则的公司必须是**空切片而不是 nil**：JSON 里 [] 与 null 不同，而 GetScope
	// 一直返回空切片——批量路径要保持同一口径，否则前端会突然从 [] 变成 null。
	if byID[noScope] == nil {
		t.Fatal("ListCompanies: 没有 scope 的公司没有被列出")
	}
	if byID[noScope].Scope == nil {
		t.Fatal("没有 scope 的公司，Scope 应为空切片而不是 nil（[] 与 null 在 JSON 里不同）")
	}
	if len(byID[noScope].Scope) != 0 {
		t.Fatalf("没有 scope 的公司不该有规则，实际 %d 条", len(byID[noScope].Scope))
	}
}
