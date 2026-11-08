package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestUsageDiagnosticsColdStartAndFilters(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	task := fmt.Sprintf("diag-%d", time.Now().UnixNano())
	session := task + "-session"
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM llm_usage WHERE session_id=$1`, session) })
	for _, u := range []LLMUsage{
		{TaskID: task, Worker: "planner", SessionID: session, InputTokens: 20, Status: "ok"},
		{TaskID: task, Worker: "planner", SessionID: session, InputTokens: 100, CacheRead: 80, Status: "ok"},
		{TaskID: task, Worker: "worker", SessionID: session, Status: "error"},
	} {
		if err := d.InsertLLMUsage(&u); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.UsageDiagnostics(context.Background(), task, "planner", "", session, 0, 100)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].FirstCall || !rows[1].FirstCall {
		t.Fatal("session boundary wrong")
	}
	zero, err := d.UsageDiagnostics(context.Background(), task, "", "zero", "", 0, 100)
	if err != nil || len(zero) != 1 || zero[0].Input != 20 {
		t.Fatalf("zero filter=%v err=%v", zero, err)
	}
	older, err := d.UsageDiagnostics(context.Background(), task, "planner", "", "", rows[0].ID, 100)
	if err != nil || len(older) != 1 {
		t.Fatalf("pagination=%v err=%v", older, err)
	}
	if err := d.InsertLLMUsage(&LLMUsage{Worker: "planner", SessionID: session, InputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertLLMUsage(&LLMUsage{Worker: "planner", SessionID: session, InputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	anonymous, err := d.UsageDiagnostics(context.Background(), "", "planner", "", session, 0, 100)
	if err != nil || len(anonymous) != 4 || anonymous[0].FirstCall || !anonymous[1].FirstCall {
		t.Fatalf("non-task boundary=%v err=%v", anonymous, err)
	}
}
