package server

import (
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 订阅者缓冲满时不能静默丢事件：必须留下 overflow 标记，SSE handler 据此断开，
// 让 EventSource 用 Last-Event-ID 从 DB 重放缺口。
func TestBroadcasterOverflowMarksSubscriber(t *testing.T) {
	b := NewBroadcaster()
	ch, unsub := b.Subscribe("task-1")
	defer unsub()
	for i := 0; i < 300; i++ {
		b.Publish("task-1", db.Activity{ID: int64(i + 1), Kind: "text"})
	}
	if !b.Overflowed("task-1", ch) {
		t.Fatal("a full subscriber buffer must be marked overflowed")
	}
	got := 0
	for {
		select {
		case <-ch:
			got++
		default:
			if got == 0 {
				t.Fatal("no events buffered")
			}
			if got > 256 {
				t.Fatalf("buffer delivered %d events, want <=256", got)
			}
			return
		}
	}
}
