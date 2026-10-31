package server

import (
	"testing"

	"github.com/lin9527ya/blade/agent"
	pgdb "github.com/lin9527ya/blade/db"
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

// TestTaskStatusRankCoversAllDerivedStatuses 钉住筛选/排序表与后端状态集合一致。
//
// taskStatusRank 是状态集合的又一份抄写（筛选项与排序都按它的顺序）。少一个值的
// 表现是"某个状态在列表里筛不出来、排序时落到 0"，不报错。
func TestTaskStatusRankCoversAllDerivedStatuses(t *testing.T) {
	for _, s := range pgdb.DerivedTaskStatuses {
		if _, ok := taskStatusRank[s]; !ok {
			t.Fatalf("taskStatusRank 缺少状态 %q：筛选与排序都会漏掉它", s)
		}
	}
	if len(taskStatusRank) != len(pgdb.DerivedTaskStatuses) {
		t.Fatalf("taskStatusRank 有 %d 个状态，后端导出 %d 个——两边必须一一对应",
			len(taskStatusRank), len(pgdb.DerivedTaskStatuses))
	}
}
