package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
	"github.com/lin9527ya/blade/db"
)

func TestCompactHotRoundInputStripsDeltaAndFullHeaders(t *testing.T) {
	delta := "STATIC RULES\n【本次触发本轮的实际变动】\n- 新增漏洞\n\n【本轮增量态势（热会话延续）】\n" + strings.Repeat("DELTA-JSON-", 400)
	msgs := hotPrefixMessages(delta, "保留增量思考结论")
	if len(msgs) != 2 {
		t.Fatalf("want 2 msgs, got %d", len(msgs))
	}
	first := msgs[0].Text()
	if strings.Contains(first, "DELTA-JSON-") {
		t.Fatalf("compactHotRoundInput failed to strip delta header: %q", first)
	}
	if !strings.Contains(first, "新增漏洞") {
		t.Fatalf("compactHotRoundInput dropped trigger block: %q", first)
	}
}

func TestHotSessionSurvivesBeyond14MessagesWithoutReset(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("hot survival", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	prov := &recordingProvider{text: "结论"}
	tx := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
	p := NewPlanner(prov, "m", t.TempDir(), tx, 0, 4)
	p.SetNonStreaming(func() bool { return true })

	// Populate > 14 messages (e.g., 16 messages) in hot session across 4 rounds before round 5 reconcile.
	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "g", nil, nil); err != nil {
		t.Fatal(err)
	}
	if p.hot == nil {
		t.Fatal("expected hot session after round 1")
	}
	extra := make([]llm.Message, 0, 14)
	for i := 0; i < 7; i++ {
		extra = append(extra, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("tool step")}})
		extra = append(extra, llm.UserText("tool output"))
	}
	p.hot.AppendMessages(extra...)
	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "g", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(p.hot.Messages()) <= 16 || p.hotLastChangeCursor == "" {
		t.Fatalf("hot session reset too aggressively at >14 messages: len=%d cursor=%q", len(p.hot.Messages()), p.hotLastChangeCursor)
	}
}

func TestBlackboardDeltaCoreAndWorkerSituationData(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	if _, err := ts.AddIntent(map[string]any{"summary": "SQLi probe", "route": "login-sqli"}, 6, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AddFactGraph(map[string]any{"summary": "found admin endpoint", "enables": []string{"surface"}}, "work#1", nil, intent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AddNode(db.KindFoothold, map[string]any{"summary": "ssh root@host", "access": "ssh", "privilege": "root"}, 0, db.StateLootActive, "worker", nil); err != nil {
		t.Fatal(err)
	}
	v, err := ts.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	delta := tools.blackboardDeltaData(0, 20, v.Cursor())
	bb, _ := delta["blackboard"].(map[string]any)
	if bb == nil || bb["incremental_delta"] != true {
		t.Fatalf("expected incremental_delta=true, got %+v", bb)
	}
	for _, want := range []string{"totals", "routes", "capabilities", "open_intents", "changes", "read_status"} {
		if _, ok := delta[want]; !ok {
			t.Fatalf("delta view missing %q", want)
		}
	}
	for _, forbid := range []string{"cold_digests", "folded_intents", "related_tasks", "attention_status"} {
		if _, ok := delta[forbid]; ok {
			t.Fatalf("delta view leaked full-only key %q", forbid)
		}
	}

	worker := tools.graphOverviewDataForWorker()
	for _, want := range []string{"goals", "hints", "task", "capabilities", "footholds", "finding_list"} {
		if _, ok := worker[want]; !ok {
			t.Fatalf("worker situation missing %q", want)
		}
	}
	for _, forbid := range []string{"branch_review_facts", "attention_status", "cold_digests", "folded_intents", "related_tasks", "open_intents", "recent_done_intents"} {
		if _, ok := worker[forbid]; ok {
			t.Fatalf("worker situation leaked planner-only key %q", forbid)
		}
	}
}

func TestRecordFactBatchCoalescesNotification(t *testing.T) {
	tools, _, intent := newToolWorker(t)
	var called int
	var gotKind string
	var gotAssets []int64
	tools.SetNotifyFact(func(kind string, _, _ int64, _ string, assetIDs []int64) {
		called++
		gotKind = kind
		gotAssets = assetIDs
	})
	payload := fmt.Sprintf(`{"facts":[
		{"summary":"info banner","evidence":"HTTP 200","confidence":"observed","intent_id":%d},
		{"summary":"拿到管理员口令","evidence":"pass=123","confidence":"observed","enables":["credential"],"intent_id":%d},
		{"summary":"发现旁路接口","evidence":"/debug","confidence":"observed","leads":[{"text":"查 /debug"}],"intent_id":%d}
	]}`, intent, intent, intent)
	out := callTool(t, tools.WorkerTools(), "record_fact", payload)
	if !strings.Contains(out, `"ids"`) {
		t.Fatalf("unexpected record_fact batch output: %s", out)
	}
	if called != 1 {
		t.Fatalf("batch record_fact fired %d notifications, want 1", called)
	}
	if gotKind != "lead_fact" && gotKind != "capability_fact" {
		t.Fatalf("got low-priority kind %q instead of high-priority batch event", gotKind)
	}
	_ = gotAssets
}
