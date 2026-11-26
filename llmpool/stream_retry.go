package llmpool

import (
	"context"
	"errors"
	"iter"
	"log"
	"strings"
	"time"

	"github.com/lin9527ya/Norma/llm"
)

// DefaultStreamRetries is the default number of pre-commit replays inside the
// same-provider safety window when neither profile nor global policy overrides it.
const DefaultStreamRetries = 2

// StreamPolicy configures single-member pre-commit safe-window retries
// (mirrors agent.RetryConfig's StreamAttempts/StreamInterval without creating
// a package dependency cycle).
type StreamPolicy struct {
	Attempts int
	Interval time.Duration
}

// StreamRetryJitter controls whether default exponential stream-retry delays use
// equal jitter ([d/2, d]). Exposed for deterministic unit tests.
var StreamRetryJitter = true

// StreamRetryBackoff returns the retry delay for attempt (0.5s, 1s…, capped at
// 4s, with equal jitter by default). Exposed as a var so tests can zero it.
var StreamRetryBackoff = func(attempt int) time.Duration {
	d := min(500*time.Millisecond*(1<<attempt), 4*time.Second)
	if StreamRetryJitter {
		return ApplyEqualJitter(d)
	}
	return d
}

// StreamRetryPolicy resolves effective retry count and per-attempt wait lookup
// (honoring fixed intervals, equal jitter defaults, and Retry-After hints).
func StreamRetryPolicy(r StreamPolicy) (retries int, backoff func(attempt int, hint *RetryHint, err error) time.Duration) {
	retries = DefaultStreamRetries
	if r.Attempts != 0 {
		retries = max(r.Attempts, 0)
	}
	return retries, func(attempt int, hint *RetryHint, err error) time.Duration {
		var ra time.Duration
		if hint != nil {
			ra = hint.Take()
		}
		if era := RetryAfterOf(err); era > ra {
			ra = era
		}
		var base time.Duration
		if r.Interval > 0 {
			base = r.Interval
			if ra > base {
				return EffectiveBackoff(0, ra, 60*time.Second)
			}
			return base
		}
		base = StreamRetryBackoff(attempt)
		if ra > base {
			return EffectiveBackoff(base, ra, 60*time.Second)
		}
		return base
	}
}

// StreamRetryProvider marks providers that already handle per-member pre-commit
// stream retries internally (both Pool and WithStreamRetry implement it).
type StreamRetryProvider interface {
	HandlesStreamRetry() bool
}

// ProviderHandlesStreamRetry reports whether p handles pre-commit stream
// retries itself, preventing outer layers from wrapping another multiplier loop.
func ProviderHandlesStreamRetry(p llm.Provider) bool {
	if p == nil {
		return false
	}
	h, ok := p.(StreamRetryProvider)
	return ok && h.HandlesStreamRetry()
}

type streamRetryProvider struct {
	inner llm.Provider
	retry StreamPolicy
}

// WithStreamRetry wraps a standalone (non-pool) provider with single-member
// pre-commit stream retries matching Pool's per-member behavior. Already-safe
// providers (such as *Pool or an existing wrapper) are returned unchanged.
func WithStreamRetry(prov llm.Provider, retry StreamPolicy) llm.Provider {
	if prov == nil || ProviderHandlesStreamRetry(prov) {
		return prov
	}
	return &streamRetryProvider{inner: prov, retry: retry}
}

func (p *streamRetryProvider) HandlesStreamRetry() bool { return true }

func (p *streamRetryProvider) PreservesThinking() bool {
	if pt, ok := p.inner.(interface{ PreservesThinking() bool }); ok {
		return pt.PreservesThinking()
	}
	return false
}

func (p *streamRetryProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return RunStreamWithRetry(ctx, p.inner, req, p.retry, "stream-retry", nil)
}

func (p *streamRetryProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return RunCompleteWithRetry(ctx, p.inner, req, p.retry, "stream-retry", nil)
}

// StreamEventCommitsOutput reports whether event delivers visible model output
// or a terminal completion/tool-use signal, closing the safe replay window.
func StreamEventCommitsOutput(event llm.StreamEvent) bool {
	switch event.Type {
	case llm.SETextDelta, llm.SEThinkingDelta, llm.SEToolInputJSON:
		return event.Text != ""
	case llm.SEToolUseStart, llm.SEMessageDelta, llm.SEMessageStop:
		return true
	default:
		return false
	}
}

