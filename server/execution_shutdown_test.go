package server

import (
	"context"
	"testing"
	"time"

	"github.com/lin9527ya/blade/agent"
)

// A fired time.Timer channel only yields once. Two SIGTERM-ignoring children
// must still be hard-killed and Shutdown must return within the shared grace.
func TestExecutionShutdownWithMultipleStubbornRuns(t *testing.T) {
	svc, _, _, task := newExecFixture(t)
	for i := 0; i < 2; i++ {
		if _, err := svc.Submit(context.Background(), task, 0, "work#1", `trap '' TERM; sleep 30`, "stubborn shutdown fixture", 0); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	start := time.Now()
	done := make(chan int, 1)
	go func() { done <- svc.Shutdown(agent.AbortShutdown.Text, 100*time.Millisecond) }()
	select {
	case n := <-done:
		if n != 2 {
			t.Fatalf("Shutdown returned %d", n)
		}
		if elapsed := time.Since(start); elapsed > execCancelGrace+3*time.Second {
			t.Fatalf("Shutdown blocked too long: %s", elapsed)
		}
	case <-time.After(execCancelGrace + 5*time.Second):
		t.Fatal("Shutdown blocked on a reused timer with >=2 stubborn runs")
	}
}
