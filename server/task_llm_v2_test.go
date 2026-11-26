package server

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/llmpool"
)

func withZeroAllRetryBackoff() func() {
	restoreServer := withZeroRetryBackoff()
	prevFn := llmpool.StreamRetryBackoff
	prevJit := llmpool.StreamRetryJitter
	llmpool.StreamRetryJitter = false
	llmpool.StreamRetryBackoff = func(int) time.Duration { return 0 }
	return func() {
		restoreServer()
		llmpool.StreamRetryBackoff = prevFn
		llmpool.StreamRetryJitter = prevJit
	}
}

func TestStreamTaskLLMDoesNotMultiplyRetriesOverPool(t *testing.T) {
	defer withZeroAllRetryBackoff()()

	p1 := &scriptedLLMProvider{err: errors.New("anthropic: status 500: boom")}
	p2 := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "ok"}}}
	m1 := &llmpool.Member{ID: 1, Name: "p1", Model: "m1", Rank: llmpool.RankActive, ProfileConfigured: true, Retry: llmpool.StreamPolicy{Attempts: 1}, Prov: p1}
	m2 := &llmpool.Member{ID: 2, Name: "p2", Model: "m2", Rank: 10, ProfileConfigured: true, Retry: llmpool.StreamPolicy{Attempts: 1}, Prov: p2}
	pool := llmpool.New([]*llmpool.Member{m1, m2}, llmpool.NewRegistry(nil, nil))

	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{provider: pool, retry: agent.RetryConfig{StreamAttempts: 2}}, nil
		},
	}
	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(events) != 1 || events[0].Text != "ok" {
		t.Fatalf("unexpected events: %+v", events)
	}
	// p1 should be tried 1 initial + 1 member retry = 2 times (NOT swept again by outer hooks).
	if p1.calls != 2 {
		t.Fatalf("p1 calls=%d, want 2", p1.calls)
	}
	if p2.calls != 1 {
		t.Fatalf("p2 calls=%d, want 1", p2.calls)
	}
}

func TestExplicitChainSharesHealthAndBypassesTransientFailureWithoutExhaustingCursor(t *testing.T) {
	defer withZeroAllRetryBackoff()()

	reg := llmpool.NewRegistry(nil, nil)
	p1 := &scriptedLLMProvider{err: errors.New("openai: status 503: temporary overload")}
	p2 := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "from-backup"}}}
	exhaustCalls := 0
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 101, revision: 1, provider: p1}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			exhaustCalls++
			return db.TaskLLMTransition{}, nil
		},
		health: reg,
		chainCandidates: func(taskLLMSelection) ([]taskChainCandidate, error) {
			return []taskChainCandidate{
				{profileID: 101, provider: p1, cfg: agent.Config{Format: llm.FormatOpenAI, Stream: true, Retry: agent.RetryConfig{StreamAttempts: -1}}},
				{profileID: 202, provider: p2, cfg: agent.Config{Format: llm.FormatOpenAI, Stream: true, Retry: agent.RetryConfig{StreamAttempts: -1}}},
			}, nil
		},
	}
	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil {
		t.Fatalf("explicit chain failed to temporarily bypass 503 member: %v", err)
	}
	if len(events) != 1 || events[0].Text != "from-backup" {
		t.Fatalf("events=%+v, want from-backup", events)
	}
	if exhaustCalls != 0 {
		t.Fatalf("transient 503 must NOT permanently exhaust DB cursor (exhaustCalls=%d)", exhaustCalls)
	}
	if st := reg.Get(101); st.Fails != 1 {
		t.Fatalf("expected shared registry to record failure on 101, got %+v", st)
	}
}

func TestExplicitChainExhaustsOnlyQuotaErrorsAndPassesHealth(t *testing.T) {
	defer withZeroAllRetryBackoff()()

	reg := llmpool.NewRegistry(nil, nil)
	p1 := &scriptedLLMProvider{err: errors.New("openai: status 402: insufficient_quota")}
	p2 := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "p2-ok"}}}
	active := int64(101)
	var exhaustedIDs []int64
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: active, revision: 1, provider: p1}, nil
		},
		exhaust: func(sel taskLLMSelection, _ error) (db.TaskLLMTransition, error) {
			exhaustedIDs = append(exhaustedIDs, sel.profileID)
			active = 202
			next := active
			return db.TaskLLMTransition{PreviousProfileID: sel.profileID, NextProfileID: &next, Advanced: true}, nil
		},
		health: reg,
		chainCandidates: func(taskLLMSelection) ([]taskChainCandidate, error) {
			return []taskChainCandidate{
				{profileID: 101, provider: p1, cfg: agent.Config{Format: llm.FormatOpenAI, Stream: true, Retry: agent.RetryConfig{StreamAttempts: -1}}},
				{profileID: 202, provider: p2, cfg: agent.Config{Format: llm.FormatOpenAI, Stream: true, Retry: agent.RetryConfig{StreamAttempts: -1}}},
			}, nil
		},
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(events) != 1 || events[0].Text != "p2-ok" {
		t.Fatalf("events=%+v, want p2-ok", events)
	}
	if len(exhaustedIDs) != 1 || exhaustedIDs[0] != 101 {
		t.Fatalf("expected profile 101 marked quota exhausted, got %v", exhaustedIDs)
	}
	if !reg.IsOpen(101) {
		t.Fatal("quota failure should also open shared health breaker for profile 101")
	}
	if st2 := reg.Get(202); st2.Fails != 0 || st2.Trips != 0 {
		t.Fatalf("healthy fallback 202 must remain clean, got %+v", st2)
	}
}

