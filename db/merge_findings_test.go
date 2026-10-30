package db

import (
	"context"
	"testing"
)

// 合并重复漏洞：重复项标 duplicate（墓碑保留）、资产锚点并入保留项、有效计数只算保留项。
func TestMergeFindings(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "merge findings")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)
	as := d.Assets()

	a, err := es.RecordFinding(context.Background(), RecordFindingInput{
		VulnClass: "sqli", Severity: "medium", Summary: "用户 id 参数存在注入", Worker: "w1", Confidence: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := es.RecordFinding(context.Background(), RecordFindingInput{
		VulnClass: "sqli", Severity: "medium", Summary: "user_id 参数可注入读取任意数据", Worker: "w2", Confidence: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	assetID, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: "merge-findings.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := es.Anchor(b.NodeID, assetID); err != nil {
		t.Fatal(err)
	}

	if total, err := es.CountActiveFindings(); err != nil || total != 2 {
		t.Fatalf("merge 前有效发现 = %d (err=%v)，want 2", total, err)
	}
	if _, err := es.MergeFindings(a.NodeID, []int64{b.NodeID}, ""); err == nil {
		t.Fatal("空理由必须被拒")
	}
	merged, err := es.MergeFindings(a.NodeID, []int64{b.NodeID}, "同入口同根因")
	if err != nil || merged != 1 {
		t.Fatalf("merge: merged=%d err=%v", merged, err)
	}
	if total, err := es.CountActiveFindings(); err != nil || total != 1 {
		t.Fatalf("merge 后有效发现 = %d (err=%v)，want 1", total, err)
	}
	closed, err := es.ClosedFindingNodeIDs([]int64{a.NodeID, b.NodeID})
	if err != nil || !closed[b.NodeID] || closed[a.NodeID] {
		t.Fatalf("closed 集合不对: %v err=%v", closed, err)
	}
	var anchored int
	if err := d.QueryRow(`SELECT COUNT(*) FROM exploration_anchors WHERE node_id=$1 AND asset_id=$2`, a.NodeID, assetID).Scan(&anchored); err != nil {
		t.Fatal(err)
	}
	if anchored != 1 {
		t.Fatal("重复项的资产锚点应并入保留项")
	}
	// 已关闭的重复项不能再当保留项。
	if _, err := es.MergeFindings(b.NodeID, []int64{a.NodeID}, "反向合并"); err == nil {
		t.Fatal("已关闭的保留项应被拒绝")
	}
}
