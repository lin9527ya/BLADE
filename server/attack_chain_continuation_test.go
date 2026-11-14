package server

import (
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestAttackChainContinuationText(t *testing.T) {
	text := attackChainContinuationText([]db.TaskEvent{
		{NodeID: 11, VulnClass: "SQLi", Severity: "critical", Summary: "login parameter injectable"},
		{NodeID: 12, VulnClass: "IDOR", Severity: "high", Summary: "user id enumerable"},
	})
	for _, want := range []string{"#11", "SQLi", "critical", "#12", "IDOR", "add_intent", "不构成必须派单"} {
		if !strings.Contains(text, want) {
			t.Fatalf("hint missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "must dispatch") {
		t.Fatalf("hint must not force intents:\n%s", text)
	}
}
