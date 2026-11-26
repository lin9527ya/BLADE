package llmpool

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
)

func withZeroPoolBackoff() func() {
	prevFn := StreamRetryBackoff
	prevJit := StreamRetryJitter
	StreamRetryJitter = false
	StreamRetryBackoff = func(int) time.Duration { return 0 }
	return func() {
		StreamRetryBackoff = prevFn
		StreamRetryJitter = prevJit
	}
}

func TestWithStreamRetryStandaloneProvider(t *testing.T) {
	defer withZeroPoolBackoff()()

	inner := &fakeProv{
		name:   "solo",
		events: [][]llm.StreamEvent{nil, {{Type: llm.SETextDelta, Text: "ok"}}},
		errs:   []error{errors.New("dial tcp: reset"), nil},
	}
	wrapped := WithStreamRetry(inner, StreamPolicy{Attempts: 2})
	if !ProviderHandlesStreamRetry(wrapped) {
		t.Fatal("wrapped provider must report HandlesStreamRetry=true")
	}
	// Wrapping twice must be idempotent.
	if WithStreamRetry(wrapped, StreamPolicy{}) != wrapped {
		t.Fatal("WithStreamRetry should not double-wrap")
	}

	got, err := drain(wrapped.Stream(context.Background(), llm.CompletionRequest{}))
	if err != nil || got != "ok" {
		t.Fatalf("got=%q err=%v, want ok", got, err)
	}
	if inner.calls != 2 {
		t.Fatalf("inner calls=%d, want 2", inner.calls)
	}
}

func TestPoolPerMemberStreamRetryWithoutChainMultiplication(t *testing.T) {
	defer withZeroPoolBackoff()()

	p1 := failProv("primary", 500)
	p2 := okProv("backup", "recovered")
	m1 := member(1, "primary", RankActive, p1)
	m1.ProfileConfigured = true
	m1.Retry = StreamPolicy{Attempts: 1}
	m2 := member(2, "backup", 10, p2)
	m2.ProfileConfigured = true
	m2.Retry = StreamPolicy{Attempts: 2}

	reg := NewRegistry(nil, nil)
	pool := New([]*Member{m1, m2}, reg)
	if !ProviderHandlesStreamRetry(pool) {
		t.Fatal("Pool must advertise HandlesStreamRetry=true")
	}

	got, err := drain(pool.Stream(context.Background(), llm.CompletionRequest{}))
	if err != nil || got != "recovered" {
		t.Fatalf("got=%q err=%v, want recovered", got, err)
	}
	// Primary runs 1 initial + 1 configured stream retry = 2 calls total (NOT swept repeatedly).
	if p1.calls != 2 {
		t.Fatalf("primary calls=%d, want 2", p1.calls)
	}
	if p2.calls != 1 {
		t.Fatalf("backup calls=%d, want 1", p2.calls)
	}
}

func TestHalfOpenFastReTripAndSingleFlightProbe(t *testing.T) {
	reg := NewRegistry(nil, nil)
	reg.SetPolicy(3, 5*time.Minute)
	reg.Restore(1, State{
		Trips:     1,
		OpenUntil: time.Now().Add(-time.Second), // expired -> Half-Open
		LastError: "503",
	})
	st := reg.Get(1)
	if !st.HalfOpen {
		t.Fatal("expired tripped state should report HalfOpen=true")
	}

	// Single-flight probe acquisition: first caller succeeds, concurrent caller yields.
	ho1, acq1 := reg.TryBeginProbe(1)
	if !ho1 || !acq1 {
		t.Fatalf("first TryBeginProbe = (%v, %v), want (true, true)", ho1, acq1)
	}
	if !reg.IsProbing(1) {
		t.Fatal("IsProbing should report true while probe lease is active")
	}
	ho2, acq2 := reg.TryBeginProbe(1)
	if !ho2 || acq2 {
		t.Fatalf("concurrent TryBeginProbe = (%v, %v), want (true, false)", ho2, acq2)
	}

	// A single transient failure during Half-Open must re-trip immediately (skip softTrip=3).
	if !reg.Trip(1, "still 503", false) {
		t.Fatal("Half-Open probe failure must re-trip breaker on failure #1")
	}
	after := reg.Get(1)
	if after.Trips != 2 || !after.Open() || after.HalfOpen {
		t.Fatalf("unexpected state after half-open trip: %+v", after)
	}
}

