package server

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveHubSendsInitialSnapshotAndStops(t *testing.T) {
	hub := newLiveHub[int]()
	var calls atomic.Int64
	ch, unsub := hub.subscribe("topic", func() int { return int(calls.Add(1)) })
	select {
	case v := <-ch:
		if v != 1 {
			t.Fatalf("initial snapshot = %d", v)
		}
	case <-time.After(time.Second):
		t.Fatal("no initial snapshot")
	}
	unsub()
	// A second unsubscribe must be a no-op.
	unsub()
}
