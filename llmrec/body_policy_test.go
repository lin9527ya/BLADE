package llmrec

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/db"
	"iter"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAuditBodyProtectsWireAndBounds(t *testing.T) {
	for _, s := range []string{`{"authorization":"secret-value","system":"hello"}`, `{"type":"base64","media_type":"image/png","data":"secret-value"}`, "data: {\"api_key\":\"secret-value\"}\n", `{"token":"secret-value`} {
		if got := auditBody(s, true); strings.Contains(got, "secret-value") {
			t.Errorf("sensitive data in %q", got)
		}
	}
	got := auditBody(strings.Repeat("中", 40000), true)
	if len(got) > ErrorBodyLimit || !utf8.ValidString(got) {
		t.Fatal("invalid bound")
	}
}

type failingAuditFixture struct{ completeProvider }

func (failingAuditFixture) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{}, "", llm.Usage{InputTokens: 7}, errors.New("Authorization: Bearer sk-fixture-token-123")
}
func (failingAuditFixture) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: llm.Usage{InputTokens: 7}}, nil)
		yield(llm.StreamEvent{}, errors.New("Authorization: Bearer sk-fixture-token-123"))
	}
}
func TestAuditModeCallAccounting(t *testing.T) {
	dsn := os.Getenv("BLADE_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err = d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"off", "errors", "full"} {
		name := "policy-" + mode + uuid.NewString()
		req := llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("Bearer sk-fixture-token-123" + strings.Repeat("测", 26000))}}}}
		for _, p := range []llm.Provider{meteredStreamProvider{}, failingAuditFixture{}} {
			r := Wrap(p, d, "fixture", name, "", "", nil).WithBodyMode(func() string { return mode })
			_, _, _, _ = r.Complete(t.Context(), req)
			for range r.Stream(t.Context(), req) {
			}
		}
		var metering, rows, okCount, maxBytes int
		var leaked bool
		if err = d.QueryRow(`SELECT count(*) FROM llm_usage WHERE profile_name=$1`, name).Scan(&metering); err != nil {
			t.Fatal(err)
		}
		if err = d.QueryRow(`SELECT count(*),count(*) FILTER(WHERE status='ok'),coalesce(max(octet_length(request_body)),0),coalesce(bool_or(request_body LIKE '%sk-fixture-token%' OR error LIKE '%sk-fixture-token%'),false) FROM llm_records WHERE profile_name=$1`, name).Scan(&rows, &okCount, &maxBytes, &leaked); err != nil {
			t.Fatal(err)
		}
		_, _ = d.Exec(`DELETE FROM llm_records WHERE profile_name=$1`, name)
		_, _ = d.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, name)
		if metering != 4 || leaked || rows != map[string]int{"off": 0, "errors": 2, "full": 4}[mode] || mode == "errors" && (maxBytes > ErrorBodyLimit || okCount != 0) {
			t.Fatalf("mode=%s usage=%d records=%d successes=%d size=%d leak=%v", mode, metering, rows, okCount, maxBytes, leaked)
		}
	}
}
