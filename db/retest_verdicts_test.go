package db

import (
	"context"
	"testing"
)

// TestLatestRetestVerdicts covers the report's retest surfacing: the LATEST
// completed verdict per finding node wins, and empty/pending retests are ignored.
func TestLatestRetestVerdicts(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	task, err := d.CreateTask("retest-verdicts-test", "verdict goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var expID int64
	if err := d.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task.ID).Scan(&expID); err != nil {
		t.Fatal(err)
	}
	// findings.node_id has an FK to exploration_nodes — create the three finding
	// nodes with explicit ids first.
	for _, nodeID := range []int64{900001, 900002, 900003} {
		if _, err := d.Exec(`INSERT INTO exploration_nodes (id, exploration_id, kind, state, payload, priority)
			VALUES ($1, $2, 'finding', 'confirmed', '{}'::jsonb, 0)`, nodeID, expID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM findings WHERE task_id=$1`, task.ID)
		_, _ = d.Exec(`DELETE FROM exploration_nodes WHERE id IN (900001,900002,900003)`)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		d.Close()
	})
	ctx := context.Background()

	// f1: two completed retests — the newer 'inconclusive' wins over 'reproduced'.
	f1, err := d.AddFinding(task.ID, 900001, "sqli", "注入", "high", "s1", "e1", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	r1, _, _, err := d.CreateFindingRetest(ctx, f1, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE finding_retests SET status='completed', verdict='reproduced' WHERE id=$1`, r1.ID); err != nil {
		t.Fatal(err)
	}
	r2, _, created, err := d.CreateFindingRetest(ctx, f1, "second")
	if err != nil || !created {
		t.Fatalf("second retest: err=%v created=%v", err, created)
	}
	if _, err := d.Exec(`UPDATE finding_retests SET status='completed', verdict='inconclusive' WHERE id=$1`, r2.ID); err != nil {
		t.Fatal(err)
	}

	// f2: single 'reproduced' — filtered out downstream but still reported here.
	f2, err := d.AddFinding(task.ID, 900002, "xss", "跨站", "medium", "s2", "e2", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	r3, _, _, err := d.CreateFindingRetest(ctx, f2, "third")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE finding_retests SET status='completed', verdict='reproduced' WHERE id=$1`, r3.ID); err != nil {
		t.Fatal(err)
	}

	// f3: no retests at all — absent from the map.
	if _, err := d.AddFinding(task.ID, 900003, "ssrf", "内网", "high", "s3", "e3", "worker", nil); err != nil {
		t.Fatal(err)
	}

	got, err := d.LatestRetestVerdicts(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %+v", got)
	}
	if got[900001] != "inconclusive" {
		t.Fatalf("f1 latest = %q, want inconclusive (newest completed wins)", got[900001])
	}
	if got[900002] != "reproduced" {
		t.Fatalf("f2 = %q, want reproduced", got[900002])
	}
	if _, ok := got[900003]; ok {
		t.Fatalf("f3 must be absent (no retests)")
	}
}
