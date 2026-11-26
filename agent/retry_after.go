package agent

import (
	"context"
	"time"

	"github.com/lin9527ya/blade/llmpool"
)

type RetryHint = llmpool.RetryHint
type RetryAfterError = llmpool.RetryAfterError

const maxRetryAfterWait = 60 * time.Second

func WithRetryHint(ctx context.Context) (context.Context, *RetryHint) {
	return llmpool.WithRetryHint(ctx)
}

func RetryHintFrom(ctx context.Context) *RetryHint {
	return llmpool.RetryHintFrom(ctx)
}

func WrapRetryAfter(err error, wait time.Duration) error {
	return llmpool.WrapRetryAfter(err, wait)
}

func RetryAfterOf(err error) time.Duration {
	return llmpool.RetryAfterOf(err)
}

func ParseRetryAfter(header string, now time.Time) time.Duration {
	return llmpool.ParseRetryAfter(header, now)
}

func ApplyEqualJitter(base time.Duration) time.Duration {
	return llmpool.ApplyEqualJitter(base)
}

func EffectiveBackoff(base, retryAfter, maxWait time.Duration) time.Duration {
	return llmpool.EffectiveBackoff(base, retryAfter, maxWait)
}