func isQuotaError(err error) bool {
	if err == nil {
		return false
	}
	if statusOf(err) == 402 {
		return true
	}
	return IsQuotaExhaustedMessage(err.Error())
}

// IsRetryableStreamError classifies pre-commit failures eligible for safe
// same-member replay: transient drops, overloaded events, 408/429/5xx.
// Quota exhaustion, over-long prompts (413), and deterministic 4xx rejections
// fail immediately so failover or higher-level handling takes over.
func IsRetryableStreamError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if isQuotaError(err) {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "too long") || strings.Contains(s, "context length") ||
		strings.Contains(s, "context_length") || strings.Contains(s, "maximum context") ||
		strings.Contains(s, "status 413") {
		return false
	}
	for _, code := range []string{"status 400", "status 401", "status 403", "status 404", "status 422"} {
		if strings.Contains(s, code) {
			return false
		}
	}
	return true
}

// RunCompleteWithRetry runs Complete with same-member retry on transient errors.
func RunCompleteWithRetry(ctx context.Context, prov llm.Provider, req llm.CompletionRequest, retry StreamPolicy, tag string, onRetry func(attempt, total int, wait time.Duration, err error)) (llm.Message, string, llm.Usage, error) {
	ctx, hint := WithRetryHint(ctx)
	retries, backoffOf := StreamRetryPolicy(retry)
	for attempt := 0; ; attempt++ {
		_ = hint.Take()
		msg, sr, usage, callErr := prov.Complete(ctx, req)
		if callErr != nil && ctx.Err() == nil && attempt < retries && IsRetryableStreamError(callErr) {
			wait := backoffOf(attempt, hint, callErr)
			if onRetry != nil {
				onRetry(attempt+1, retries, wait, callErr)
			} else {
				log.Printf("[%s] 非流式调用失败，%v 后同节点重试 (%d/%d): %v", tag, wait, attempt+1, retries, callErr)
			}
			if sleepCtx(ctx, wait) {
				if err := ctx.Err(); err != nil {
					return llm.Message{}, "", llm.Usage{}, err
				}
				return llm.Message{}, "", llm.Usage{}, callErr
			}
			continue
		}
		if callErr != nil {
			if ra := hint.Take(); ra > 0 {
				callErr = WrapRetryAfter(callErr, ra)
			}
		}
		return msg, sr, usage, callErr
	}
}

// RunStreamWithRetry runs Stream, buffering non-committing lead events and
// transparently replaying transient pre-commit errors on the same member.
func RunStreamWithRetry(ctx context.Context, prov llm.Provider, req llm.CompletionRequest, retry StreamPolicy, tag string, onRetry func(attempt, total int, wait time.Duration, err error)) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		ctx, hint := WithRetryHint(ctx)
		retries, backoffOf := StreamRetryPolicy(retry)
		for attempt := 0; ; attempt++ {
			_ = hint.Take()
			committed := false
			var pending []llm.StreamEvent
			var streamErr error
			for ev, err := range prov.Stream(ctx, req) {
				if err != nil {
					streamErr = err
					break
				}
				if !committed && !StreamEventCommitsOutput(ev) {
					pending = append(pending, ev)
					continue
				}
				if !committed {
					for _, b := range pending {
						if !yield(b, nil) {
							return
						}
					}
					pending = nil
					committed = true
				}
				if !yield(ev, nil) {
					return
				}
			}
			if streamErr != nil && !committed && ctx.Err() == nil && attempt < retries && IsRetryableStreamError(streamErr) {
				wait := backoffOf(attempt, hint, streamErr)
				if onRetry != nil {
					onRetry(attempt+1, retries, wait, streamErr)
				} else {
					log.Printf("[%s] 提交前流失败，%v 后同节点重试 (%d/%d): %v", tag, wait, attempt+1, retries, streamErr)
				}
				if sleepCtx(ctx, wait) {
					if cerr := ctx.Err(); cerr != nil {
						yield(llm.StreamEvent{}, cerr)
					} else {
						yield(llm.StreamEvent{}, streamErr)
					}
					return
				}
				continue
			}
			if streamErr == nil {
				for _, b := range pending {
					if !yield(b, nil) {
						return
					}
				}
				return
			}
			for _, b := range pending {
				if !yield(b, nil) {
					return
				}
			}
			if ra := hint.Take(); ra > 0 {
				streamErr = WrapRetryAfter(streamErr, ra)
			}
			yield(llm.StreamEvent{}, streamErr)
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() != nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}
