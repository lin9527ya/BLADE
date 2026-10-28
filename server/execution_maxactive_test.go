package server

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestSubmitRefusesWhenMaxActiveReached 钉住「本任务同时执行数上限」。
//
// P24 的资源限额限的是**每一条**执行能吃多少（ulimit 前导）；这条限的是**同时有几条**。
// 两者互补：单条被 CPU 限额兜住，但 agent 若起了一百条，机器照样被打满。此前没有这条。
func TestSubmitRefusesWhenMaxActiveReached(t *testing.T) {
	svc, pg, _, task := newExecFixture(t)

	// 共享测试库不留残留：改前记录、跑完还原。newExecFixture 的清理是**先**注册的，
	// 所以这里的 t.Cleanup 先执行——那时连接池还开着（defer/t.Cleanup 都是后进先出）。
	prev, had, err := pg.GetSetting(settingExecMaxActive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = pg.SetSetting(settingExecMaxActive, prev)
			return
		}
		_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, settingExecMaxActive)
	})

	if err := pg.SetSetting(settingExecMaxActive, "1"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := svc.Submit(ctx, task, 0, "test-worker", "sleep 30", "占住唯一的名额", 0)
	if err != nil {
		t.Fatalf("上限 1 时第一条应当放行: %v", err)
	}
	t.Cleanup(func() { _, _ = svc.Cancel(context.Background(), first.ID, "用例清理") })

	if _, err := svc.Submit(ctx, task, 0, "test-worker", "sleep 30", "第二条应被拒", 0); err == nil {
		t.Fatal("已达上限（1/1）时第二条必须被拒——没有这条闸，agent 起一百条就把机器打满了")
	} else if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("拒绝理由应当说清是撞了上限，而不是含糊报错：%v", err)
	}

	// 0 = 不限：必须放行（与 P24 三个轴的 0 语义一致）。
	if err := pg.SetSetting(settingExecMaxActive, "0"); err != nil {
		t.Fatal(err)
	}
	second, err := svc.Submit(ctx, task, 0, "test-worker", "sleep 30", "0=不限应放行", 0)
	if err != nil {
		t.Fatalf("上限设为 0（不限）时应当放行: %v", err)
	}
	t.Cleanup(func() { _, _ = svc.Cancel(context.Background(), second.ID, "用例清理") })
}

// Admission must be atomic: COUNT followed by a separate INSERT lets concurrent
// workers overshoot the configured limit.
func TestSubmitConcurrentAdmissionRespectsLimit(t *testing.T) {
	svc, pg, _, task := newExecFixture(t)
	prev, had, err := pg.GetSetting(settingExecMaxActive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = pg.SetSetting(settingExecMaxActive, prev)
			return
		}
		_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, settingExecMaxActive)
	})
	if err := pg.SetSetting(settingExecMaxActive, "2"); err != nil {
		t.Fatal(err)
	}

	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ids []int64
	var failures []string
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			st, err := svc.Submit(context.Background(), task, 0, "test-worker", "sleep 30", "并发准入", 0)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err.Error())
				return
			}
			ids = append(ids, st.ID)
		}()
	}
	close(start)
	wg.Wait()
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = svc.Cancel(context.Background(), id, "用例清理")
		}
	})

	if len(ids) != 2 || len(failures) != workers-2 {
		t.Fatalf("admitted=%d rejected=%d, want 2/%d", len(ids), len(failures), workers-2)
	}
	for _, message := range failures {
		if !strings.Contains(message, "上限") {
			t.Fatalf("rejection should name the limit, got %q", message)
		}
	}
}
