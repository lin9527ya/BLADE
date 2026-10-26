package server

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// After the worker-message rework, the human-message path no longer goes through
// ControlWork with an "intervene" action — it runs the intent in a dedicated
// goroutine (runDetachedIntent) and pauses, if needed, with the ordinary "pause"
// action. Guard that ControlWork only knows pause|cancel so a reintroduced
// "intervene" caller fails loudly instead of silently no-op'ing.
func TestControlWorkRejectsRemovedInterveneAction(t *testing.T) {
	e := NewEngine(nil)
	err := e.ControlWork(context.Background(), 1, "intervene")
	if err == nil || !containsUnsupported(err) {
		t.Fatalf("intervene should be unsupported, got %v", err)
	}
}

// A control request for an intent with no live work is a conflict, not a panic or
// a silent success — the same guard the message handler relies on when a run has
// already finished between the UI read and the request.
func TestControlWorkConflictsWhenNoWorkRegistered(t *testing.T) {
	e := NewEngine(nil)
	err := e.ControlWork(context.Background(), 42, "pause")
	if !errors.Is(err, errWorkControlConflict) {
		t.Fatalf("pause with no work = %v, want errWorkControlConflict", err)
	}
}

// validWorkerMessageRequestID gates the idempotency token; keep its charset stable
// so a client-generated UUID is always accepted and injection-y ids are rejected.
func TestValidWorkerMessageRequestID(t *testing.T) {
	ok := []string{"worker-message-abc123", "a.b:c_d-1", "550e8400-e29b-41d4-a716-446655440000"}
	for _, id := range ok {
		if !validWorkerMessageRequestID(id) {
			t.Errorf("id %q should be valid", id)
		}
	}
	bad := []string{"", "has space", "emoji😀", "slash/here", string(make([]byte, 129))}
	for _, id := range bad {
		if validWorkerMessageRequestID(id) {
			t.Errorf("id %q should be rejected", id)
		}
	}
}

func containsUnsupported(err error) bool {
	return err != nil && !errors.Is(err, errWorkControlConflict) &&
		strings.Contains(err.Error(), "unsupported work action")
}

// 人工消息的送达路由：运行中实时注入；暂停与四种中断终态从 transcript 续跑；
// 有结论的收场（done/negative/out-of-scope）与待领取/已删除不放行。
func TestWorkerMessageRoute(t *testing.T) {
	cases := map[string]struct{ steer, resume bool }{
		"running":      {true, false},
		"paused":       {false, true},
		"blocked":      {false, true},
		"exhausted":    {false, true},
		"stopped":      {false, true},
		"no_output":    {false, true},
		"done":         {},
		"negative":     {},
		"out-of-scope": {},
		"open":         {},
		"deleted":      {},
	}
	for state, want := range cases {
		steer, resume := workerMessageRoute(state)
		if steer != want.steer || resume != want.resume {
			t.Errorf("workerMessageRoute(%q) = steer:%v resume:%v, want steer:%v resume:%v",
				state, steer, resume, want.steer, want.resume)
		}
	}
}
