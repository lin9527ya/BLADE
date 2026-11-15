package db

import "testing"

func TestAPITokenLifecycleAndAudit(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	token, plain, err := d.CreateAPIToken("test token")
	if err != nil {
		t.Fatal(err)
	}
	// no delete method: revoke is the product operation.
	defer d.RevokeAPIToken(token.ID) //nolint:errcheck
	if plain == "" || token.ID == 0 {
		t.Fatalf("created token = %+v plain=%q", token, plain)
	}
	name, ok, err := d.ValidateAPIToken(plain)
	if err != nil || !ok || name != "test token" {
		t.Fatalf("validate: name=%q ok=%v err=%v", name, ok, err)
	}
	if _, ok, _ := d.ValidateAPIToken("blt_invalid"); ok {
		t.Fatal("invalid token validated")
	}
	if _, err := d.RevokeAPIToken(token.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.ValidateAPIToken(plain); ok {
		t.Fatal("revoked token still validates")
	}

	if err := d.InsertAuditEvent("admin", "POST", "/api/test", 200, "127.0.0.1", "test"); err != nil {
		t.Fatal(err)
	}
	events, err := d.ListAuditEvents(10)
	if err != nil || len(events) == 0 {
		t.Fatalf("audit events = %+v err=%v", events, err)
	}
	if events[0].Path != "/api/test" || events[0].Actor != "admin" {
		t.Fatalf("latest audit = %+v", events[0])
	}
}
