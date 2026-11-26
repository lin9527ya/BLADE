package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	if got := ParseRetryAfter("", now); got != 0 {
		t.Fatalf("empty = %v, want 0", got)
	}
	if got := ParseRetryAfter("5", now); got != 5*time.Second {
		t.Fatalf("5s = %v, want 5s", got)
	}
	if got := ParseRetryAfter("120", now); got != maxRetryAfterWait {
		t.Fatalf("clamped = %v, want %v", got, maxRetryAfterWait)
	}
	future := now.Add(15 * time.Second).UTC().Format(http.TimeFormat)
	if got := ParseRetryAfter(future, now); got < 14*time.Second || got > 16*time.Second {
		t.Fatalf("http-date = %v, want ~15s", got)
	}
	past := now.Add(-5 * time.Second).UTC().Format(http.TimeFormat)
	if got := ParseRetryAfter(past, now); got != 0 {
		t.Fatalf("past http-date = %v, want 0", got)
	}
}

func TestApplyEqualJitterAndEffectiveBackoff(t *testing.T) {
	t.Parallel()
	base := 4 * time.Second
	for range 50 {
		d := ApplyEqualJitter(base)
		if d < base/2 || d > base {
			t.Fatalf("ApplyEqualJitter(%v) = %v, want [%v, %v]", base, d, base/2, base)
		}
	}
	eff := EffectiveBackoff(500*time.Millisecond, 5*time.Second, 30*time.Second)
	if eff < 5*time.Second || eff > 6*time.Second {
		t.Fatalf("EffectiveBackoff with Retry-After=5s gave %v, want [5s, 6s]", eff)
	}
	capped := EffectiveBackoff(500*time.Millisecond, 45*time.Second, 10*time.Second)
	if capped != 10*time.Second {
		t.Fatalf("capped = %v, want 10s", capped)
	}
	wrapped := WrapRetryAfter(errors.New("boom"), 7*time.Second)
	if got := RetryAfterOf(wrapped); got != 7*time.Second {
		t.Fatalf("RetryAfterOf = %v, want 7s", got)
	}
}

func TestQuotaAwareTransportRecordsRetryAfterExceptQuota402(t *testing.T) {
	t.Parallel()

	ctx, hint := WithRetryHint(context.Background())
	rt := quotaAwareTransport{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Retry-After", "4")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(`{"message":"rate limit"}`)),
		}, nil
	})}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := hint.Take(); got != 4*time.Second {
		t.Fatalf("recorded Retry-After = %v, want 4s", got)
	}

	// 429 rewritten to 402 quota exhaustion must NOT populate RetryHint.
	rtQuota := quotaAwareTransport{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Retry-After", "10")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(`{"code":"insufficient_quota"}`)),
		}, nil
	})}
	req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid", nil)
	resp2, err := rtQuota.RoundTrip(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", resp2.StatusCode)
	}
	if got := hint.Take(); got != 0 {
		t.Fatalf("quota exhaustion recorded Retry-After = %v, want 0", got)
	}

	// 503 with Retry-After should record hint too.
	rt503 := quotaAwareTransport{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Retry-After", "6")
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(`overloaded`)),
		}, nil
	})}
	req3, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid", nil)
	resp3, err := rt503.RoundTrip(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if got := hint.Take(); got != 6*time.Second {
		t.Fatalf("503 Retry-After = %v, want 6s", got)
	}
}

func TestQuotaAwareTransportTracksConnectRetries(t *testing.T) {
	t.Parallel()

	ctx, hint := WithRetryHint(context.Background())
	// 1 transport failure alone (e.g. ConnectAttempts=-1) must NOT suppress Layer 3 retry.
	hint.BeginAttempt()
	rt429 := quotaAwareTransport{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"message":"rate limit"}`)),
		}, nil
	})}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid", nil)
	resp, err := rt429.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hint.TransportRetriesExhausted() {
		t.Fatal("single failed transport round trip must not mark ConnectAttempts exhausted")
	}

	// >=2 failed round trips without 200 OK means SDK ConnectAttempts already retried.
	resp, err = rt429.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !hint.TransportRetriesExhausted() {
		t.Fatal("multiple failed round trips without 200 OK should mark TransportRetriesExhausted=true")
	}

	// If 200 OK was reached (e.g. attempt 1 got 429, attempt 2 got 200 OK and stream dropped),
	// Layer 3 stream retry must stay enabled.
	hint.BeginAttempt()
	_, _ = rt429.RoundTrip(req)
	resp, _ = rt429.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	rtOK := quotaAwareTransport{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}
	okResp, err := rtOK.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	okResp.Body.Close()
	if hint.TransportRetriesExhausted() {
		t.Fatal("200 OK followed by mid-stream drop must allow Layer 3 stream retry")
	}
}
