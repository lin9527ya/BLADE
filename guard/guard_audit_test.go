package guard

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/intercept"
)

// A blocked call must produce exactly one audit entry (block), not an extra
// "allow" recorded before the verdict was known.
func TestBlockedCallRecordsOnlyBlock(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	rule, err := d.CreateInterceptRule("audit-order-test", "tool_name", "string", "Bash", "deny",
		"测试拦截", 999, true, false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID) })

	g := NewWithInterceptor(intercept.New(d), 0)
	input, _ := json.Marshal(map[string]string{"command": "ls"})
	blocked, _, _ := g.Hooks().PreToolUse(context.Background(), "Bash", input)
	if !blocked {
		t.Fatal("rule should block the call")
	}

	entries := g.Audit()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d (%+v), want exactly one block entry", len(entries), entries)
	}
	if entries[0].Action != "block" {
		t.Fatalf("audit action = %q, want block", entries[0].Action)
	}
}
