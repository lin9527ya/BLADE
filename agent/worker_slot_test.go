package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/db"
)

// TestWorkerSlotReusesTranscriptAcrossIntents is the core regression for the
// fixed-slot design: two intents on the same work#1 use one slot transcript (and
// one system prompt), instead of creating a new per-intent session each time.
func TestWorkerSlotReusesTranscriptAcrossIntents(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	expID, err := d.CreateExploration("slot transcript", "reuse across intents")
	if err != nil {
		t.Fatal(err)
	}
	ts := d.Exploration(expID)
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID) })

	intentA, err := ts.AddIntent(map[string]any{"summary": "first slot task"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intentB, err := ts.AddIntent(map[string]any{"summary": "second slot task"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	var reqs []llm.CompletionRequest
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "done"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn"}, nil)
	}}
	capture := &captureProvider{inner: provider, reqs: &reqs}
	workDir := t.TempDir()
	tx := transcript.NewStore(filepath.Join(workDir, "transcripts"))
	worker := NewWorker(capture, "test-model", workDir, tx, 0, 3)
	run := func(id int64, summary string) {
		payload, _ := json.Marshal(map[string]any{"summary": summary})
		if _, _, err := worker.Execute(t.Context(), "work#1", expID, nil, ts, &db.Node{ID: id, Payload: payload}, nil, nil, nil, nil); err != nil {
			t.Fatalf("execute intent %d: %v", id, err)
		}
	}
	run(intentA, "first slot task")
	run(intentB, "second slot task")

	slotID := WorkerSessionIDForSlot(expID, 1)
	entries, err := os.ReadDir(filepath.Join(workDir, "transcripts"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, slotID+".jsonl") {
		t.Fatalf("slot transcript %s missing: %v", slotID, names)
	}
	if strings.Contains(joined, WorkerSessionID(expID, intentA)) || strings.Contains(joined, WorkerSessionID(expID, intentB)) {
		t.Fatalf("per-intent transcript must not be created anymore: %v", names)
	}
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	sysA, sysB := strings.Join(reqs[0].System, "\n"), strings.Join(reqs[1].System, "\n")
	// Runtime date text can differ; the intent-specific anchors must not.
	for _, intent := range []string{workerIntentMarker(intentA), "first slot task", workerIntentMarker(intentB), "second slot task"} {
		if strings.Contains(sysA, intent) || strings.Contains(sysB, intent) {
			t.Fatalf("system prompt leaked per-intent content %q", intent)
		}
	}
	if strings.TrimSpace(sysA) != strings.TrimSpace(sysB) {
		t.Fatalf("system prompt changed between intents (cache prefix broken):\n--- A ---\n%s\n--- B ---\n%s", sysA, sysB)
	}
	// Second run resumes the slot transcript: the first intent is still history,
	// and the new intent is appended as the current one.
	blob, _ := json.Marshal(reqs[1].Messages)
	// json.Marshal escapes < > in the marker, so match the stable id part.
	if !strings.Contains(string(blob), fmt.Sprintf("BLADE_WORKER_INTENT:%d", intentA)) ||
		!strings.Contains(string(blob), fmt.Sprintf("BLADE_WORKER_INTENT:%d", intentB)) {
		t.Fatalf("slot history did not carry both intent markers: %s", blob)
	}
}

type captureProvider struct {
	inner llm.Provider
	reqs  *[]llm.CompletionRequest
}

func (p *captureProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	*p.reqs = append(*p.reqs, req)
	return p.inner.Stream(ctx, req)
}

// Complete is used when Worker runs in non-streaming mode (not here), but the
// provider interface requires it.
func (p *captureProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.inner.Complete(ctx, req)
}
