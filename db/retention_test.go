package db

import (
	"context"
	"testing"
	"time"
)

// TestRetentionPurgeRespectsProtection pins the retention contract at the DB layer:
// rows older than the cutoff go, rows belonging to a protected (non-terminal) task
// stay, and rows with no task at all are purgeable rather than immortal.
//
// The NULL-task case is the one worth guarding: task_id is nullable, and a naive
// `NOT (task_id = ANY(...))` evaluates to NULL for those rows, so they would never
// be deleted and the table would keep growing through the exact rows nobody owns.
func TestRetentionPurgeRespectsProtection(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatalf("ensure llm_usage table: %v", err)
	}
	ctx := context.Background()

	const protected, free = "retention-test-protected", "retention-test-free"
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM llm_usage WHERE task_id LIKE 'retention-test-%'`)
		_, _ = d.Exec(`DELETE FROM server_logs WHERE tag = 'retention-test'`)
		d.Close()
	})

	old := time.Now().AddDate(0, 0, -100)
	recent := time.Now().AddDate(0, 0, -1)
	seed := func(ts time.Time, task any) {
		if _, err := d.Exec(
			`INSERT INTO llm_usage(ts, task_id, model, input_tokens) VALUES ($1,$2,'retention-test',1)`,
			ts, task); err != nil {
			t.Fatalf("seed llm_usage: %v", err)
		}
	}
	seed(old, protected) // 受保护：不该动
	seed(old, free)      // 过期且无保护：该删
	seed(old, nil)       // 无任务的调用：该删（不是"永远删不掉"）
	seed(recent, free)   // 未过期：不该动

	cutoff := time.Now().AddDate(0, 0, -30)
	n, err := d.PurgeLLMUsageBefore(ctx, cutoff, []string{protected}, 2) // batch=2 顺带覆盖分批路径
	if err != nil {
		t.Fatalf("PurgeLLMUsageBefore: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted %d rows, want 2 (old+free and old+NULL; protected and recent must stay)", n)
	}

	var leftProtected, leftRecent int
	if err := d.QueryRow(`SELECT count(*) FROM llm_usage WHERE task_id=$1`, protected).Scan(&leftProtected); err != nil {
		t.Fatal(err)
	}
	if leftProtected != 1 {
		t.Fatalf("protected rows left = %d, want 1 — 非终态任务的记录被删了", leftProtected)
	}
	if err := d.QueryRow(`SELECT count(*) FROM llm_usage WHERE task_id=$1`, free).Scan(&leftRecent); err != nil {
		t.Fatal(err)
	}
	if leftRecent != 1 {
		t.Fatalf("free rows left = %d, want 1 (the recent one) — 未过期的行被删了", leftRecent)
	}
}

// TestRetentionEmptyProtectionPurgesAll covers the default posture: with nothing
// protected (no running tasks) every expired row is fair game.
func TestRetentionEmptyProtectionPurgesAll(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	ctx := context.Background()

	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM server_logs WHERE tag = 'retention-test'`)
		d.Close()
	})
	if _, err := d.Exec(
		`INSERT INTO server_logs(created_at, level, tag, text) VALUES ($1,'info','retention-test','x'), ($2,'info','retention-test','y')`,
		time.Now().AddDate(0, 0, -100), time.Now().AddDate(0, 0, -100)); err != nil {
		t.Fatalf("seed server_logs: %v", err)
	}

	n, err := d.PurgeServerLogsBefore(ctx, time.Now().AddDate(0, 0, -30), 0)
	if err != nil {
		t.Fatalf("PurgeServerLogsBefore: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted %d, want 2", n)
	}
}

// TestStripLLMRecordBodiesKeepsMetadataAndTerminates pins the two-stage reclaim:
// the four large TEXT columns are emptied, the metadata row survives, and the loop
// terminates (an UPDATE that keeps re-matching already-stripped rows would hang).
func TestStripLLMRecordBodiesKeepsMetadataAndTerminates(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatalf("ensure llm_records table: %v", err)
	}
	ctx := context.Background()

	const task = "retention-test-strip"
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM llm_records WHERE task_id = $1`, task)
		d.Close()
	})
	if _, err := d.Exec(`
INSERT INTO llm_records(ts, task_id, model, status, request_body, response_body, raw_request, raw_response)
VALUES ($1,$2,'m','ok','REQ','RESP','RAWQ','RAWS'),
       ($3,$2,'m','ok','REQ2','RESP2','RAWQ2','RAWS2')`,
		time.Now().AddDate(0, 0, -100), task, time.Now().AddDate(0, 0, -100)); err != nil {
		t.Fatalf("seed llm_records: %v", err)
	}

	n, err := d.StripLLMRecordBodiesBefore(ctx, time.Now().AddDate(0, 0, -30), nil, 1) // batch=1 覆盖分批
	if err != nil {
		t.Fatalf("StripLLMRecordBodiesBefore: %v", err)
	}
	if n != 2 {
		t.Fatalf("stripped %d rows, want 2", n)
	}

	var rows, withBodies int
	if err := d.QueryRow(
		`SELECT count(*), count(request_body) FROM llm_records WHERE task_id = $1`, task).
		Scan(&rows, &withBodies); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("metadata rows = %d, want 2 — 载荷清空不该把行删掉", rows)
	}
	if withBodies != 0 {
		t.Fatalf("rows still carrying request_body = %d, want 0", withBodies)
	}
}
