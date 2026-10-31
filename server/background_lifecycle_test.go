package server

import (
	"context"
	"testing"
	"time"
)

// TestShutdownStopsBackgroundLoops 钉住关停契约：Shutdown 必须在 ctx 到期前把
// New 起的后台循环停掉并返回 0。
//
// 为什么值得单独测：这些循环连的是同一个库，main 在 return 之后 defer 的
// mgr.Close() 会关连接池——Shutdown 不把它们等干净，关停时它们还在查库，
// 日志里就是 `sql: database is closed`，在途的归档/检查点写入被静默丢掉。
func TestShutdownStopsBackgroundLoops(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	if m.pg == nil {
		t.Skip("postgres unavailable — skipping")
	}
	t.Cleanup(func() { _ = m.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	td := t.TempDir()
	s := New(ctx, m, td, td, td)

	// 测试进程里 New 不启动周期循环（见 server.New 的 testing.Testing 守卫），
	// 所以这里手工登记一个「活到 ctx 取消」的循环，验证 Shutdown 会等它退出。
	stopped := make(chan struct{})
	s.loops.start(func() {
		<-s.ctx.Done()
		close(stopped)
	})

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if left := s.Shutdown(shutdownCtx); left != 0 {
		t.Fatalf("Shutdown 应在超时前停掉全部后台循环，仍剩 %d 个", left)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Shutdown 返回了，但登记的循环还在跑")
	}
}

// TestRuntimeCtxFollowsShutdown 钉住「所有者已不存在」时的 ctx 语义。
//
// runtimeCtx 此前对未知任务返回 context.Background()——Done() 是 nil channel，
// 等于让调用方（执行观察者）永远等不到取消，每 5 秒查库最长 24 小时，跨过关停。
// 现在兜底到 Engine 的 base ctx：可取消，但不会在任务只是「还没启动」时立刻结束。
func TestRuntimeCtxFollowsShutdown(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	e := NewEngine(m)
	base, cancel := context.WithCancel(context.Background())
	e.SetBaseContext(base)
	ctx := e.runtimeCtx("no-such-task")
	select {
	case <-ctx.Done():
		t.Fatal("任务只是没有运行时条目，不该立刻结束")
	default:
	}
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("关停（base ctx 取消）后兜底 ctx 必须一起结束，而不是永远不结束的 Background")
	}
}
