package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// instanceLockKey 是「本库只有一个 BLADE 实例在跑」的会话级 advisory lock。
//
// 为什么需要它：引擎、调度器、启动恢复都假设**独占**——启动时 ResetRunningIntents
// 会把另一个实例正在跑的意图抢回来，reconcileConcurrency 只看本进程的任务，流量代理
// 绑定固定端口（第二个实例静默丢失录制）。这些不是"性能差一点"，是两个实例互相拆台。
// 与其在文档里写"不要起两个"，不如让第二个起不来：拿不到锁就带着可读理由退出。
//
// 与已有的锁键不重叠：1001 schema 迁移、1002 测试套件、1003 公司作用域、
// 1004 证据写入、1010 复测。
const instanceLockKey int64 = 7337741005

// ErrInstanceLocked 表示本库已有另一个实例持有实例锁。
var ErrInstanceLocked = errors.New("另一个 BLADE 实例正在使用同一个数据库")

// instanceLock 持有那条 pin 住的连接——advisory lock 是会话级的，连接一关就释放，
// 所以必须一直攥着它，不能还回连接池。
type instanceLock struct {
	mu   sync.Mutex
	conn *sql.Conn
}

// AcquireInstanceLock 尝试取得实例独占锁（非阻塞）。
//
// 成功返回一个持有者；失败且库上已有实例时返回 ErrInstanceLocked。
// 调用方应在进程退出/DB 关闭时 Release。
//
// 环境变量 BLADE_ALLOW_MULTI_INSTANCE=1 是显式逃生口：某些排障场景（只读副本、
// 短暂双跑做数据比对）确实需要，但必须由人显式承担互相拆台的风险。
func (d *DB) AcquireInstanceLock(ctx context.Context) error {
	if v := strings.TrimSpace(os.Getenv("BLADE_ALLOW_MULTI_INSTANCE")); v == "1" || strings.EqualFold(v, "true") {
		return nil
	}
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取实例锁连接: %w", err)
	}
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, instanceLockKey).Scan(&got); err != nil {
		conn.Close()
		return fmt.Errorf("获取实例锁: %w", err)
	}
	if !got {
		conn.Close()
		return ErrInstanceLocked
	}
	d.lock.mu.Lock()
	d.lock.conn = conn
	d.lock.mu.Unlock()
	return nil
}

// ReleaseInstanceLock 释放实例锁（幂等）。关闭连接本身就会释放，这里显式解锁是为了
// 让"进程还在、但实例已经让位"这种路径也能立刻生效。
func (d *DB) ReleaseInstanceLock() {
	d.lock.mu.Lock()
	conn := d.lock.conn
	d.lock.conn = nil
	d.lock.mu.Unlock()
	if conn == nil {
		return
	}
	_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, instanceLockKey)
	_ = conn.Close()
}