func TestPoolSingleFlightSkipsActivelyProbedHalfOpenMember(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	slowHalfOpen := slowProbeProv{
		onStart: func() {
			close(started)
			<-release
		},
	}
	backup := okProv("backup", "from-backup")

	reg := NewRegistry(nil, nil)
	reg.Restore(1, State{
		Trips:     1,
		OpenUntil: time.Now().Add(-time.Second),
	})
	pool := New([]*Member{
		member(1, "halfopen", RankActive, slowHalfOpen),
		member(2, "backup", 10, backup),
	}, reg)

	firstDone := make(chan string, 1)
	go func() {
		got, err := drain(pool.Stream(context.Background(), llm.CompletionRequest{}))
		if err != nil {
			t.Errorf("first probe failed: %v", err)
		}
		firstDone <- got
	}()

	<-started // probe #1 holds the Half-Open single-flight lease on member 1
	got2, err2 := drain(pool.Stream(context.Background(), llm.CompletionRequest{}))
	if err2 != nil || got2 != "from-backup" {
		t.Fatalf("concurrent caller got=%q err=%v, want from-backup immediately", got2, err2)
	}
	if backup.calls != 1 {
		t.Fatalf("backup calls=%d, want 1", backup.calls)
	}
	close(release)
	if got1 := <-firstDone; got1 != "probed" {
		t.Fatalf("probe caller got=%q, want probed", got1)
	}
	if st := reg.Get(1); st.Trips != 0 || st.HalfOpen {
		t.Fatalf("successful probe must reset breaker cleanly, got %+v", st)
	}
}

func TestAllTrippedPrefersEarliestSoftCooldownOverHardHead(t *testing.T) {
	head := okProv("head-hard", "head")
	soft := okProv("soft-soon", "soft")

	reg := NewRegistry(nil, nil)
	now := time.Now()
	reg.Restore(1, State{Trips: 3, OpenUntil: now.Add(25 * time.Minute), LastError: "status 402: insufficient_quota"})
	reg.Restore(2, State{Trips: 1, OpenUntil: now.Add(15 * time.Second), LastError: "status 429: rate limit"})

	pool := New([]*Member{member(1, "head", RankActive, head), member(2, "soft", 5, soft)}, reg)
	got, err := drain(pool.Stream(context.Background(), llm.CompletionRequest{}))
	if err != nil || got != "soft" {
		t.Fatalf("got=%q err=%v, want soft member expiring earliest", got, err)
	}
	if head.calls != 0 {
		t.Fatalf("hard-tripped long-cooldown head should not beat soon-to-recover soft member (head.calls=%d)", head.calls)
	}
}

func TestQuota400AndStreamFrameAreHardFailover(t *testing.T) {
	cases := []string{
		"openai: status 400: insufficient_quota",
		"stream error: credit balance is too low",
	}
	for _, msg := range cases {
		err := errors.New(msg)
		if !shouldFailover(context.Background(), err) {
			t.Errorf("%q must trigger failover", msg)
		}
		if !isHardFailure(err) {
			t.Errorf("%q must trigger hard breaker trip", msg)
		}
	}
}

func TestTransportTimeoutFailsOverWhileOuterDeadlineStops(t *testing.T) {
	if !shouldFailover(context.Background(), fmt.Errorf("Post: %w", context.DeadlineExceeded)) {
		t.Fatal("transport DeadlineExceeded while parent ctx is live must fail over")
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if shouldFailover(expired, fmt.Errorf("Post: %w", context.DeadlineExceeded)) {
		t.Fatal("outer expired deadline must not fail over")
	}
}

func TestCrossFormatCompat400FallsOverWithoutTripping(t *testing.T) {
	a := &fakeProv{
		name: "oai-strict",
		errs: []error{errors.New("openai: status 400: unsupported_parameter: thinking")},
	}
	b := okProv("anth", "ok-anth")
	ma := member(1, "oai-strict", RankActive, a)
	ma.Format = "openai"
	mb := member(2, "anth", 10, b)
	mb.Format = "anthropic"

	reg := NewRegistry(nil, nil)
	pool := New([]*Member{ma, mb}, reg)
	got, err := drain(pool.Stream(context.Background(), llm.CompletionRequest{}))
	if err != nil || got != "ok-anth" {
		t.Fatalf("got=%q err=%v, want ok-anth", got, err)
	}
	if reg.Get(1).Fails != 0 || reg.Get(1).Trips != 0 {
		t.Fatalf("cross-format 400 compat skip must not trip original member health: %+v", reg.Get(1))
	}
}

func TestStickyEvictionKeepsRecentAndCounts(t *testing.T) {
	a := okProv("a", "a")
	b := okProv("b", "b")
	pool := New([]*Member{member(1, "a", 5, a), member(2, "b", 5, b)}, NewRegistry(nil, nil))
	for i := range maxStickySessions {
		pool.pinSession(fmt.Sprintf("sess-%d", i), 1)
	}
	// Adding one more evicts ~25%, preserving ~75% including the most recent pins.
	pool.pinSession("sess-new", 2)
	if pool.pinnedMember("sess-new") != 2 {
		t.Fatal("newest pin missing after smooth eviction")
	}
	if pool.pinnedMember(fmt.Sprintf("sess-%d", maxStickySessions-1)) != 1 {
		t.Fatal("recent hot session was evicted instead of oldest sessions")
	}
	if pool.pinnedMember("sess-0") != 0 {
		t.Fatal("oldest session should have been evicted first")
	}
	counts := pool.StickyCounts()
	if counts[1] == 0 || counts[2] != 1 {
		t.Fatalf("unexpected sticky counts: %+v", counts)
	}
	ctx := transcript.WithSessionID(context.Background(), "sess-new")
	if m := pool.CurrentMember(sessionKey(ctx, llm.CompletionRequest{})); m == nil || m.ID != 2 {
		t.Fatalf("CurrentMember did not follow sticky pin 2: %+v", m)
	}
}

type slowProbeProv struct {
	onStart func()
}

func (s slowProbeProv) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if s.onStart != nil {
			s.onStart()
		}
		yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "probed"}, nil)
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func (s slowProbeProv) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if s.onStart != nil {
		s.onStart()
	}
	return llm.Message{Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "probed"}}}, "stop", llm.Usage{}, nil
}

