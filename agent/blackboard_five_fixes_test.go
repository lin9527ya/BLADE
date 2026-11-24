package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestProposalKeyPreservesChangedConditions(t *testing.T) {
	base := intentProposal{
		Summary: "验证 /api/a-b", Hypothesis: "认证边界", Mechanism: "query-auth", Route: "auth-chain",
		Reason: "匿名请求返回 403", AssetIDs: []int64{12, 13}, DependsOn: []int64{7, 8},
	}
	key := proposalKey(base)
	reordered := base
	reordered.AssetIDs = []int64{13, 12, 12}
	reordered.DependsOn = []int64{8, 7, 7}
	if got := proposalKey(reordered); got != key {
		t.Fatal("id set ordering must not turn a replay into a new proposal")
	}
	if !reflect.DeepEqual(base.AssetIDs, []int64{12, 13}) || !reflect.DeepEqual(base.DependsOn, []int64{7, 8}) {
		t.Fatal("proposal key mutated caller id slices")
	}
	for _, tc := range []struct {
		name string
		edit func(*intentProposal)
	}{
		{"new evidence", func(p *intentProposal) { p.Reason = "拿到有效管理员凭据" }},
		{"new prerequisite", func(p *intentProposal) { p.DependsOn = []int64{7, 9} }},
		{"distinct path", func(p *intentProposal) { p.Summary = "验证 /api/ab" }},
		{"distinct mechanism", func(p *intentProposal) { p.Mechanism = "query_auth" }},
		{"distinct identity", func(p *intentProposal) { p.Hypothesis = "认证边界 | 身份 B" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.edit(&changed)
			if proposalKey(changed) == key {
				t.Fatal("changed experimental conditions were deduplicated")
			}
		})
	}
}

