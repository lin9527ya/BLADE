package agent

import (
	"testing"

	"github.com/lin9527ya/Norma/llm"
)

func TestWorkerSessionIDIsStablePerIntent(t *testing.T) {
	if got := WorkerSessionID(12, 34); got != "exp12-worker-i34" {
		t.Fatalf("session id = %q, want exp12-worker-i34", got)
	}
}

func TestWorkerChatMarkerOnlyMatchesItsNormalUserTurn(t *testing.T) {
	requestID := "worker-message-123"
	messages := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(workerChatMarker(requestID))}},
		llm.UserText(workerChatMarker("worker-message-other") + "\nother"),
		llm.UserText(workerChatMarker(requestID) + "\nnew intent"),
	}
	if !hasWorkerChatMessage(messages, requestID) {
		t.Fatal("expected the matching user turn to be detected")
	}
	if hasWorkerChatMessage(messages, "worker-message-missing") {
		t.Fatal("unrelated request id matched a Worker user turn")
	}
}

func TestWorkerSessionIDForSlotIsStableAcrossIntents(t *testing.T) {
	if got := WorkerSessionIDForSlot(12, 3); got != "exp12-worker-slot3" {
		t.Fatalf("slot session id = %q, want exp12-worker-slot3", got)
	}
	// The id must not depend on the intent: that is the whole point of a slot.
	a, b := WorkerSessionIDForSlot(12, 3), WorkerSessionIDForSlot(12, 3)
	if a != b {
		t.Fatal("slot session id must be deterministic")
	}
	if WorkerSessionIDForSlot(12, 3) == WorkerSessionID(12, 34) {
		t.Fatal("slot session must not collide with a legacy per-intent session")
	}
	if workerSlotNumber("work#3") != 3 || workerSlotNumber("work#x") != 0 || workerSlotNumber("chat") != 0 {
		t.Fatal("slot number parsing drifted")
	}
}

func TestWorkerIntentMarkerDedup(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText(workerIntentMarker(7) + "\nold"),
		llm.UserText(workerIntentMarker(8) + "\ncurrent"),
	}
	if hasWorkerIntentMessage(msgs, 7) != true || hasWorkerIntentMessage(msgs, 9) {
		t.Fatal("intent marker detection drifted")
	}
	if got := lastWorkerIntentMessageIndex(msgs); got != 1 {
		t.Fatalf("last marker index = %d, want 1", got)
	}
}
