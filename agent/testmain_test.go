package agent

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lin9527ya/blade/db"
)

// TestMain acquires a PostgreSQL advisory lock (7337741002) for the entire
// agent test suite so cross-package DELETE cleanup races with db/server
// packages are avoided when running `go test ./...`.
func TestMain(m *testing.M) {
	dsn, _, err := db.DSN()
	if err != nil {
		os.Exit(m.Run())
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil || conn.Ping() != nil {
		os.Exit(m.Run())
	}
	defer conn.Close()
	if _, err := conn.Exec(`SELECT pg_advisory_lock(7337741002)`); err != nil {
		os.Exit(m.Run())
	}
	defer conn.Exec(`SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck
	// 清掉上轮测试泄漏的 LLM profile：库里只要有 active(is_default) profile，建任务的
	// 后台目标拆解就会真发网络 LLM 调用——慢到越过用例的轮询窗口（本地偶发
	// TestCoreTaskLifecyclePG 红，CI 全新库所以稳定），还可能打到真实端点。测试都必须
	// 各自按需建 profile（CI 空库全绿即是证明），这里统一清残留，让拆解走
	// 「无 provider → 原始目标作为单目标」的确定性快路径。
	// （tasks 的 profile 外键是 ON DELETE SET NULL，清残留不会牵连任务行。）
	_, _ = conn.Exec(`DELETE FROM llm_profiles`)
	os.Exit(m.Run())
}
