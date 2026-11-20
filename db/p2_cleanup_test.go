package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestHardDeleteRemovesBlackboardJournal(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("p2-bb-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	exp := task.ExplorationID
	if _, err := d.Exec(`INSERT INTO blackboard_operations(id,exploration_id,reason_code,detail) VALUES($1,$2,'fixture','x')`, fmt.Sprintf("op-%d", task.ID), exp); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO blackboard_events(exploration_id,seq,epoch,entity,object_key,operation) VALUES($1,999999,'fixture','node','x','update')`, exp); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO blackboard_outbox(transaction_id,exploration_id,entity,object_key,operation) VALUES(999999,$1,'node','x','update')`, exp); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"blackboard_versions", "blackboard_operations", "blackboard_events", "blackboard_outbox"} {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM `+table+` WHERE exploration_id=$1`, exp).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s left %d rows (err=%v)", table, n, err)
		}
	}
}

func TestActivityListSystemReturnsOnlyAuditRows(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("p2-sys-%d", time.Now().UnixNano()), "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	for _, a := range []Activity{
		{Worker: "worker", Kind: "tool_use", Summary: "ignored"},
		{Worker: "system", Kind: "system", Summary: "system event"},
		{Worker: "planner", Kind: "llm_switch", Summary: "switch"},
	} {
		if _, err := store.AppendActivity(a); err != nil {
			t.Fatal(err)
		}
	}
	items, cursor, err := store.ActivityListSystem(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Worker != "system" || items[1].Kind != "llm_switch" || cursor != items[len(items)-1].ID {
		t.Fatalf("system activity = %+v cursor=%d", items, cursor)
	}
	_ = context.Background()
}
