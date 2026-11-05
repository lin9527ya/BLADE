package db

import (
	"context"
	"testing"
)

// TestOnlyConfirmedFindingsFilter 验证播报板/探索链路的展示口径：report_finding
// 产生的 pending 节点虽然 exploration_nodes.state=confirmed，也必须被过滤掉；
// 只有 findings.status 进入 confirmed 后才作为漏洞出现。候选闭合（ruled_out /
// open_proof_gap）同样不能混进来。
func TestOnlyConfirmedFindingsFilter(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	expID, err := d.CreateExploration("test", "finding display")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	factID, err := es.AddNode(KindFact, map[string]any{"summary": "fact"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := es.RecordFinding(context.Background(), RecordFindingInput{
		Worker: "worker", VulnClass: "xss", Name: "pending finding",
		Severity: "medium", Summary: "pending", Confidence: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := es.RecordCandidateClosure(context.Background(), CandidateClosureInput{
		Worker: "worker", VulnClass: "sqli", Name: "ruled out", Severity: "high",
		Summary: "ruled out", Outcome: FindingRuledOut, Confidence: "high",
		Counterevidence: "parameterized query",
	}); err != nil {
		t.Fatal(err)
	}

	nodes, total, err := es.NodesPage(NodeFilter{OnlyConfirmedFindings: true}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(nodes) != 1 || nodes[0].ID != factID {
		t.Fatalf("pending/ruled_out must be hidden: total=%d nodes=%+v", total, nodes)
	}

	if _, err := d.SetFindingStatus(rec.FindingID, FindingConfirmed); err != nil {
		t.Fatal(err)
	}
	nodes, total, err = es.NodesPage(NodeFilter{OnlyConfirmedFindings: true}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(nodes) != 2 {
		t.Fatalf("confirmed finding must be visible: total=%d nodes=%+v", total, nodes)
	}
	var seen bool
	for _, n := range nodes {
		if n.ID == rec.NodeID {
			seen = true
			if n.FindingStatus != FindingConfirmed {
				t.Fatalf("finding status projection = %q, want confirmed", n.FindingStatus)
			}
		}
	}
	if !seen {
		t.Fatalf("confirmed finding node %d missing from page", rec.NodeID)
	}
}