func TestProposalsKeepNewReasonAndPrerequisite(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	base := intentProposal{Summary: "验证相同入口", Reason: "缺少认证态", DependsOn: []int64{1}}
	input := func(p intentProposal) string {
		raw, err := json.Marshal(map[string]any{"proposals": []intentProposal{p}})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	assertNew := func(p intentProposal) {
		t.Helper()
		out := callTool(t, tools.WorkerTools(), "propose_intents", input(p))
		if !strings.Contains(out, `"proposals":1`) {
			t.Fatalf("new conditions should produce a proposal: %s", out)
		}
	}
	assertNew(base)
	if out := callTool(t, tools.WorkerTools(), "propose_intents", input(base)); !strings.Contains(out, "no new proposals") {
		t.Fatalf("same proposal replay should deduplicate: %s", out)
	}
	newReason := base
	newReason.Reason = "已拿到有效认证态"
	assertNew(newReason)
	newDependency := base
	newDependency.DependsOn = []int64{2}
	assertNew(newDependency)
	facts, err := ts.ListByKind(db.KindFact, 10)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, fact := range facts {
		var payload struct {
			Origin string `json:"origin"`
		}
		if json.Unmarshal(fact.Payload, &payload) == nil && payload.Origin == "proposal" {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("want 3 condition versions, got %d", count)
	}
}

func TestProposalReplayReadsLegacyDetail(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	proposal := intentProposal{Summary: "旧提案", Reason: "旧证据", DependsOn: []int64{1}}
	detail, err := json.Marshal(map[string]any{"proposals": []intentProposal{proposal}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AddFactGraph(map[string]any{
		"origin": "proposal", "summary": "旧版提案事实", "detail": string(detail),
	}, "worker", nil, 0, nil); err != nil {
		t.Fatal(err)
	}
	if out := callTool(t, tools.WorkerTools(), "propose_intents", string(detail)); !strings.Contains(out, "no new proposals") {
		t.Fatalf("detail-only legacy proposal should still deduplicate: %s", out)
	}
}

func TestFactLeadsAreStoredWithoutWindowLimits(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	prefix := strings.Repeat("必须保留的分叉细节", 60)
	facts := make([]map[string]any, 5)
	for i := range facts {
		leads := make([]map[string]any, 0, 15)
		for j := 0; j < 12; j++ {
			text := fmt.Sprintf("独立线索 %d-%d", i, j)
			if i == 0 && j < 2 {
				text = prefix + fmt.Sprintf("末尾差异 %d", j)
			}
			leads = append(leads, map[string]any{"text": text})
		}
		if i == 0 {
			leads = append(leads,
				map[string]any{"text": "相同文字但不同资产", "asset_ids": []int64{1}},
				map[string]any{"text": "相同文字但不同资产", "asset_ids": []int64{2}},
				map[string]any{"text": "相同文字但不同资产", "asset_ids": []int64{1, 1}},
			)
		}
		facts[i] = map[string]any{"summary": fmt.Sprintf("事实 %d", i), "leads": leads}
	}
	raw, err := json.Marshal(map[string]any{"facts": facts})
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.WorkerTools(), "record_fact", string(raw))
	var result struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.IDs) != 5 {
		t.Fatalf("fact batch: %s (%v)", out, err)
	}
	n, err := ts.GetNode(result.IDs[0])
	if err != nil || n == nil {
		t.Fatalf("first fact: %v", err)
	}
	var payload struct {
		Leads []struct {
			Text     string  `json:"text"`
			AssetIDs []int64 `json:"asset_ids"`
		} `json:"leads"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Leads) != 14 || payload.Leads[0].Text != prefix+"末尾差异 0" || payload.Leads[1].Text != prefix+"末尾差异 1" {
		t.Fatalf("fact lead count/text was cut: count=%d", len(payload.Leads))
	}
	if !reflect.DeepEqual(payload.Leads[12].AssetIDs, []int64{1}) || !reflect.DeepEqual(payload.Leads[13].AssetIDs, []int64{2}) {
		t.Fatal("same text on different assets was incorrectly deduplicated")
	}
	trace, err := ts.LeadTraceReport("", "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Stats.Total != 62 || len(trace.Rows) != 62 {
		t.Fatalf("full trace should retain 62 leads: stats=%+v rows=%d", trace.Stats, len(trace.Rows))
	}
	last := fmt.Sprintf("L%d-11", result.IDs[4])
	trace, err = ts.LeadTraceReport("", last, 5, 0)
	if err != nil || len(trace.Rows) != 1 || trace.Rows[0].Text != "独立线索 4-11" {
		t.Fatalf("tail lead should remain addressable: %v (%v)", trace, err)
	}
}

func TestProposalStorageExceedsOldTwentyEntryCap(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	items := make([]intentProposal, 0, 36)
	for i := 0; i < 35; i++ {
		items = append(items, intentProposal{Summary: fmt.Sprintf("独立提案 %d", i), Reason: fmt.Sprintf("依据 %d", i)})
	}
	items = append(items, items[0])
	raw, err := json.Marshal(map[string]any{"proposals": items})
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.WorkerTools(), "propose_intents", string(raw))
	var result struct {
		FactID     int64 `json:"fact_id"`
		Proposals  int   `json:"proposals"`
		Dropped    int   `json:"dropped"`
		Duplicates int   `json:"duplicates"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.FactID == 0 || result.Proposals != 35 || result.Dropped != 1 || result.Duplicates != 1 {
		t.Fatalf("only exact duplicates should be omitted: %s", out)
	}
	trace, err := ts.LeadTraceReport("", "", 5, 30)
	if err != nil || trace.Total != 35 || len(trace.Rows) != 5 {
		t.Fatalf("proposal tail page: %+v (%v)", trace, err)
	}
	last := fmt.Sprintf("P%d-34", result.FactID)
	trace, err = ts.LeadTraceReport("", last, 5, 0)
	if err != nil || len(trace.Rows) != 1 || trace.Rows[0].Text != "独立提案 34" {
		t.Fatalf("proposal tail entry should remain addressable: %+v (%v)", trace, err)
	}
	adopted := callTool(t, tools.PlannerTools(), "add_intent", fmt.Sprintf(`{"summary":"接手尾部提案","lead_ids":[%q]}`, last))
	if !strings.HasPrefix(adopted, "intent created:") {
		t.Fatalf("tail proposal cannot be adopted: %s", adopted)
	}
	trace, err = ts.LeadTraceReport("", last, 5, 0)
	if err != nil || len(trace.Rows) != 1 || trace.Rows[0].State != "queued" {
		t.Fatal("tail proposal adoption was not traceable")
	}
}

func TestDeltaIntentSummaryPreservesBranchDimensions(t *testing.T) {
	tools, ts, dependency := newToolWorker(t)
	parent, err := ts.AddNode(db.KindFact, map[string]any{"summary": "入口依据"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.Repeat("同一入口与研究问题，", 20)
	hypothesis := strings.Repeat("需要保留的身份前提，", 30) + "最终身份 A"
	first, err := ts.AddIntentGraph(map[string]any{
		"summary": prefix + "GET Query 身份 A", "hypothesis": hypothesis,
		"asset_ids": []int64{42}, "depends_on": []int64{dependency}, "depends_on_mode": "advisory",
		"contract": map[string]any{"route": "route-A", "mechanism": "query-check", "acceptance": "LARGE-RESULT"},
		"detail":   "LARGE-DETAIL", "evidence": "LARGE-EVIDENCE",
	}, 5, nil, "planner", []db.IntentEdge{{From: parent, Rel: db.RelDerivedFrom}, {To: dependency, Rel: db.RelDependsOn}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ts.AddIntentGraph(map[string]any{
		"summary": prefix + "POST Body 身份 B", "hypothesis": "最终身份 B", "asset_ids": []int64{43},
		"route": "route-B", "risk_area": "auth", "depends_on_mode": "strict",
	}, 5, nil, "planner", []db.IntentEdge{{From: parent, Rel: db.RelDerivedFrom}})
	if err != nil {
		t.Fatal(err)
	}
	version, err := ts.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	view := tools.blackboardDeltaData(0, 30, version.Cursor())
	rows, ok := view["open_intents"].([]map[string]any)
	if !ok {
		t.Fatalf("delta missing intent window: %v", view)
	}
	byID := map[int64]map[string]any{}
	for _, row := range rows {
		byID[row["id"].(int64)] = row
	}
	a, b := byID[first], byID[second]
	if a == nil || b == nil || a["summary"] == b["summary"] || a["summary"] != prefix+"GET Query 身份 A" || a["hypothesis"] != hypothesis {
		t.Fatal("direction suffix or full hypothesis was lost")
	}
	if !reflect.DeepEqual(a["parents"], []int64{parent}) || a["depends_on_mode"] != "advisory" || b["risk_area"] != "auth" ||
		firstAssetIDValue(a["asset_ids"]) != 42 || firstAssetIDValue(b["asset_ids"]) != 43 || firstAssetIDValue(a["depends_on"]) != dependency {
		t.Fatalf("parent/dependency/asset/risk dimensions missing: a=%v b=%v", a, b)
	}
	contract, _ := a["contract"].(map[string]any)
	if len(contract) != 2 || contract["route"] != "route-A" || contract["mechanism"] != "query-check" {
		t.Fatalf("minimal route contract: %v", contract)
	}
	for _, key := range []string{"detail", "evidence"} {
		if _, ok := a[key]; ok {
			t.Fatalf("delta retained a large result body %q", key)
		}
	}
	if err := ts.SetIntentState(first, "running"); err != nil {
		t.Fatal(err)
	}
	worker := tools.graphOverviewDataForWorker()
	peers, _ := worker["running_intents"].([]map[string]any)
	if len(peers) != 1 || peers[0]["hypothesis"] != hypothesis || !reflect.DeepEqual(peers[0]["parents"], []int64{parent}) {
		t.Fatalf("worker peer window lost branch dimensions: %v", peers)
	}
}

func TestSlimDeltaDoesNotRecutBranchSummary(t *testing.T) {
	summary := strings.Repeat("研究问题", 50) + "末尾独立身份"
	row := map[string]any{
		"id": int64(1), "state": "open", "summary": summary, "hypothesis": "身份 A",
		"asset_ids": []int64{42}, "parents": []int64{3}, "depends_on": []int64{4},
		"detail": "large result body", "evidence": "raw output",
	}
	view := map[string]any{"open_intents": []map[string]any{row}}
	slimBlackboardDelta(view)
	rows := view["open_intents"].([]map[string]any)
	if rows[0]["summary"] != summary || rows[0]["hypothesis"] != "身份 A" || !reflect.DeepEqual(rows[0]["parents"], []int64{3}) {
		t.Fatal("legacy slim path cut branch dimensions")
	}
	if rows[0]["detail"] != nil || rows[0]["evidence"] != nil {
		t.Fatal("slim path should still omit result bodies")
	}
}

func TestStoredLeadLimitDescriptionRefreshesWithoutUserOverride(t *testing.T) {
	tool := NewToolSet(nil, "").recordFact()
	old := "【本意图之外的线索】数组，元素 {text, asset_ids?}：看到的其它入口/参数/行为/资产，值得 planner 下一轮跟进。最多 8 条、每条 ≤300 字；线索不是结论，不需要 evidence。不要把线索塞进 summary。"
	stored := map[string]any{"type": "object", "properties": map[string]any{
		"leads":   map[string]any{"type": "array", "description": old},
		"summary": map[string]any{"type": "string", "description": "用户自定义说明", "default": "用户默认值"},
	}}
	decorated := DecorateStoredTool(tool, "用户工具说明", stored)
	props := decorated.InputSchema()["properties"].(map[string]any)
	leadDesc := props["leads"].(map[string]any)["description"].(string)
	if strings.Contains(leadDesc, "最多 8 条") || !strings.Contains(leadDesc, "完整保存") {
		t.Fatalf("old builtin annotation still hides complete storage: %s", leadDesc)
	}
	summary := props["summary"].(map[string]any)
	if summary["description"] != "用户自定义说明" || summary["default"] != "用户默认值" {
		t.Fatal("user annotation/default was overwritten")
	}
	stored["properties"].(map[string]any)["leads"].(map[string]any)["description"] = "用户线索说明"
	decorated = DecorateStoredTool(tool, "用户工具说明", stored)
	props = decorated.InputSchema()["properties"].(map[string]any)
	if props["leads"].(map[string]any)["description"] != "用户线索说明" {
		t.Fatal("genuine user lead annotation was overwritten")
	}
}

func TestCompleteLeadsKeepBoundedAddressablePreviews(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	text := strings.Repeat("原始完整分叉细节", 80) + "末尾差异"
	leads := make([]map[string]any, 12)
	for i := range leads {
		leads[i] = map[string]any{"text": text + fmt.Sprintf(" %d", i)}
	}
	fact, err := ts.AddFactGraph(map[string]any{"summary": "长线索", "leads": leads}, "worker", nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := ts.GetNode(fact)
	if err != nil {
		t.Fatal(err)
	}
	preview := compactBlackboardNode(n)
	items, _ := preview["leads"].([]map[string]any)
	status, _ := preview["leads_read_status"].(db.ReadStatus)
	if len(items) != 8 || status.Total == nil || *status.Total != 12 || !status.HasMore {
		t.Fatalf("compact lead window should not copy all stored text: items=%d status=%+v", len(items), status)
	}
	if items[0]["id"] != fmt.Sprintf("L%d-0", fact) || items[0]["text_truncated"] != true || items[0]["read_more"] == nil {
		t.Fatalf("clipped lead must retain identity and drilldown: %v", items[0])
	}
	version, err := ts.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []map[string]any{tools.graphOverviewData(), tools.blackboardDeltaData(0, 20, version.Cursor())} {
		rows, _ := view["recent_leads"].([]map[string]any)
		if len(rows) != 12 || rows[0]["text_truncated"] != true || rows[0]["read_more"] == nil {
			t.Fatal("overview should send addressable lead previews, not full long bodies")
		}
		if len([]rune(rows[0]["text"].(string))) > maxLeadPreviewRunes+1 {
			t.Fatal("overview lead text exceeded the preview bound")
		}
	}
	trace, err := ts.LeadTraceReport("", fmt.Sprintf("L%d-11", fact), 5, 0)
	if err != nil || len(trace.Rows) != 1 || trace.Rows[0].Text != text+" 11" {
		t.Fatal("read-side preview limit leaked into stored/drilldown content")
	}
}

func TestRecentRoutePreviewUsesEarliestParent(t *testing.T) {
	routes := blackboardRoutes(map[string]any{
		"open_intents": []map[string]any{{
			"id": int64(12), "state": "open", "summary": "独立父节点分叉",
			"parents": []any{int64(9), " 3 "},
		}},
	})
	if len(routes) != 1 || routes[0]["route"] != "parent:3" || routes[0]["open"] != 1 {
		t.Fatalf("recent route preview disagrees with earliest-parent fallback: %v", routes)
	}
	rows, _ := routes[0]["recent"].([]map[string]any)
	if len(rows) != 1 || rows[0]["id"] != int64(12) {
		t.Fatal("parent route lost its recent intent summary")
	}
}

func TestSlimIntentEmptyPayloadRemainsReadable(t *testing.T) {
	rows := slimIntentNodes([]*db.Node{{ID: 12, State: "open", Payload: json.RawMessage("null")}}, nil)
	if len(rows) != 1 || rows[0]["id"] != int64(12) || rows[0]["state"] != "open" {
		t.Fatal("legacy empty payload should not panic during lightweight projection")
	}
}