type hintAwareCompleteProv struct {
	calls        int
	failuresEach int
	saw200       bool
	err          error
}

func (p *hintAwareCompleteProv) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.calls++
		h := RetryHintFrom(ctx)
		for range p.failuresEach {
			h.RecordTransportFailure()
		}
		if p.saw200 {
			h.RecordTransportSuccess()
		}
		yield(llm.StreamEvent{}, p.err)
	}
}

func (p *hintAwareCompleteProv) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	h := RetryHintFrom(ctx)
	for range p.failuresEach {
		h.RecordTransportFailure()
	}
	if p.saw200 {
		h.RecordTransportSuccess()
	}
	return llm.Message{}, "", llm.Usage{}, p.err
}

func TestPoolDoesNotMultiplyRetriesAfterSDKConnectExhaustion(t *testing.T) {
	defer withZeroPoolBackoff()()

	p1 := &hintAwareCompleteProv{
		failuresEach: 4, // simulates SDK ConnectAttempts=3 (4 HTTP 429 round trips, no 200 OK)
		err:          errors.New("openai: status 429: rate limit"),
	}
	p2 := okProv("backup", "from-backup")
	m1 := member(1, "primary", RankActive, p1)
	m1.ProfileConfigured = true
	m1.Retry = StreamPolicy{Attempts: 2}
	m2 := member(2, "backup", 10, p2)
	m2.ProfileConfigured = true
	m2.Retry = StreamPolicy{Attempts: 2}

	pool := New([]*Member{m1, m2}, NewRegistry(nil, nil))
	msg, _, _, err := pool.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil || len(msg.Content) == 0 || msg.Content[0].Text != "from-backup" {
		t.Fatalf("expected failover to backup, got msg=%+v err=%v", msg, err)
	}
	if p1.calls != 1 {
		t.Fatalf("primary Complete calls=%d, want 1 (must not multiply Layer 1 x Layer 3)", p1.calls)
	}
	if p2.calls != 1 {
		t.Fatalf("backup calls=%d, want 1", p2.calls)
	}

	// Single-shot transport failure (ConnectAttempts=-1) and mid-stream drop after 200 OK
	// must still run Layer 3 stream retries.
	singleShot := &hintAwareCompleteProv{failuresEach: 1, err: errors.New("openai: status 429: rate limit")}
	_, err = drain(WithStreamRetry(singleShot, StreamPolicy{Attempts: 2}).Stream(context.Background(), llm.CompletionRequest{}))
	if err == nil || singleShot.calls != 3 {
		t.Fatalf("single-shot HTTP failure should still get Layer 3 retries (calls=%d want 3)", singleShot.calls)
	}

	midStreamDrop := &hintAwareCompleteProv{failuresEach: 1, saw200: true, err: errors.New("stream closed unexpectedly")}
	_, err = drain(WithStreamRetry(midStreamDrop, StreamPolicy{Attempts: 2}).Stream(context.Background(), llm.CompletionRequest{}))
	if err == nil || midStreamDrop.calls != 3 {
		t.Fatalf("post-200 mid-stream failure should still get Layer 3 retries (calls=%d want 3)", midStreamDrop.calls)
	}
}
