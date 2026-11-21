package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestArchiveRejectsChildWrittenAfterSnapshot(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	// llm_records / llm_usage 由服务启动时惰性建表，CI 的全新库还没有它们；
	// 归档快照会读取这两张表，测试必须先按生产启动顺序补建。
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("archive-race-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
		_ = d.DeleteTask(task.ID)
	})
	node, err := d.Exploration(task.ExplorationID).AddNode(KindFact, map[string]any{"summary": "root"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	finding, err := d.AddFinding(task.ID, node, "archive-race", "finding", SeverityHigh, "s", "e", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A human comment arrives after the package snapshot. The package must not
	// cascade-delete it silently: completion fails before any destructive write.
	if _, err := d.Exec(`INSERT INTO finding_comments(finding_id,author,body) VALUES ($1,'operator','late comment')`, finding); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/incomplete.tar.zst", "abc", 1, 1); !errors.Is(err, ErrTaskArchiveState) {
		t.Fatalf("late child write must reject archive, got %v", err)
	}
	var comments int
	if err := d.QueryRow(`SELECT count(*) FROM finding_comments WHERE finding_id=$1`, finding).Scan(&comments); err != nil || comments != 1 {
		t.Fatalf("destructive delete ran despite failed completeness check: %d %v", comments, err)
	}
}
