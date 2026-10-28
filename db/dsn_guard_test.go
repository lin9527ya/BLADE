package db

import (
	"strings"
	"testing"

	"github.com/lin9527ya/blade/config"
)

func TestGuardTestDSN(t *testing.T) {
	app, hasApp := config.ConfigPostgresDSN()
	if hasApp {
		if err := guardTestDSN(app); err == nil || !strings.Contains(err.Error(), "应用数据库") {
			t.Fatalf("app database must be rejected, got %v", err)
		}
	}
	if err := guardTestDSN("postgres://u:p@127.0.0.1:5432/blade_ci"); err == nil {
		t.Fatal("non-test database must be rejected")
	}
	if err := guardTestDSN("postgres://u:p@127.0.0.1:5432/blade_test"); err != nil {
		t.Fatalf("test database rejected: %v", err)
	}
	if err := guardTestDSN("postgres://u:p@127.0.0.1:5432/blade_acceptance"); err != nil {
		t.Fatalf("acceptance database rejected: %v", err)
	}
}

func TestSameDatabaseIgnoresCredentials(t *testing.T) {
	if !sameDatabase("postgres://a:b@127.0.0.1:5432/blade", "postgres://c:d@127.0.0.1:5432/blade?sslmode=disable") {
		t.Fatal("same host/db should compare equal regardless of credentials/query")
	}
	if sameDatabase("postgres://a:b@127.0.0.1:5432/blade", "postgres://a:b@127.0.0.1:5432/blade_test") {
		t.Fatal("different db names must not compare equal")
	}
}
