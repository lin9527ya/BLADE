package db

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/lin9527ya/blade/notify"
)

func TestFindingCollaborationAndComments(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	fid, err := d.AddFinding(0, 0, "collab", "协作测试", "high", "summary", "evidence", "tester", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = d.DeleteFinding(fid) }()

	due := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	assignee := "alice"
	cvss := 8.8
	cves := []string{"CVE-2026-12345", "blade-2026-0001"}
	remediation := "升级到 1.2.3"
	if n, err := d.UpdateFindingCollab(fid, FindingCollabPatch{
		Assignee: &assignee, SetDueAt: true, DueAt: &due, CVSSScore: &cvss,
		CVEIDs: &cves, Remediation: &remediation,
	}); err != nil || n != 1 {
		t.Fatalf("UpdateFindingCollab: n=%d err=%v", n, err)
	}
	collab, err := d.GetFindingCollab(fid)
	if err != nil || collab == nil {
		t.Fatalf("GetFindingCollab: %+v %v", collab, err)
	}
	if collab.Assignee != assignee || collab.CVSSScore != cvss || collab.Remediation != remediation {
		t.Fatalf("collab fields = %+v", collab)
	}
	if collab.DueAt == nil || !collab.DueAt.Equal(due) {
		t.Fatalf("due_at = %v want %v", collab.DueAt, due)
	}
	if len(collab.CVEIDs) != 2 {
		t.Fatalf("cve_ids = %v", collab.CVEIDs)
	}

	comment, err := d.AddFindingComment(fid, "operator", "已通知客户")
	if err != nil {
		t.Fatal(err)
	}
	if comment.ID == 0 || comment.Body != "已通知客户" {
		t.Fatalf("comment = %+v", comment)
	}
	comments, err := d.ListFindingComments(fid)
	if err != nil || len(comments) != 1 {
		t.Fatalf("comments = %+v err=%v", comments, err)
	}
	if n, err := d.DeleteFindingComment(fid, comment.ID); err != nil || n != 1 {
		t.Fatalf("delete comment: n=%d err=%v", n, err)
	}

	empty := ""
	if n, err := d.UpdateFindingCollab(fid, FindingCollabPatch{Assignee: &empty, SetDueAt: true}); err != nil || n != 1 {
		t.Fatalf("clear collaboration: n=%d err=%v", n, err)
	}
	collab, _ = d.GetFindingCollab(fid)
	if collab.Assignee != "" || collab.DueAt != nil {
		t.Fatalf("clear failed: %+v", collab)
	}
}

func TestInboxAggregatesTaskAndFindingFacts(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	task, err := d.CreateTask("inbox task", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	if err := d.SetStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	eventID, err := d.AddNotificationEvent(context.Background(), "finding_created", 987654321, notify.Snapshot{Name: "inbox finding", Severity: "high"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM notification_events WHERE id=$1`, eventID)

	items, err := d.ListInboxItems(100)
	if err != nil {
		t.Fatal(err)
	}
	var sawTask, sawFinding bool
	for _, item := range items {
		if item.ID == "task-"+i64Test(task.ID) {
			sawTask = true
		}
		if item.ID == "finding-"+i64Test(eventID) {
			sawFinding = true
		}
	}
	if !sawTask || !sawFinding {
		t.Fatalf("inbox missed seeded facts: task=%v finding=%v items=%+v", sawTask, sawFinding, items)
	}
}

func i64Test(v int64) string { return strconv.FormatInt(v, 10) }
