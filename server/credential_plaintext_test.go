package server

import (
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestTaskNodeDTOCredentialPlaintextFidelity(t *testing.T) {
	node := &db.Node{
		ID:      7,
		Kind:    db.KindCredential,
		Payload: []byte(`{"kind":"password","username":"svc","secret":"s3cr3t-value","summary":"svc 凭据"}`),
	}
	redacted := taskNodeDTO(node)
	if strings.Contains(redacted.Payload, "s3cr3t-value") || !strings.Contains(redacted.Payload, "secret_redacted") {
		t.Fatalf("default/off DTO must redact secret: %s", redacted.Payload)
	}
	if redacted.Fingerprint == "" {
		t.Fatal("fingerprint must survive redaction")
	}
	plain := taskNodeDTO(node, true)
	if !strings.Contains(plain.Payload, "s3cr3t-value") || strings.Contains(plain.Payload, "secret_redacted") {
		t.Fatalf("plaintext-on DTO must include secret: %s", plain.Payload)
	}
	if plain.Fingerprint != redacted.Fingerprint {
		t.Fatalf("fingerprint changed: %q vs %q", plain.Fingerprint, redacted.Fingerprint)
	}
}
