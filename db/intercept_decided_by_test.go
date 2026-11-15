package db

import "testing"

func TestResolveInterceptRecordsDecidedBy(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	id, err := d.CreateInterceptPending(0, 0, "decided-by-test", "test", "Bash", []byte(`{"command":"id"}`), "review",
		&InterceptAudit{Correlation: "exact", InitialAction: "ask", ExecutionStatus: "not_started"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM intercept_pending WHERE id=$1`, id)

	ok, err := d.ResolveInterceptWith(id, "allowed", "allow", "人工允许", map[string]any{
		"decided_by": "token:prometheus", "decision_channel": "batch",
	})
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	detail, err := d.GetInterceptDetail(id)
	if err != nil || detail == nil || detail.Audit == nil {
		t.Fatalf("detail = %+v err=%v", detail, err)
	}
	if detail.Audit.DecidedBy != "token:prometheus" || detail.Audit.DecisionChannel != "batch" {
		t.Fatalf("audit decided_by/channel = %q/%q", detail.Audit.DecidedBy, detail.Audit.DecisionChannel)
	}
}
