package db

import (
	"testing"
)

// 「同根兄弟资产自动入范围」的判定只有一条依据：同一个注册域（eTLD+1）。
// 跨根域一律不动——那需要显式的 company/icp 授权，不能因为"看起来像"就扩。
func TestSweepSiblingScopeSameRootOnly(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("sibling scope", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(task.ID)
		_, _ = d.Exec(`DELETE FROM assets WHERE domain LIKE '%sibling-scope-%' OR url LIKE '%sibling-scope-%'`)
	})
	as := d.Assets()

	// 目标主机入范围（精确子域）。
	if _, err := as.AddAgentScope(task.ID, "subdomain", "www.sibling-scope-test.example", "任务目标", "agent"); err != nil {
		t.Fatal(err)
	}
	// 资产图里登记同根兄弟 + 一个不同根域的站点。
	for _, host := range []string{"mail.sibling-scope-test.example", "vpn.sibling-scope-test.example", "shop.sibling-scope-other.example"} {
		if _, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: host, TaskID: task.ID}); err != nil {
			t.Fatal(err)
		}
	}
	added, err := as.SweepSiblingScope(task.ID, "同注册域兄弟资产")
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("只应纳入同根的 2 个子域，实际 %d", added)
	}
	rows, err := as.ListTaskScope(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		if r.Domain != "" {
			got[r.Domain] = true
		}
	}
	for _, want := range []string{"www.sibling-scope-test.example", "mail.sibling-scope-test.example", "vpn.sibling-scope-test.example"} {
		if !got[want] {
			t.Fatalf("%s 应在范围内：%+v", want, got)
		}
	}
	if got["shop.sibling-scope-other.example"] {
		t.Fatal("不同注册域的资产绝不能被自动纳入范围")
	}
	// 幂等：再扫一次不应新增。
	if again, err := as.SweepSiblingScope(task.ID, "重复扫描"); err != nil || again != 0 {
		t.Fatalf("重复扫描应无新增，got %d err %v", again, err)
	}
}

// 裸根域目标：自身入范围用 root_domain（覆盖整域），而不是 subdomain。
func TestAddSiblingScopeApexBecomesRootDomain(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("apex scope", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(task.ID)
		_, _ = d.Exec(`DELETE FROM assets WHERE domain LIKE '%sibling-scope-%' OR url LIKE '%sibling-scope-%'`)
	})
	as := d.Assets()
	if n, err := as.AddSiblingScope(task.ID, "apex-sibling-test.example", "任务目标是裸根域"); err != nil || n != 1 {
		t.Fatalf("裸根域应登记一条 root_domain：n=%d err=%v", n, err)
	}
	rows, _ := as.ListTaskScope(task.ID)
	if len(rows) != 1 || rows[0].Kind != "root_domain" || rows[0].Domain != "apex-sibling-test.example" {
		t.Fatalf("应为 root_domain example.org：%+v", rows)
	}
	roots, err := as.ScopeRootDomains(task.ID)
	if err != nil || len(roots) != 1 || roots[0] != "apex-sibling-test.example" {
		t.Fatalf("ScopeRootDomains = %v, %v", roots, err)
	}
}
