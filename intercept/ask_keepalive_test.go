package intercept

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// TestHandleAskKeepAliveWhileWaitingNoTimeout 钉住「无超时审批」的等待期间会被判成僵尸。
//
// 意图租约的心跳由**活动**驱动（server 的 emitRenew → renewLease，只有发活动才续租）。
// 无超时的审批在 ch 上干等，期间一条活动都不发，于是租约在 db.IntentLeaseTTL 后到期、被任务
// 看门狗判成僵尸意图回收；回收会取消本 work 的 ctx，HandleAsk 随即走 ctx.Done() 分支把这次
// 审批**判成 deny**——人工只要慢于 IntentLeaseTTL，他的决定就永远落不了地，而 agent 会把这次
// 工具调用当成「平台拒绝了」继续往下走。即：人的决定被静默丢掉。
//
// 所以这条用例断言的不是"等得久一点"，而是：等待期间确实按节拍发出了 keep-alive 活动
// （= 续租心跳）。
func TestHandleAskKeepAliveWhileWaitingNoTimeout(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	// TTL/4 是 3m45s，用例等不起：把节拍调小，测的还是同一段逻辑。
	oldInterval := askKeepAliveInterval
	askKeepAliveInterval = 30 * time.Millisecond
	defer func() { askKeepAliveInterval = oldInterval }()

	// 共享测试库不留残留：记下调用前的最大 id，跑完删掉自己造的行。
	var before int64
	if err := d.QueryRow(`SELECT COALESCE(max(id),0) FROM intercept_pending`).Scan(&before); err != nil {
		t.Fatalf("读 pending 基线: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_pending WHERE id > $1`, before) })

	ic := New(d)
	emitted := make(chan db.Activity, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithTaskContext(ctx, "1", "worker · #1", func(a db.Activity) {
		select {
		case emitted <- a:
		default:
		}
	})

	// TimeoutEnabled 留零值 = 规则没启用超时，正是要覆盖的那条路径（有超时的走 timer 分支）。
	dec := Decision{Action: "ask", RuleName: "[内置] 测试规则", Message: "需要人工确认"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ic.HandleAsk(ctx, 0, dec, "Bash", json.RawMessage(`{"command":"id"}`))
	}()

	// 注意：等待之前会先发一条 intercept_request（审批请求本身），跳过它等 keep-alive。
	deadline := time.After(5 * time.Second)
	for kept := false; !kept; {
		select {
		case a := <-emitted:
			kept = a.Kind == "intercept_wait" // 拿到了 keep-alive，正是本用例要断言的东西
		case <-deadline:
			t.Fatal("等待期间没有发出 intercept_wait：租约不会被续，这次审批会被看门狗判成 deny")
		}
	}

	// 取消后必须立刻返回（ctx.Done() 分支要真的解开等待，而不是继续等）。
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后 HandleAsk 未返回")
	}
}
