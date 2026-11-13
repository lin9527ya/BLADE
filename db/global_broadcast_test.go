package db

import (
	"context"
	"fmt"
	"testing"
)

// TestGlobalNodesPageJoinsTasksAndMapsSession 覆盖全局播报的三条核心语义：
// 跨任务聚合、deleted_at 任务剔除、以及 yields 边到 intent 会话的回填。
func TestGlobalNodesPageJoinsTasksAndMapsSession(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	taskA, err := d.CreateTaskWithOptions("global broadcast A", "目标 A", TaskCreateOptions{Name: "广播任务 A"})
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := d.CreateTaskWithOptions("global broadcast B", "目标 B", TaskCreateOptions{Name: "广播任务 B"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteTaskCascade(taskA.ID, true, true, true) })
	t.Cleanup(func() { _, _ = d.DeleteTaskCascade(taskB.ID, true, true, true) })

	storeA := d.Exploration(taskA.ExplorationID)
	intentID, err := storeA.AddNode(KindIntent, map[string]any{"summary": "needle-intent"}, 1, "open", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	factID, err := storeA.AddNode(KindFact, map[string]any{"summary": "needle-fact"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO exploration_edges(exploration_id, src_id, dst_id, rel)
VALUES ($1,$2,$3,'yields')`, taskA.ExplorationID, intentID, factID); err != nil {
		t.Fatal(err)
	}
	derivedID, err := storeA.AddNode(KindFact, map[string]any{"summary": "needle-derived"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeA.Link(factID, RelDerivedFrom, derivedID); err != nil {
		t.Fatal(err)
	}
	storeB := d.Exploration(taskB.ExplorationID)
	if _, err := storeB.AddNode(KindFact, map[string]any{"summary": "needle-fact-b"}, 0, "confirmed", "worker", nil); err != nil {
		t.Fatal(err)
	}

	rows, total, err := d.GlobalNodesPage(GlobalNodeFilter{Query: "needle-fact"}, 0, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("cross-task query: total=%d rows=%+v, want A+B", total, rows)
	}
	derivedRows, derivedTotal, err := d.GlobalNodesPage(GlobalNodeFilter{Query: "needle-derived"}, 0, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if derivedTotal != 1 || len(derivedRows) != 1 || derivedRows[0].SessionKey != "intent:"+i64s(intentID) {
		t.Fatalf("derived fact must inherit the intent session: total=%d rows=%+v", derivedTotal, derivedRows)
	}
	var sawA, sawB bool
	for _, row := range rows {
		switch row.TaskID {
		case taskA.ID:
			sawA = true
			if row.TaskName != "广播任务 A" {
				t.Fatalf("task A name = %q", row.TaskName)
			}
			if row.SessionKey != "intent:"+i64s(intentID) {
				t.Fatalf("yields session key = %q, want intent:%d", row.SessionKey, intentID)
			}
		case taskB.ID:
			sawB = true
			if row.SessionKey != "" {
				t.Fatalf("task B has no yields edge, got session %q", row.SessionKey)
			}
		}
	}
	if !sawA || !sawB {
		t.Fatalf("missing task rows: A=%v B=%v rows=%+v", sawA, sawB, rows)
	}

	// 删除任务后，其节点不得再出现在全局播报里。
	if _, err := d.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, taskB.ID); err != nil {
		t.Fatal(err)
	}
	rows, total, err = d.GlobalNodesPage(GlobalNodeFilter{Query: "needle-fact"}, 0, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].TaskID != taskA.ID {
		t.Fatalf("deleted task leaked: total=%d rows=%+v", total, rows)
	}
	if _, err := d.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, taskA.ID); err != nil {
		t.Fatal(err)
	}
	rows, _, err = d.GlobalNodesPage(GlobalNodeFilter{Query: "needle-fact"}, 0, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskState != "paused" {
		t.Fatalf("paused task must project paused, got rows=%+v", rows)
	}
}

// TestGlobalNodesPageConfirmedFindings 复用播报板的展示口径：pending finding
// 不能出现在全局流里，人工确认后才进入。
func TestGlobalNodesPageConfirmedFindings(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("global finding", "目标", TaskCreateOptions{Name: "finding 广播"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteTaskCascade(task.ID, true, true, true) })
	store := d.Exploration(task.ExplorationID)
	rec, err := store.RecordFinding(context.Background(), RecordFindingInput{
		TaskID: task.ID, Worker: "worker", VulnClass: "xss", Name: "needle-pending",
		Severity: "medium", Summary: "needle-pending", Confidence: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, total, err := d.GlobalNodesPage(GlobalNodeFilter{Query: "needle-pending", OnlyConfirmedFindings: true}, 0, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("pending finding must be hidden: total=%d rows=%+v", total, rows)
	}
	pendingRows, pendingTotal, err := d.GlobalNodesPage(GlobalNodeFilter{
		Query: "needle-pending", FindingStatuses: []string{FindingPending},
	}, 0, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if pendingTotal != 1 || len(pendingRows) != 1 || pendingRows[0].Node.FindingStatus != FindingPending {
		t.Fatalf("finding_status filter must expose pending explicitly: total=%d rows=%+v", pendingTotal, pendingRows)
	}
	if _, err := d.SetFindingStatus(rec.FindingID, FindingConfirmed); err != nil {
		t.Fatal(err)
	}
	rows, total, err = d.GlobalNodesPage(GlobalNodeFilter{Query: "needle-pending", OnlyConfirmedFindings: true}, 0, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].Node.FindingStatus != FindingConfirmed {
		t.Fatalf("confirmed finding missing: total=%d rows=%+v", total, rows)
	}
}

func i64s(v int64) string {
	return fmt.Sprintf("%d", v)
}
