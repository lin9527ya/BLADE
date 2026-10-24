package db

import (
	"fmt"
	"testing"
)

// 邻域是**关系**：父域、子资产、同根兄弟、同 IP 的 vhost、同 C 段的其它 IP。
// list_assets 只能按字段筛，回答不了「这台机器还连着谁」。
func TestAssetNeighborsBuckets(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	const domain = "nb-test.example"
	const ipBase = "10.99.77."
	cleanup := func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE domain LIKE $1 OR root_domain LIKE $1 OR ip LIKE $2`,
			"%"+domain+"%", ipBase+"%")
	}
	cleanup()
	t.Cleanup(cleanup)

	as := d.Assets()
	root, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	a, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "a." + domain})
	if err != nil {
		t.Fatal(err)
	}
	b, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "b." + domain})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := as.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://a." + domain + ":8443/x"})
	if err != nil {
		t.Fatal(err)
	}
	ip1, err := as.UpsertIP(UpsertIPReq{IP: ipBase + "1"})
	if err != nil {
		t.Fatal(err)
	}
	ip2, err := as.UpsertIP(UpsertIPReq{IP: ipBase + "2"})
	if err != nil {
		t.Fatal(err)
	}

	nbs, err := as.AssetNeighbors(0, a, 10)
	if err != nil {
		t.Fatal(err)
	}
	byRel := map[string][]int64{}
	for _, n := range nbs {
		byRel[n.Relation] = append(byRel[n.Relation], n.ID)
		if n.ID == a {
			t.Fatal("邻域里不该包含自己")
		}
	}
	has := func(rel string, id int64) bool {
		for _, got := range byRel[rel] {
			if got == id {
				return true
			}
		}
		return false
	}
	if !has("parent", root) {
		t.Fatalf("子域应能查到父域：%+v", byRel)
	}
	if !has("sibling", b) {
		t.Fatalf("应能查到同根兄弟子域：%+v", byRel)
	}
	if !has("child", svc) {
		t.Fatalf("子域下的服务应算子资产：%+v", byRel)
	}
	_ = fmt.Sprint(ip1, ip2)

	// 同 C 段：另一个 IP。
	ipNbs, err := as.AssetNeighbors(0, ip1, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundIP2 := false
	for _, n := range ipNbs {
		if n.Relation == "same_csegment" && n.ID == ip2 {
			foundIP2 = true
		}
	}
	if !foundIP2 {
		t.Fatalf("同 C 段的另一个 IP 应算邻居：%+v", ipNbs)
	}

	if _, err := as.AssetNeighbors(0, 0, 10); err == nil {
		t.Fatal("不存在的资产应报错")
	}
}
