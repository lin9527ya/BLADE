package agent

import (
	"testing"
	"time"
)

func TestCloseHotSessionWaitsForActivePlan(t *testing.T) {
	p := &Planner{}
	p.planMu.Lock()
	done := make(chan struct{})
	go func() { p.CloseHotSession(); close(done) }()
	select {
	case <-done:
		t.Fatal("CloseHotSession did not wait for the active planner lock")
	case <-time.After(30 * time.Millisecond):
	}
	p.planMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("CloseHotSession did not resume after Plan released the lock")
	}
}
