package agent

import (
	"strings"
	"testing"
)

func TestAssessFindingEvidence(t *testing.T) {
	short := strings.Repeat("x", 100)
	mech := "curl -sS -i https://t.example/api?id=1\nHTTP/1.1 200 OK\n{\"status\":\"ok\",\"data\":[1,2,3]}"
	version := "Server: nginx/1.2.3\nX-Powered-By: PHP/8.1"
	longVersion := version + "\n" + strings.Repeat("版本匹配说明", 30)
	cases := []struct {
		name, severity, evidence string
		wantBlock, wantWarn      bool
	}{
		{"short critical", "critical", short, true, false},
		{"short high", "high", short, true, false},
		{"mechanical high", "high", mech + strings.Repeat("x", 80), false, false},
		{"version only", "high", longVersion, true, false},
		{"medium short", "medium", short, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := assessFindingEvidence(c.severity, c.evidence)
			if (got.Block != "") != c.wantBlock || (got.Warn != "") != c.wantWarn {
				t.Fatalf("got block=%q warn=%q, want block=%v warn=%v", got.Block, got.Warn, c.wantBlock, c.wantWarn)
			}
		})
	}
}

func TestToolOutputLedgerVerifyEvidence(t *testing.T) {
	l := NewToolOutputLedger()
	l.Add("HTTP/1.1 200 OK\nuid=0(root) gid=0(root)\nsecret=abc12345678901234567890")
	evidence := "uid=0(root) gid=0(root)\nsecret=abc12345678901234567890"
	matched, total := l.VerifyEvidence(evidence)
	if total != 2 || matched != 2 {
		t.Fatalf("matched=%d total=%d, want 2/2", matched, total)
	}
	matched, total = l.VerifyEvidence("uid=0(root) gid=0(root)\n这是模型转述的另一行")
	if total != 2 || matched != 1 {
		t.Fatalf("partial matched=%d total=%d, want 1/2", matched, total)
	}
	if verdict := evidenceVerdict(0, 2); verdict != "none" {
		t.Fatalf("verdict=%q", verdict)
	}
}
