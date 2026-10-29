package agent

import (
	"context"
	"testing"
)

// A triggered run (reporter/retester) attaches TaskID/ExplorationID before
// calling Chat. Chat must keep that attribution and only fill SessionID.
func TestChatPreservesTaskRunInfo(t *testing.T) {
	ctx := WithRunInfo(context.Background(), RunInfo{TaskID: 42, ExplorationID: 7})
	ri := RunInfoFrom(ctx)
	ri.SessionID = "conv-9"
	ctx = WithRunInfo(ctx, ri)
	got := RunInfoFrom(ctx)
	if got.TaskID != 42 || got.ExplorationID != 7 || got.SessionID != "conv-9" {
		t.Fatalf("run info = %+v", got)
	}
}
