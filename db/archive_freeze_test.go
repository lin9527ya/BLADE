package db

import (
	"context"
	"errors"
	"testing"
)

func TestArchiveFreezesFindingWritesAndRejectsEqualCountChanges(t *testing.T) {
	for _, change := range []string{"finding", "comment"} {
		t.Run(change, func(t *testing.T) {
			d, err := Open(testDSN(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Close() })
			if err := d.EnsureLLMRecordsTable(); err != nil {
				t.Fatal(err)
			}
			if err := d.EnsureLLMUsageTable(); err != nil {
				t.Fatal(err)
			}
			task, err := d.CreateTask("archive freeze fixture", "fixture", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
				_ = d.DeleteTask(task.ID)
			})
			id, err := d.AddFinding(task.ID, 0, "fixture", "fixture", SeverityLow, "summary", "evidence", "test", nil)
			if err != nil {
				t.Fatal(err)
			}
			comment, err := d.AddFindingComment(id, "operator", "original")
			if err != nil {
				t.Fatal(err)
			}
			c, err := d.CreateFindingCase(task.ID, 0, "case", "reason", SeverityLow, []int64{id}, "test")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM finding_cases WHERE id=$1`, c.ID) })
			if err := d.SetPaused(task.ID, true); err != nil {
				t.Fatal(err)
			}
			a, err := d.QueueTaskArchive(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.ClaimTaskArchiveJob(context.Background()); err != nil {
				t.Fatal(err)
			}
			snap, err := d.SnapshotTaskArchive(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			note := "updated"
			if _, err := d.UpdateFindingCollab(id, FindingCollabPatch{Remediation: &note}); !errors.Is(err, ErrTaskArchiveState) {
				t.Fatalf("collab not frozen: %v", err)
			}
			if _, err := d.AddFindingComment(id, "operator", "late"); !errors.Is(err, ErrTaskArchiveState) {
				t.Fatalf("comment not frozen: %v", err)
			}
			if err := d.RemoveFindingCaseMember(c.ID, id, "late", "operator"); !errors.Is(err, ErrTaskArchiveState) {
				t.Fatalf("case not frozen: %v", err)
			}
			// Simulate a legacy writer bypassing the public API. Same row counts
			// must not let completion discard its accepted content update.
			if change == "finding" {
				_, err = d.Exec(`UPDATE findings SET remediation='legacy update' WHERE id=$1`, id)
			} else {
				_, err = d.Exec(`UPDATE finding_comments SET body='legacy update' WHERE id=$1`, comment.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := d.CompleteTaskArchive(a.ID, snap, "/tmp/fixture.tar.zst", "sha", 1, 1); !errors.Is(err, ErrTaskArchiveState) {
				t.Fatalf("equal-count change accepted: %v", err)
			}
			if f, err := d.GetFinding(id); err != nil || f == nil {
				t.Fatal("failed archive deleted finding")
			}
		})
	}
}

func TestFindingMutationRetainsExecutionAuthority(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("finding authority fixture", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "owner"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.ClaimNode(t.Context(), intent, "worker", "open", IntentLeaseTTL, nil, false)
	if err != nil || n == nil {
		t.Fatal(err)
	}
	finding, err := d.AddFinding(task.ID, 0, "fixture", "fixture", SeverityLow, "summary", "evidence", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	a := n.WriteAuthority(task.ID, task.ExplorationID)
	stale := a
	stale.Generation++
	note := "stale mutation"
	if _, err := d.WithAuthority(stale).UpdateFindingCollab(finding, FindingCollabPatch{Remediation: &note}); !errors.Is(err, ErrStaleExecution) {
		t.Fatalf("helper lost generation fence: %v", err)
	}
	if _, err := d.WithAuthority(stale).AddFindingComment(finding, "worker", "stale"); !errors.Is(err, ErrStaleExecution) {
		t.Fatalf("comment helper lost generation fence: %v", err)
	}
	if _, err := d.WithAuthority(a).AddFindingComment(finding, "worker", "current"); err != nil {
		t.Fatalf("current authority rejected: %v", err)
	}
}
