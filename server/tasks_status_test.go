package server

import (
	"testing"

	"github.com/lin9527ya/blade/agent"
)

// Provider resolution is expensive; an unstarted task can never be running, so
// status calculation must not resolve its planner/worker pair.
func TestResolvedTaskStatusSkipsProviderForUnstartedTask(t *testing.T) {
	engine := &Engine{}
	called := 0
	engine.resolve = func(*Task) (*agent.Planner, *agent.Worker) {
		called++
		return nil, nil
	}
	s := &Server{engine: engine}
	if got := s.resolvedTaskStatus(&Task{ID: "not-started"}); got != "created" {
		t.Fatalf("status=%q, want created", got)
	}
	if called != 0 {
		t.Fatalf("provider resolved %d times for an unstarted task", called)
	}
	engine.started.Store("started", true)
	_ = s.resolvedTaskStatus(&Task{ID: "started"})
	if called != 1 {
		t.Fatalf("started task resolve calls=%d, want 1", called)
	}
}
