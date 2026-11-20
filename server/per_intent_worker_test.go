package server

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

func TestPerIntentClaimAssignsIndependentWorkerAndSession(t *testing.T) {
	e, task := goalDrainFixture(t)
	i1, err := task.Store.AddIntent(map[string]any{"summary": "first intent"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	i2, err := task.Store.AddIntent(map[string]any{"summary": "second intent"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	c1 := e.claimNextIntent(task)
	c2 := e.claimNextIntent(task)
	if c1 == nil || c2 == nil || c1.ID == c2.ID {
		t.Fatalf("expected two distinct claimed intents, got c1=%+v c2=%+v", c1, c2)
	}
	for _, c := range []*db.Node{c1, c2} {
		wantOwner := fmt.Sprintf("worker#%d", c.ID)
		if c.Owner != wantOwner {
			t.Fatalf("intent %d owner = %q, want %q", c.ID, c.Owner, wantOwner)
		}
		if slotNumber(c.Owner) != 0 {
			t.Fatalf("intent %d should not bind to legacy work# slot: %q", c.ID, c.Owner)
		}
		wantSess := agent.WorkerSessionID(task.ExpID, c.ID)
		if c.LogicalSessionID != wantSess {
			t.Fatalf("intent %d logical_session_id = %q, want %q", c.ID, c.LogicalSessionID, wantSess)
		}
	}
	if i1 == i2 {
		t.Fatal("duplicate intent IDs")
	}
}

func TestRunDetachedIntentSucceedsConcurrentlyAcrossIntents(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := m.SetWorkers(1); err != nil {
		t.Fatal(err)
	}
	sCtx, sCancel := context.WithCancel(context.Background())
	t.Cleanup(sCancel)
	s := &Server{ctx: sCtx, m: m, engine: NewEngine(m)}
	task, err := m.CreateTask(fmt.Sprintf("detached-concurrent-%d", time.Now().UnixNano()), "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })

	i1, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "paused 1"}, 5, "paused", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	i2, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "paused 2"}, 5, "paused", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	var active atomic.Int32
	bothRunning := make(chan struct{}, 1)
	worker := agent.NewWorker(retestProvider{complete: func(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		if active.Add(1) >= 2 {
			select {
			case bothRunning <- struct{}{}:
			default:
			}
		}
		defer active.Add(-1)
		select {
		case <-release:
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("resumed")}}, "end_turn", llm.Usage{}, nil
		case <-ctx.Done():
			return llm.Message{}, "", llm.Usage{}, ctx.Err()
		}
	}}, "test", m.dir, nil, 10000, 1)
	worker.SetNonStreaming(func() bool { return true })
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return nil, worker })

	ctx, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() { cancel(agent.AbortShutdown) })

	if err := s.engine.runDetachedIntent(ctx, task, i1, "paused", "req-1", "hi 1", "hi 1"); err != nil {
		t.Fatalf("first detached intent failed: %v", err)
	}
	if err := s.engine.runDetachedIntent(ctx, task, i2, "paused", "req-2", "hi 2", "hi 2"); err != nil {
		t.Fatalf("second detached intent must not be blocked by slot lock: %v", err)
	}

	select {
	case <-bothRunning:
	case <-time.After(10 * time.Second):
		t.Fatal("detached independent workers did not run concurrently")
	}
	close(release)

	drain, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := s.waitTaskQuiescent(drain, task.ID); err != nil {
		t.Fatalf("wait quiescent: %v", err)
	}
	for _, iid := range []int64{i1, i2} {
		acts, _, err := task.Store.ActivityPage(db.ActivitySessionFilter{NodeID: &iid}, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range acts {
			if a.SlotNo != nil {
				t.Fatalf("intent %d activity unexpectedly set slot_no=%d: %+v", iid, *a.SlotNo, a)
			}
		}
	}
}

func TestWorkerDispatcherHonorsConcurrencyAndSpawnsIndependentWorkers(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	sCtx, sCancel := context.WithCancel(context.Background())
	t.Cleanup(sCancel)
	s := &Server{ctx: sCtx, m: m, engine: NewEngine(m)}

	task, err := m.CreateTaskWithOptions(fmt.Sprintf("dispatch-cap-%d", time.Now().UnixNano()), "goal", db.TaskCreateOptions{
		WorkerCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.engine.StopTask(task.ID)
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
	})

	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := task.Store.AddIntent(map[string]any{"summary": fmt.Sprintf("intent-%d", i+1)}, 5, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	var cur, maxActive atomic.Int32
	gate := make(chan struct{})
	seenReq := make(chan string, 10)
	worker := agent.NewWorker(retestProvider{complete: func(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		n := cur.Add(1)
		for {
			prev := maxActive.Load()
			if n <= prev || maxActive.CompareAndSwap(prev, n) {
				break
			}
		}
		var combined strings.Builder
		for _, msg := range req.Messages {
			combined.WriteString(msg.Text())
			combined.WriteByte('\n')
		}
		seenReq <- combined.String()
		defer cur.Add(-1)
		select {
		case <-gate:
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}}, "end_turn", llm.Usage{}, nil
		case <-ctx.Done():
			return llm.Message{}, "", llm.Usage{}, ctx.Err()
		}
	}}, "test", m.dir, nil, 10000, 1)
	worker.SetNonStreaming(func() bool { return true })
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return nil, worker })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.engine.Run(ctx, task)

	// First wave: concurrency cap 2 -> 2 workers start concurrently.
	for i := 0; i < 2; i++ {
		select {
		case txt := <-seenReq:
			if !strings.Contains(txt, "独立 Worker #") {
				t.Fatalf("expected independent worker turn text, got: %s", txt)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for worker %d/2", i+1)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if got := maxActive.Load(); got != 2 {
		t.Fatalf("max concurrent workers = %d, want 2", got)
	}

	// Unblock so the 3rd queued intent is picked up by a fresh independent worker.
	close(gate)
	select {
	case txt := <-seenReq:
		if !strings.Contains(txt, "独立 Worker #") {
			t.Fatalf("expected independent worker turn text for 3rd intent, got: %s", txt)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for 3rd intent after concurrency freed")
	}

	// Wait for all 3 independent workers to settle before tearing down.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		allSettled := true
		for _, id := range ids {
			n, err := task.Store.GetNode(id)
			if err != nil || n == nil || n.State == "open" || n.State == "running" {
				allSettled = false
				break
			}
		}
		if allSettled {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.engine.StopTask(task.ID)
	for _, id := range ids {
		n, err := task.Store.GetNode(id)
		if err != nil || n == nil {
			t.Fatalf("get intent %d: %v", id, err)
		}
		if want := fmt.Sprintf("worker#%d", id); n.Owner != want {
			t.Fatalf("intent %d owner = %q, want %q", id, n.Owner, want)
		}
	}
}
