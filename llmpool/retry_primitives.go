package llmpool

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxRetryAfterWait = 60 * time.Second

// IsQuotaExhaustedMessage deliberately recognizes only explicit balance,
// billing, credit, or quota-exhaustion signals. Generic 429/rate-limit text,
// authentication failures, network errors, and server failures are excluded.
var (
	nonFailoverHTTPStatus = regexp.MustCompile(`(?:status(?:\s+code)?|http(?:\s+status)?)\s*[=:]?\s*(?:401|403|5\d\d)\b`)
	transientQuotaLimit   = regexp.MustCompile(`(?i)(?:\b(?:rpm|tpm|rpd|qps)\b|quota[_\s-]*metric|rate[_\s-]*limit|too many requests|(?:requests?|tokens?)\s+(?:per|/)\s*(?:second|minute)|(?:per|/)\s*(?:second|minute)\s+(?:requests?|tokens?)|generate[_\s-]*requests[_\s-]*per[_\s-]*(?:minute|second)|tokens?[_\s-]*per[_\s-]*(?:minute|second))`)
)

func IsQuotaExhaustedMessage(message string) bool {
	message = strings.ToLower(message)
	if nonFailoverHTTPStatus.MatchString(message) {
		return false
	}
	if transientQuotaLimit.MatchString(message) {
		return false
	}
	markers := []string{
		"insufficient_quota", "quota_exceeded", "quota exceeded", "quota exhausted",
		"exceeded your current quota", "billing_hard_limit_reached",
		"billing hard limit", "billing_not_active", "credit balance", "insufficient credit",
		"insufficient balance", "balance is too low", "payment required", "status 402",
		"余额不足", "额度不足", "额度已用尽", "欠费",
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return strings.Contains(message, "resource_exhausted") &&
		!strings.Contains(message, "rate limit") &&
		!strings.Contains(message, "too many requests")
}

type retryHintCtxKey struct{}

// RetryHint records upstream Retry-After hints and per-attempt HTTP transport
// observations observed on HTTP 429/503/network responses during a single
// logical model call so outer backoff loops can honor Retry-After delays and
// avoid re-multiplying HTTP-level ConnectAttempts retries.
type RetryHint struct {
	mu             sync.Mutex
	wait           time.Duration
	transportFails int
	saw200OK       bool
}

// WithRetryHint attaches (or reuses an existing) RetryHint on ctx.
func WithRetryHint(ctx context.Context) (context.Context, *RetryHint) {
	if existing := RetryHintFrom(ctx); existing != nil {
		return ctx, existing
	}
	h := &RetryHint{}
	return context.WithValue(ctx, retryHintCtxKey{}, h), h
}

// RetryHintFrom returns the RetryHint attached to ctx, if any.
func RetryHintFrom(ctx context.Context) *RetryHint {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(retryHintCtxKey{}).(*RetryHint)
	return h
}

// BeginAttempt resets the per-attempt transport observation counters before
// issuing a provider call.
func (h *RetryHint) BeginAttempt() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.transportFails = 0
	h.saw200OK = false
	h.mu.Unlock()
}

// RecordTransportFailure notes a failed or >=400 HTTP round trip within the
// current logical provider attempt.
func (h *RetryHint) RecordTransportFailure() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.transportFails++
	h.mu.Unlock()
}

// RecordTransportSuccess notes that an HTTP 200 OK response was received
// during the current logical provider attempt (any later failure happened inside
// body/stream delivery rather than before connection establishment).
func (h *RetryHint) RecordTransportSuccess() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.saw200OK = true
	h.mu.Unlock()
}

// TransportRetriesExhausted reports whether the underlying HTTP transport
// already performed multiple failed round trips without ever reaching 200 OK
// in the current attempt — meaning Layer 1 (SDK ConnectAttempts) already
// retried this failure and Layer 3 (StreamAttempts) must not multiply it.
func (h *RetryHint) TransportRetriesExhausted() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.saw200OK && h.transportFails > 1
}

// Record stores an upstream retry delay, clamped to [0, maxRetryAfterWait].
func (h *RetryHint) Record(d time.Duration) {
	if h == nil || d <= 0 {
		return
	}
	if d > maxRetryAfterWait {
		d = maxRetryAfterWait
	}
	h.mu.Lock()
	if d > h.wait {
		h.wait = d
	}
	h.mu.Unlock()
}

// Take returns and clears the recorded Retry-After wait duration.
func (h *RetryHint) Take() time.Duration {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	d := h.wait
	h.wait = 0
	h.mu.Unlock()
	return d
}

// RetryAfterError wraps an error with an explicit server-suggested wait duration.
type RetryAfterError struct {
	Err  error
	Wait time.Duration
}

func (e RetryAfterError) Error() string {
	if e.Err == nil {
		return "retry after " + e.Wait.String()
	}
	return e.Err.Error()
}

func (e RetryAfterError) Unwrap() error { return e.Err }

// WrapRetryAfter attaches an explicit wait duration to err when wait > 0.
func WrapRetryAfter(err error, wait time.Duration) error {
	if err == nil || wait <= 0 {
		return err
	}
	if wait > maxRetryAfterWait {
		wait = maxRetryAfterWait
	}
	return RetryAfterError{Err: err, Wait: wait}
}

// RetryAfterOf extracts an attached Retry-After duration from err, if any.
func RetryAfterOf(err error) time.Duration {
	var ra RetryAfterError
	if errors.As(err, &ra) && ra.Wait > 0 {
		if ra.Wait > maxRetryAfterWait {
			return maxRetryAfterWait
		}
		return ra.Wait
	}
	return 0
}

// ParseRetryAfter parses an HTTP Retry-After header (integer/float seconds or
// HTTP-date) relative to now and clamps it to [0, maxRetryAfterWait].
func ParseRetryAfter(header string, now time.Time) time.Duration {
	s := strings.TrimSpace(header)
	if s == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		d := time.Duration(secs * float64(time.Second))
		if d > maxRetryAfterWait {
			return maxRetryAfterWait
		}
		return d
	}
	if t, err := http.ParseTime(s); err == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0
		}
		if d > maxRetryAfterWait {
			return maxRetryAfterWait
		}
		return d
	}
	return 0
}

// ApplyEqualJitter applies bounded equal jitter in [base/2, base] so concurrent
// callers recovering from an error don't retry in lockstep.
func ApplyEqualJitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	half := base / 2
	span := base - half
	if span <= 0 {
		return base
	}
	return half + time.Duration(rand.Int64N(int64(span)+1))
}

// EffectiveBackoff combines jittered local backoff with an optional upstream
// Retry-After hint, returning max(ApplyEqualJitter(base), retryAfter + small spread),
// bounded by maxWait (when maxWait > 0).
func EffectiveBackoff(base, retryAfter, maxWait time.Duration) time.Duration {
	d := ApplyEqualJitter(base)
	if retryAfter > 0 {
		capSpread := retryAfter / 10
		if capSpread > 250*time.Millisecond {
			capSpread = 250 * time.Millisecond
		}
		withSpread := retryAfter
		if capSpread > 0 {
			withSpread += time.Duration(rand.Int64N(int64(capSpread) + 1))
		}
		if withSpread > d {
			d = withSpread
		}
	}
	if maxWait > 0 && d > maxWait {
		d = maxWait
	}
	return d
}
