package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/llm"
)

func TestWriteIntentRejectsStaleIntent(t *testing.T) {
	ts := &ToolSet{ownerNode: 11, ownerIsIntent: true}
	if id, err := ts.writeIntent(nil); err != nil || id != 11 {
		t.Fatalf("empty intent = %d,%v; want current 11", id, err)
	}
	if id, err := ts.writeIntent(json.RawMessage(`11`)); err != nil || id != 11 {
		t.Fatalf("matching intent = %d,%v", id, err)
	}
	if _, err := ts.writeIntent(json.RawMessage(`10`)); err == nil || !strings.Contains(err.Error(), "当前运行的意图") {
		t.Fatalf("stale intent must be rejected, got %v", err)
	}
	// Planner's owner is the origin fact: it keeps the old freedom to target an intent.
	planner := &ToolSet{ownerNode: 1}
	if id, err := planner.writeIntent(json.RawMessage(`42`)); err != nil || id != 42 {
		t.Fatalf("planner intent = %d,%v", id, err)
	}
}

func TestApprovalHistoryStartsAtCurrentIntent(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText(workerIntentMarker(1) + "\nold intent"),
		llm.UserText("old action"),
		llm.UserText(workerIntentMarker(2) + "\ncurrent intent"),
		llm.UserText("current action"),
	}
	entries := approvalHistory(msgs)
	if len(entries) != 2 || !strings.Contains(entries[0].Text, "current intent") || entries[1].Text != "current action" {
		t.Fatalf("approval history = %+v", entries)
	}
	plain := approvalHistory([]llm.Message{llm.UserText("no marker")})
	if len(plain) != 1 {
		t.Fatalf("non-worker history must be untouched: %+v", plain)
	}
}

// The launch turn is overview + intent block, so the marker is NOT at the start.
// Prefix matching here silently kept the whole slot history in approval context.
func TestApprovalHistoryMarkerAfterOverview(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText(workerIntentMarker(1) + "\nold intent"),
		llm.UserText("old action"),
		llm.UserText("【全局态势】…\n\n" + workerIntentMarker(2) + "\ncurrent intent"),
		llm.UserText("current action"),
	}
	entries := approvalHistory(msgs)
	if len(entries) != 2 || !strings.Contains(entries[0].Text, "current intent") || entries[1].Text != "current action" {
		t.Fatalf("approval history = %+v", entries)
	}
}
