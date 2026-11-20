package agent

import (
	"context"
	"testing"
	"time"
)

func TestCompactorWaitIsBroadcastAndContextBounded(t *testing.T) {
	c := NewCompactor(nil, "")
	c.mu.Lock()
	c.active = 1
	c.idle = make(chan struct{})
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if c.Wait(ctx) {
		t.Fatal("Wait returned while a compaction was still active")
	}
	c.mu.Lock()
	c.active = 0
	close(c.idle)
	c.mu.Unlock()
	if !c.Wait(context.Background()) {
		t.Fatal("Wait did not observe the closed idle broadcast")
	}
}
