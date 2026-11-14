package db

import "testing"

func TestClaimFindingReportPatrolIsLeasedAndBounded(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer d.Close()
	task, err := d.CreateTask("patrol", "empty report patrol", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = d.DeleteTaskCascade(task.ID, false, true) }()
	store := d.Exploration(task.ExplorationID)
	nodeID, err := store.AddNode(KindFinding, map[string]any{"summary": "patrol finding", "severity": "high"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	findingID, err := d.AddFinding(task.ID, nodeID, "TEST", "patrol finding", "high", "summary", "evidence", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Make it old enough and eligible (high confidence).
	if _, err := d.Exec(`UPDATE findings SET confidence='high', created_at=now()-interval '11 minutes' WHERE id=$1`, findingID); err != nil {
		t.Fatal(err)
	}

	first, err := d.ClaimFindingReportPatrol(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].FindingID != findingID || first[0].NodeID != nodeID || first[0].TaskID != task.ID {
		t.Fatalf("first claim=%+v", first)
	}
	var attempts int
	if err := d.QueryRow(`SELECT report_patrol_attempts FROM findings WHERE id=$1`, findingID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1 (claim must lease immediately)", attempts)
	}
	if again, err := d.ClaimFindingReportPatrol(1); err != nil || len(again) != 0 {
		t.Fatalf("immediate reclaim must be empty: %+v err=%v", again, err)
	}
	// Simulate the retry window elapsing; the second attempt is allowed, the
	// third is not.
	if _, err := d.Exec(`UPDATE findings SET report_patrol_at=now()-interval '31 minutes' WHERE id=$1`, findingID); err != nil {
		t.Fatal(err)
	}
	if second, err := d.ClaimFindingReportPatrol(1); err != nil || len(second) != 1 {
		t.Fatalf("second claim=%+v err=%v", second, err)
	}
	if _, err := d.Exec(`UPDATE findings SET report_patrol_at=now()-interval '31 minutes' WHERE id=$1`, findingID); err != nil {
		t.Fatal(err)
	}
	if third, err := d.ClaimFindingReportPatrol(1); err != nil || len(third) != 0 {
		t.Fatalf("attempt cap not enforced: %+v err=%v", third, err)
	}
}
