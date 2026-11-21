package server

import (
	"github.com/lin9527ya/blade/db"
	"testing"
)

func TestTimeoutDispatchRetrySkipsAcknowledgedTargets(t *testing.T) {
	e, task := goalDrainFixture(t)
	s := &Server{m: e.m, engine: e, ctx: t.Context()}
	sc := &Scheduler{s: s, pg: e.m.pg}
	if _, err := e.m.pg.Exec(`UPDATE tasks SET status='timeout' WHERE id=$1`, tidOfTask(task)); err != nil {
		t.Fatal(err)
	}
	events, err := sc.pg.ClaimTaskTimeoutEvents(t.Context(), 10, tidOfTask(task))
	if err != nil || len(events) != 1 {
		t.Fatal(err)
	}
	want := []*db.AgentTrigger{{ID: 1, OnTaskTimeout: true}, {ID: 2, OnTaskTimeout: true}}
	counts := map[int64]int{}
	ready := false
	sc.enqueueTimeout = func(tr *db.AgentTrigger, _ db.TaskTimeoutEvent, _ string) bool {
		counts[tr.ID]++
		return tr.ID == 1 || ready
	}
	sc.dispatchTaskTimeoutEvent(t.Context(), events[0], want)
	retry, err := sc.pg.ClaimTaskTimeoutEvents(t.Context(), 10, tidOfTask(task))
	if err != nil || len(retry) != 1 {
		t.Fatal("unready target was discarded")
	}
	ready = true
	sc.dispatchTaskTimeoutEvent(t.Context(), retry[0], want)
	if counts[1] != 1 || counts[2] != 2 {
		t.Fatalf("partial retry duplicated successful target: %v", counts)
	}
	if evs, err := sc.pg.ClaimTaskTimeoutEvents(t.Context(), 10, tidOfTask(task)); err != nil || len(evs) != 0 {
		t.Fatal("consumed event redelivered")
	}
}
