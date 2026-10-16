package server

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lin9527ya/blade/db"
)

// degrade 报告"库不可用"时的降级。测试仍然照跑（守住本地无库也能 `go test` 的能力），
// 但跨包 advisory lock 与 llm_profiles 清理都拿不到——后果是**偶发、无规律的跨包竞态红**，
// 而不是一句"数据库没连上"。所以这里把降级和它具体卡在哪一步、错误是什么都打出来，
// 让它在输出里看得见、可诊断，而不是让失败以"看起来像代码问题"的形态出现。
func degrade(step string, err error) {
	fmt.Fprintf(os.Stderr,
		"\n!!! server 测试降级：%s 失败（%v）\n"+
			"!!! 跨包 advisory lock 与 llm_profiles 清理未生效。\n"+
			"!!! 此后的失败可能是跨包竞态而非代码问题；要确定性请把 BLADE_PG_DSN 指向一个可用的库。\n\n",
		step, err)
}

// TestMain acquires a PostgreSQL advisory lock (7337741002) for the entire
// server test suite so cross-package DELETE cleanup races with db/agent
// packages are avoided when running `go test ./...`.
func TestMain(m *testing.M) {
	dsn, _, err := db.DSN()
	if err != nil {
		degrade("读取数据库配置", err)
		os.Exit(m.Run())
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		degrade("打开数据库连接", err)
		os.Exit(m.Run())
	}
	if err := conn.Ping(); err != nil {
		degrade("连接数据库", err)
		os.Exit(m.Run())
	}
	defer conn.Close()
	if _, err := conn.Exec(`SELECT pg_advisory_lock(7337741002)`); err != nil {
		degrade("获取跨包测试锁", err)
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
