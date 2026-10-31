package db

import (
	"context"
	"errors"
	"testing"
)

// TestInstanceLockIsExclusive 钉住「同一库只允许一个实例」的判据。
//
// 为什么值得单测：引擎/调度器/启动恢复都假设独占，第二个实例会把第一个正在跑的
// 意图抢回来（ResetRunningIntents）、在固定端口上抢不到流量代理。这些失败都不是
// 报错，而是"数据看起来对、其实两个进程在互相拆台"——所以必须在启动时挡住。
func TestInstanceLockIsExclusive(t *testing.T) {
	dsn := testDSN(t)
	first, err := Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer first.Close()

	ctx := context.Background()
	if err := first.AcquireInstanceLock(ctx); err != nil {
		t.Fatalf("第一个实例应拿到锁：%v", err)
	}

	second, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.AcquireInstanceLock(ctx); !errors.Is(err, ErrInstanceLocked) {
		t.Fatalf("第二个实例应被拒绝，得到 %v", err)
	}

	// 释放后第二个实例可以接手（热重启 / 同进程重建 Manager 的路径）。
	first.ReleaseInstanceLock()
	if err := second.AcquireInstanceLock(ctx); err != nil {
		t.Fatalf("释放后第二个实例应能拿到锁：%v", err)
	}
	second.ReleaseInstanceLock()

	// 显式逃生口。
	t.Setenv("BLADE_ALLOW_MULTI_INSTANCE", "1")
	third, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if err := third.AcquireInstanceLock(ctx); err != nil {
		t.Fatalf("BLADE_ALLOW_MULTI_INSTANCE=1 时应放行：%v", err)
	}
}
