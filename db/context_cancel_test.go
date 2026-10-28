package db

import (
	"context"
	"errors"
	"testing"
)

func TestHotReadsHonorCanceledContext(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	checks := []struct {
		name string
		run  func() error
	}{
		{"dashboard tokens", func() error { _, err := d.DashboardTaskTokensContext(ctx); return err }},
		{"task metrics", func() error { _, err := d.TaskListMetricsForContext(ctx, []int64{1}); return err }},
		{"archive blockers", func() error { _, err := d.TaskArchiveBlockersForContext(ctx, []int64{1}); return err }},
		{"last activity", func() error { _, err := d.LastActivityAllContext(ctx); return err }},
		{"intent states", func() error { _, err := d.IntentStateCountsForContext(ctx, []int64{1}); return err }},
		{"claimable intents", func() error { _, err := d.ClaimableIntentCountsForContext(ctx, []int64{1}); return err }},
		{"conversation page", func() error { _, err := d.ListConversationsPageContext(ctx, "", 0, 10); return err }},
		{"conversation tail", func() error { _, _, _, err := d.ConvActivityTailPageContext(ctx, 1, 0, 10); return err }},
	}
	for _, check := range checks {
		if err := check.run(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: err=%v, want context.Canceled", check.name, err)
		}
	}
}
