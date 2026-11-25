package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	baseVer, err := ts.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AddIntent(map[string]any{"summary": "SQLi probe", "route": "login-sqli"}, 6, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AddFactGraph(map[string]any{"summary": "found admin endpoint", "enables": []string{"surface"}}, "work#1", nil, intent, nil); err != nil {
		t.Fatal(err)
	}
	fhID, err := ts.AddNode(db.KindFoothold, map[string]any{"summary": "ssh root@host", "access": "ssh", "privilege": "root"}, 0, db.StateLootActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	crID, err := ts.AddNode(db.KindCredential, map[string]any{"summary": "admin token", "kind": "token", "username": "root"}, 0, db.StateLootActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	fnID, err := ts.AddNode(db.KindFinding, map[string]any{"summary": "RCE on /upload", "vulnclass": "rce", "severity": "critical"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	delta := tools.blackboardDeltaData(0, 20, baseVer.Cursor())
	bb, _ := delta["blackboard"].(map[string]any)
	if bb == nil || bb["incremental_delta"] != true {
		t.Fatalf("expected incremental_delta=true, got %+v", bb)
	}
	for _, want := range []string{"totals", "routes", "capabilities", "open_intents", "changes", "read_status", "delta_footholds", "delta_credentials", "delta_findings"} {
		if _, ok := delta[want]; !ok {
			t.Fatalf("delta view missing %q", want)
		}
	}
	changes, _ := delta["changes"].([]db.BlackboardChange)
	seenKinds := map[string]bool{}
	for _, c := range changes {
		if c.Kind != "" {
			seenKinds[c.Kind] = true
		}
	}
	for _, wantKind := range []string{db.KindIntent, db.KindFact, db.KindFoothold, db.KindCredential, db.KindFinding} {
		if !seenKinds[wantKind] {
			t.Fatalf("BlackboardChange missing kind=%q in changes=%+v", wantKind, changes)
		}
	}
	if df, _ := delta["delta_footholds"].([]map[string]any); len(df) != 1 || df[0]["id"] != fhID {
		t.Fatalf("unexpected delta_footholds: %+v", delta["delta_footholds"])
	}
	if dc, _ := delta["delta_credentials"].([]map[string]any); len(dc) != 1 || dc[0]["id"] != crID {
		t.Fatalf("unexpected delta_credentials: %+v", delta["delta_credentials"])
	}
	if dv, _ := delta["delta_findings"].([]map[string]any); len(dv) != 1 || dv[0]["id"] != fnID {
		t.Fatalf("unexpected delta_findings: %+v", delta["delta_findings"])
	}
	for _, forbid := range []string{"cold_digests", "folded_intents", "related_tasks", "attention_status", "task", "campaign", "asset_scope"} {
		if _, ok := delta[forbid]; ok {
			t.Fatalf("delta view leaked full-only/static key %q", forbid)
		}
	}

	worker := tools.graphOverviewDataForWorker()
	for _, want := range []string{"goals", "hints", "task", "capabilities", "footholds", "finding_list"} {
		if _, ok := worker[want]; !ok {
			t.Fatalf("worker situation missing %q", want)
		}
	}
	for _, forbid := range []string{"running_intents", "branch_review_facts", "attention_status", "cold_digests", "folded_intents", "related_tasks", "open_intents", "recent_done_intents"} {
		if _, ok := worker[forbid]; ok {
			t.Fatalf("worker situation leaked forbidden/planner-only key %q", forbid)
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

func TestListLeadsToolAndProposeIntentsSummary(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions("leads task", "验证线索分页", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	a1, err := d.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://one.leads-test.example", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := d.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://two.leads-test.example", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE url LIKE '%leads-test.example%' OR domain LIKE '%leads-test.example%'`)
	})
	ts := d.Exploration(task.ExplorationID)
	tools := NewToolSet(ts, "work#1")
	tools.SetTaskID(task.ID)
	tools.SetAssetStore(d.Assets(), nil)

	propOut := callTool(t, tools.WorkerTools(), "propose_intents", fmt.Sprintf(`{"proposals":[
		{"summary":"测 /upload 双写绕过","asset_ids":[%d,%d,99999999]},
		{"summary":"复核 /admin 越权删除","asset_ids":[%d]},
		{"summary":"验证导出接口 SSRF"}
	]}`, a1, a2, a2))
	var propRes struct {
		FactID    int64 `json:"fact_id"`
		Proposals int   `json:"proposals"`
	}
	if err := json.Unmarshal([]byte(propOut), &propRes); err != nil || propRes.FactID <= 0 || propRes.Proposals != 3 {
		t.Fatalf("unexpected propose_intents result: %s (err=%v)", propOut, err)
	}
	n, err := ts.GetNode(propRes.FactID)
	if err != nil || n == nil {
		t.Fatalf("proposal fact missing: %v", err)
	}
	var fp map[string]any
	if err := json.Unmarshal(n.Payload, &fp); err != nil {
		t.Fatal(err)
	}
	propSummary, _ := fp["summary"].(string)
	if !strings.Contains(propSummary, "测 /upload 双写绕过") || !strings.Contains(propSummary, "3 个方向") {
		t.Fatalf("proposal summary should include concrete directions: %q", propSummary)
	}

	page1Raw := callTool(t, tools.PlannerTools(), "list_leads", `{"state":"pending","limit":2,"offset":0}`)
	var page1 struct {
		Total      int              `json:"total"`
		HasMore    bool             `json:"has_more"`
		NextOffset int              `json:"next_offset"`
		Rows       []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(page1Raw), &page1); err != nil {
		t.Fatalf("list_leads invalid JSON: %v\n%s", err, page1Raw)
	}
	if page1.Total != 3 || !page1.HasMore || page1.NextOffset != 2 || len(page1.Rows) != 2 {
		t.Fatalf("unexpected page1 pagination: %+v", page1)
	}
	page2Raw := callTool(t, tools.PlannerTools(), "list_leads", fmt.Sprintf(`{"state":"pending","limit":2,"offset":%d}`, page1.NextOffset))
	var page2 struct {
		HasMore bool             `json:"has_more"`
		Rows    []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(page2Raw), &page2); err != nil {
		t.Fatalf("list_leads page2 invalid JSON: %v", err)
	}
	if page2.HasMore || len(page2.Rows) != 1 {
		t.Fatalf("unexpected page2 pagination: %+v", page2)
	}
}

func TestDeleteGraphHealthEvictsCache(t *testing.T) {
	expID := int64(987654321)
	graphHealthCache.Store(expID, graphHealthEntry{at: time.Now(), data: map[string]any{"issues": 1}})
	t.Cleanup(func() { DeleteGraphHealth(expID) })
	if _, ok := graphHealthCache.Load(expID); !ok {
		t.Fatal("expected cache entry before delete")
	}
	DeleteGraphHealth(expID)
	if _, ok := graphHealthCache.Load(expID); ok {
		t.Fatal("DeleteGraphHealth did not evict entry")
	}
}
