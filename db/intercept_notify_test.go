package db

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestCreateInterceptPendingEmitsApprovalNotification(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	tool := fmt.Sprintf("ApprovalNotify%d", time.Now().UnixNano())
	id, err := d.CreateInterceptPending(0, 0, "notify-approval-task", "tester", tool, []byte(`{"command":"id"}`), "review")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM intercept_pending WHERE id=$1`, id) })
	var findingID int64
	var snapRaw []byte
	err = d.QueryRow(`SELECT finding_id, snapshot FROM notification_events
WHERE kind='approval_pending' AND snapshot->>'name'=$1 ORDER BY id DESC LIMIT 1`, "审批待处理："+tool).Scan(&findingID, &snapRaw)
	if err != nil {
		t.Fatal(err)
	}
	if findingID != 0 {
		t.Fatalf("finding_id = %d, want 0", findingID)
	}
	var snap struct {
		URL     string `json:"url"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(snapRaw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.URL != "/system/intercept/approvals" {
		t.Fatalf("url = %q", snap.URL)
	}
	if snap.Summary == "" {
		t.Fatal("summary empty")
	}
}
