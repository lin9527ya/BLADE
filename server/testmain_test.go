package server

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lin9527ya/blade/db"
	"go.uber.org/goleak"
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
	requireDB := os.Getenv("BLADE_PG_DSN") != "" || os.Getenv("BLADE_REQUIRE_TESTS") == "1"
	fail := func(step string, err error) {
		if requireDB {
			fmt.Fprintf(os.Stderr, "!!! server 测试要求真实数据库（%s）：%v\n", step, err)
			os.Exit(1)
		}
		degrade(step, err)
		os.Exit(m.Run())
	}
	dsn, _, err := db.DSN()
	if err != nil {
		fail("读取数据库配置", err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		fail("打开数据库连接", err)
	}
	if err := conn.Ping(); err != nil {
		fail("连接数据库", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`SELECT pg_advisory_lock(7337741002)`); err != nil {
		fail("获取跨包测试锁", err)
	}
	defer conn.Exec(`SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck
	// 只对确认是测试库的连接做破坏性清理。db.DSN 已在测试二进制里拒绝了应用库，
	// 这里再按库名兜一道：将来有人为了排障临时放开 DSN 护栏，也不会顺手清空生产配置。
	if !db.IsTestDatabase(dsn) {
		fmt.Fprintf(os.Stderr, "\n!!! server 测试跳过 llm_profiles 清理：DSN 不是测试库（%s）\n\n", db.DatabaseNameForLog(dsn))
	} else {
		// 清掉上轮测试泄漏的 LLM profile：库里只要有 active(is_default) profile，建任务的
		// 后台目标拆解就会真发网络 LLM 调用——慢到越过用例的轮询窗口（本地偶发
		// TestCoreTaskLifecyclePG 红，CI 全新库所以稳定），还可能打到真实端点。测试都必须
		// 各自按需建 profile（CI 空库全绿即是证明），这里统一清残留，让拆解走
		// 「无 provider → 原始目标作为单目标」的确定性快路径。
		// （tasks 的 profile 外键是 ON DELETE SET NULL，清残留不会牵连任务行。）
		_, _ = conn.Exec(`DELETE FROM llm_profiles`)
	}
	code := m.Run()
	// 关掉 TestMain 自己那条连接：os.Exit 不会跑 defer，不显式关的话
	// database/sql 的 connectionOpener 会一直活着，goleak 门禁会把它报成泄漏。
	_ = conn.Close()
	// 停掉进程级日志持久化。logSink 是单例、writer 活到显式 CloseDB：最后一个
	// 用例的 Manager 关了之后它仍会持有一条 goroutine 与一个已关闭的池。
	logSink.CloseDB()
	if code == 0 {
		// goroutine 泄漏门禁：本包起的后台工作必须随用例结束。曾有一个用例留下
		// 仍在 tick 的 notifier，在后面的用例运行时领取别人的投递行，把
		// TestNotifyDigestSegmentsAndDefersRemainder 偶发弄红。校验放在这里而不是
		// 每个用例里，是为了让「泄漏」以一条明确的报告收场，而不是随机的功能失败。
		if err := goleak.Find(goleakOptions()...); err != nil {
			fmt.Fprintf(os.Stderr, "!!! server 测试结束仍有 goroutine 存活（泄漏）：\n%v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

// goleakOptions 只忽略**必然活到进程退出**的项，每条都写明理由。
//
// 刻意不用 goleak.IgnoreTopFunction("github.com/lin9527ya/blade/server.*") 这种宽泛
// 白名单：那等于把整个包豁免，门禁也就失去了意义。新增忽略项之前先问一句
// 「它为什么不能在本用例结束时退出」。
func goleakOptions() []goleak.Option {
	return []goleak.Option{
		// pgx 的连接池在 Close 后仍会短暂跑清理 goroutine（health check / conn
		// 回收），它们由驱动持有、不属于本包，且不碰我们的状态。
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck"),
		goleak.IgnoreAnyFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).triggerHealthCheck"),
		// database/sql 在连接池 Close 之后仍可能留一个 connectionOpener 在 select 上
		// 等连接归还（驱动内部，直到 GC 回收 *DB 才退出）。同样不碰本包状态。
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
	}
}
