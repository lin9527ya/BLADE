package db

import (
	"fmt"
	"testing"
	"time"
)

func TestCredentialLifecycleClassification(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour).Format(time.RFC3339)
	past := now.Add(-48 * time.Hour).Format(time.RFC3339)
	cases := []struct {
		name string
		in   CredentialLifecycleInput
		want string
	}{
		{"lost", CredentialLifecycleInput{State: StateLootLost}, CredLifecycleLost},
		{"locked", CredentialLifecycleInput{State: StateLootActive, LastResult: "locked"}, CredLifecycleLocked},
		{"invalid", CredentialLifecycleInput{State: StateLootActive, LastResult: "failed"}, CredLifecycleInvalid},
		{"expired", CredentialLifecycleInput{State: StateLootActive, Targets: []CredentialTarget{{Host: "a", Verified: true, ValidUntil: past}}}, CredLifecycleExpired},
		{"expiring", CredentialLifecycleInput{State: StateLootActive, Targets: []CredentialTarget{{Host: "a", Verified: true, ValidUntil: future}}}, CredLifecycleExpiring},
		{"verified", CredentialLifecycleInput{State: StateLootActive, Targets: []CredentialTarget{{Host: "a", Verified: true}}, LastCheckAt: &now}, CredLifecycleVerified},
		{"stale", CredentialLifecycleInput{State: StateLootActive, Targets: []CredentialTarget{{Host: "a", Verified: true, ObservedAt: now.Add(-40 * 24 * time.Hour).Format(time.RFC3339)}}}, CredLifecycleStale},
		{"unverified", CredentialLifecycleInput{State: StateLootActive, Targets: []CredentialTarget{{Host: "a"}}}, CredLifecycleUnverified},
		{"unknown", CredentialLifecycleInput{State: StateLootActive}, CredLifecycleUnknown},
	}
	for _, c := range cases {
		c.in.Now = now
		if got := ComputeCredentialLifecycle(c.in); got != c.want {
			t.Errorf("%s: lifecycle = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCredentialTargetsProjectionSync(t *testing.T) {
	_, s := intentLeaseFixture(t)
	id, err := s.AddNode(KindCredential, map[string]any{
		"summary": "projection test",
		"targets": []CredentialTarget{
			{Host: "A.Example", Port: 22, Service: "SSH", Verified: true, Evidence: "ok"},
			{Host: "b.example", Port: 445, Service: "smb"},
		},
	}, 5, StateLootActive, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d(s).Exec(`DELETE FROM exploration_nodes WHERE id=$1`, id)
	count := func() int {
		var n int
		if err := d(s).QueryRow(`SELECT COUNT(*) FROM credential_targets WHERE node_id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(); got != 2 {
		t.Fatalf("projection rows = %d, want 2", got)
	}
	var host, service string
	var verified bool
	if err := d(s).QueryRow(`SELECT host, service, verified FROM credential_targets WHERE node_id=$1 AND ordinal=1`, id).Scan(&host, &service, &verified); err != nil {
		t.Fatal(err)
	}
	if host != "a.example" || service != "ssh" || !verified {
		t.Fatalf("projection row = %q/%q/%v", host, service, verified)
	}
	if err := s.SetNodePayload(id, KindCredential, map[string]any{
		"summary": "projection test",
		"targets": []CredentialTarget{{Host: "c.example", Service: "rdp"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 1 {
		t.Fatalf("projection rows after update = %d, want 1", got)
	}
}

// d 只是从 ExplorationStore 取回底层 DB，避免测试里重复传参。
func d(s *ExplorationStore) *DB { return s.db }

var _ = fmt.Sprint
