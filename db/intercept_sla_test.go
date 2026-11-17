package db

import (
	"fmt"
	"testing"
	"time"
)

func TestInterceptSLAStatsCountsOverdueAndTimeouts(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	before, err := d.InterceptSLAStats(3600)
	if err != nil {
		t.Fatal(err)
	}
	tag := fmt.Sprintf("sla-test-%d", time.Now().UnixNano())
	var ids []int64
	insert := func(status string, age time.Duration, decided bool) {
		var id int64
		var err error
		if decided {
			err = d.QueryRow(`INSERT INTO intercept_pending(task_id,tool_name,status,created_at,decided_at)
VALUES ($1,'Bash',$2, now() - make_interval(secs => $3), now() - make_interval(secs => $4)) RETURNING id`,
				tag, status, int64(age.Seconds()), int64((age - time.Minute).Seconds())).Scan(&id)
		} else {
			err = d.QueryRow(`INSERT INTO intercept_pending(task_id,tool_name,status,created_at)
VALUES ($1,'Bash',$2, now() - make_interval(secs => $3)) RETURNING id`,
				tag, status, int64(age.Seconds())).Scan(&id)
		}
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	insert("pending", 2*time.Hour, false) // overdue
	insert("pending", time.Minute, false) // fresh
	insert("timeout", time.Hour, true)    // decided + timeout in 24h
	insert("allowed", 2*time.Hour, true)  // decided in 24h
	t.Cleanup(func() {
		for _, id := range ids {
			d.Exec(`DELETE FROM intercept_pending WHERE id=$1`, id)
		}
	})
	after, err := d.InterceptSLAStats(3600)
	if err != nil {
		t.Fatal(err)
	}
	if after.Pending-before.Pending < 2 || after.OverduePending-before.OverduePending < 1 {
		t.Fatalf("pending/overdue delta = %d/%d", after.Pending-before.Pending, after.OverduePending-before.OverduePending)
	}
	if after.Decided24h-before.Decided24h < 2 || after.Timeouts24h-before.Timeouts24h < 1 {
		t.Fatalf("decided/timeouts delta = %d/%d", after.Decided24h-before.Decided24h, after.Timeouts24h-before.Timeouts24h)
	}
	if after.OldestPendingSeconds < 3600 {
		t.Fatalf("oldest pending = %d, want >= 3600", after.OldestPendingSeconds)
	}
}
