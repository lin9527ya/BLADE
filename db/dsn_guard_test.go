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

// TestIsTestDatabase 守住破坏性测试清理的开关：只有 test/acceptance 库允许执行
// 整表 DELETE。应用库（blade）和普通名字必须判为 false —— 否则本地用
// BLADE_TEST_ALLOW_UNSAFE=1 跑测试时会把运维的 LLM 配置清空。
func TestIsTestDatabase(t *testing.T) {
	cases := map[string]bool{
		"postgres://u:p@127.0.0.1:5432/blade":         false,
		"postgres://u:p@127.0.0.1:5432/blade_test":    true,
		"postgres://u:p@127.0.0.1:5432/blade_ci":      false,
		"postgres://u:p@127.0.0.1:5432/blade_test_ci": true,
		"postgres://u:p@127.0.0.1:5432/acceptance":    true,
	}
	for dsn, want := range cases {
		if got := IsTestDatabase(dsn); got != want {
			t.Errorf("IsTestDatabase(%q) = %v, want %v", dsn, got, want)
		}
	}
}
