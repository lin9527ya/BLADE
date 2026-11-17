package db

import (
	"fmt"
	"testing"
	"time"
)

func TestListVerificationLedgerFilters(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	taskID := fmt.Sprintf("verification-ledger-%d", time.Now().UnixNano())
	insert := func(tool, command, status string) int64 {
		var id int64
		if err := d.QueryRow(`INSERT INTO tool_executions(task_id,tool,command,status,partial_tail)
VALUES($1,$2,$3,$4,'example output') RETURNING id`, taskID, tool, command, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	a := insert("http_request", "GET https://example.test/a", "completed")
	b := insert("diff_responses", "diff two responses", "failed")
	t.Cleanup(func() { d.Exec(`DELETE FROM tool_executions WHERE task_id=$1`, taskID) })

	rows, total, err := d.ListVerificationLedger(VerificationLedgerFilter{TaskID: taskID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("total=%d rows=%d", total, len(rows))
	}
	rows, total, err = d.ListVerificationLedger(VerificationLedgerFilter{TaskID: taskID, Tool: "http_request"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != a {
		t.Fatalf("tool filter = %+v total=%d", rows, total)
	}
	rows, total, err = d.ListVerificationLedger(VerificationLedgerFilter{TaskID: taskID, Status: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != b {
		t.Fatalf("status filter = %+v total=%d", rows, total)
	}
	rows, total, err = d.ListVerificationLedger(VerificationLedgerFilter{TaskID: taskID, Query: "example.test/a"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != a {
		t.Fatalf("query filter = %+v total=%d", rows, total)
	}
}
