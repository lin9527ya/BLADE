package db

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Restoring task A must not clear the un-referenced marker on a snapshot that
// only task B's findings reference: cleanup would then treat it as live (or
// delete it) based on the wrong task.
func TestRestoreEvidenceUnreferenceIsScopedToTask(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	mkTask := func(name string) *Task {
		task, err := d.CreateTask(name, "fixture", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
		return task
	}
	taskA := mkTask("evidence-scope-a")
	taskB := mkTask("evidence-scope-b")
	if _, err := d.Exploration(taskA.ExplorationID).RecordFinding(t.Context(), RecordFindingInput{
		TaskID: taskA.ID, ExplorationID: taskA.ExplorationID, Summary: "a", Severity: "low",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(taskB.ExplorationID).RecordFinding(t.Context(), RecordFindingInput{
		TaskID: taskB.ID, ExplorationID: taskB.ExplorationID, Summary: "b", Severity: "low",
	}); err != nil {
		t.Fatal(err)
	}

	// Shared content-addressed snapshot referenced only by task B.
	snapshotID := fmt.Sprintf("scope-shared-%d", taskA.ID)
	if _, err := d.Exec(`
INSERT INTO traffic_evidence_snapshots(id, source_traffic_id, captured_at, url, method, status,
    req_head, resp_head, req_hash, resp_hash, req_len, resp_len, created_at, unreferenced_at)
VALUES ($1,'src',1,'http://example.test/','GET',200,'','','req','resp',1,1,now(),now())`, snapshotID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM finding_traffic_bindings WHERE snapshot_id=$1`, snapshotID)
		_, _ = d.Exec(`DELETE FROM traffic_evidence_snapshots WHERE id=$1`, snapshotID)
	})
	var findingB int64
	if err := d.QueryRow(`SELECT id FROM findings WHERE task_id=$1 LIMIT 1`, taskB.ID).Scan(&findingB); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`
INSERT INTO finding_traffic_bindings(finding_id, snapshot_id, role, note, position)
VALUES ($1,$2,'proof','b',1)`, findingB, snapshotID); err != nil {
		t.Fatal(err)
	}

	// Restore for task A (which has no bindings on this snapshot).
	snapshot := &TaskArchiveSnapshot{
		FormatVersion: 3,
		TaskID:        taskA.ID,
		Tables: map[string]json.RawMessage{
			"traffic_evidence_snapshots": json.RawMessage(`[]`),
			"finding_traffic_bindings":   json.RawMessage(`[]`),
		},
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := restoreFindingTrafficTx(tx, snapshot); err != nil {
		t.Fatal(err)
	}
	var unreferenced *string
	if err := tx.QueryRow(`SELECT unreferenced_at::text FROM traffic_evidence_snapshots WHERE id=$1`, snapshotID).Scan(&unreferenced); err != nil {
		t.Fatal(err)
	}
	if unreferenced == nil {
		t.Fatal("restoring task A cleared the marker on a snapshot only task B references")
	}
}