func TestChatAgentUsesStickySessionPoolMemberOptions(t *testing.T) {
	reg := llmpool.NewRegistry(nil, nil)
	var seenMu sync.Mutex
	seenReqs := map[int]llm.CompletionRequest{}

	makeProv := func(idx int, reply string) llm.Provider {
		return scriptedCompleteProv{fn: func(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
			seenMu.Lock()
			seenReqs[idx] = req
			seenMu.Unlock()
			return llm.Message{Content: []llm.ContentBlock{{Type: llm.BlockText, Text: reply}}}, "stop", llm.Usage{}, nil
		}}
	}
	m1 := &llmpool.Member{ID: 1, Name: "m1", Model: "gpt-a", Format: "openai", Rank: 10, ProfileConfigured: true, NonStreaming: true, MaxTokens: 1111, PromptCacheRetention: "in_memory", WindowTokens: 100_000, Prov: makeProv(1, "from-1")}
	m2 := &llmpool.Member{ID: 2, Name: "m2", Model: "gpt-b", Format: "openai", Rank: 10, ProfileConfigured: true, NonStreaming: true, MaxTokens: 2222, PromptCacheRetention: "24h", WindowTokens: 200_000, Prov: makeProv(2, "from-2")}
	pool := llmpool.New([]*llmpool.Member{m1, m2}, reg)

	// Warm sticky affinity for session "conv-sticky" onto member #2 specifically.
	// Find a session id that pins member #2 on first call or trip 1 temporarily.
	reg.Trip(1, "temp", true)
	_, _, _, err := pool.Complete(transcript.WithSessionID(context.Background(), "conv-sticky"), llm.CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	reg.Reset(1)

	s := &Server{m: &Manager{dir: t.TempDir()}}
	ca := s.newConfiguredChatAgent(pool, agent.Config{Format: llm.FormatOpenAI, Model: "gpt-a", MaxTokens: 1111, Stream: false})
	ca.SetNoaEnabled(nil) // nil pg in unit test without Postgres
	if cur := pool.CurrentMember("conv-sticky"); cur == nil || cur.ID != 2 {
		t.Fatalf("expected sticky member 2, got %+v", cur)
	}
	out, err := ca.Chat(context.Background(), "assistant", "conv-sticky", "hello", 1, 0, false, nil)
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if out != "from-2" {
		t.Fatalf("expected sticky member 2 reply, got %q", out)
	}
	seenMu.Lock()
	req2 := seenReqs[2]
	seenMu.Unlock()
	if req2.MaxTokens != 2222 || req2.PromptCacheRetention != "24h" {
		t.Fatalf("ChatAgent did not follow sticky member #2 runtime options: %+v", req2)
	}
}

func TestRetryableWorkerModelErrorSkipsDeterministicFailures(t *testing.T) {
	if retryableWorkerModelError(harness.ReasonModelError, errors.New("openai: status 401: bad key")) {
		t.Fatal("401 should not trigger Worker Intent retry")
	}
	if retryableWorkerModelError(harness.ReasonModelError, llmpool.ErrExhausted) {
		// Plain ErrExhausted without deterministic marker could be allowed; over-window must skip:
	}
	if retryableWorkerModelError(harness.ReasonModelError, errors.Join(llmpool.ErrExhausted, errors.New("请求估算约 900000 tokens，超过所有配置的上下文窗口上限"))) {
		t.Fatal("over-window ErrExhausted must not trigger Worker Intent retry")
	}
	if retryableWorkerModelError(harness.ReasonModelError, errors.Join(llmpool.ErrExhausted, errors.New("anthropic: status 402: insufficient_quota"))) {
		t.Fatal("hard 402 ErrExhausted must not trigger Worker Intent retry")
	}
	if !retryableWorkerModelError(harness.ReasonModelError, errors.Join(llmpool.ErrExhausted, errors.New("anthropic: status 429: overloaded"))) {
		t.Fatal("transient 429 ErrExhausted should still allow Worker Intent retry")
	}
}

type scriptedCompleteProv struct {
	fn func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error)
}

func (s scriptedCompleteProv) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {}
}

func (s scriptedCompleteProv) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return s.fn(ctx, req)
}
