package db

import "testing"

// TestTokenUsedByTask sums the per-task ledger and defaults to 0 for unknown tasks.
func TestTokenUsedByTask(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatalf("ensure llm_usage table: %v", err)
	}
	taskID := "99001-budget-test"
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM llm_usage WHERE task_id=$1`, taskID)
		d.Close()
	})
	if err := d.InsertLLMUsage(&LLMUsage{TaskID: taskID, InputTokens: 100, OutputTokens: 50, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertLLMUsage(&LLMUsage{TaskID: taskID, InputTokens: 30, OutputTokens: 20, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	used, err := d.TokenUsedByTask(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if used != 200 {
		t.Fatalf("used=%d want 200", used)
	}
	missing, err := d.TokenUsedByTask("no-such-task-99001")
	if err != nil || missing != 0 {
		t.Fatalf("missing=%d err=%v want 0/nil", missing, err)
	}
}
