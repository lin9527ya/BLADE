package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 线索与事实语义：record_fact 的 leads/polarity 必须落库，且能进 planner 态势。
func TestRecordFactLeadsAndPolarity(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	out := callTool(t, tools.WorkerTools(), "record_fact",
		`{"intent_id":`+strconv.FormatInt(intent, 10)+`,"summary":"/admin 302 到 /login","polarity":"positive","enables":["surface"],"leads":[{"text":"robots 暴露 /files/exportimport/"}]}`)
	if !strings.Contains(out, "fact recorded") {
		t.Fatalf("record_fact 应成功：%s", out)
	}
	facts, _ := ts.ListByKind(db.KindFact, 10)
	var factID int64
	for _, n := range facts {
		if n.State != "origin" {
			factID = n.ID
		}
	}
	if factID == 0 {
		t.Fatal("没找到写入的事实")
	}
	n, _ := ts.GetNode(factID)
	var p struct {
		Polarity string           `json:"polarity"`
		Enables  []string         `json:"enables"`
		Leads    []map[string]any `json:"leads"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	if p.Polarity != "positive" || len(p.Enables) != 1 || p.Enables[0] != "surface" || len(p.Leads) != 1 {
		t.Fatalf("事实语义落库不对：%+v", p)
	}

	data := tools.graphOverviewData()
	leads, _ := data["recent_leads"].([]map[string]any)
	if len(leads) != 1 || leads[0]["id"] != leadID(factID, 0) {
		t.Fatalf("recent_leads 应带出 1 条线索：%+v", data["recent_leads"])
	}
	if data["leads_open"] != 1 || data["leads_total"] != 1 {
		t.Fatalf("leads 计数不对：open=%v total=%v", data["leads_open"], data["leads_total"])
	}
	// worker 侧不应看到规划层的线索账本。
	w := tools.graphOverviewDataForWorker()
	if _, ok := w["recent_leads"]; ok {
		t.Fatal("worker 态势不应包含 recent_leads")
	}
}

// 线索转意图：converts_lead_ids 落库，overview 的 leads_converted 随之更新；
// dismiss 后线索不再出现在 recent_leads。
func TestLeadConvertAndDismiss(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	callTool(t, tools.WorkerTools(), "record_fact",
		`{"intent_id":`+strconv.FormatInt(intent, 10)+`,"summary":"认证面观察","leads":[{"text":"/admin/login 被改写"},{"text":"robots 有 /files/"}]}`)
	facts, _ := ts.ListByKind(db.KindFact, 10)
	var factID int64
	for _, n := range facts {
		if n.State != "origin" {
			factID = n.ID
		}
	}
	lead0 := leadID(factID, 0)
	lead1 := leadID(factID, 1)

	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"auth","summary":"对 /admin/login 测账号枚举","converts_lead_ids":["`+lead0+`"],"parent_ids":[`+strconv.FormatInt(factID, 10)+`]}`)
	if !strings.Contains(out, "intent created") {
		t.Fatalf("带 converts_lead_ids 的意图应成功：%s", out)
	}
	data := tools.graphOverviewData()
	if data["leads_converted"] != 1 || data["leads_open"] != 1 {
		t.Fatalf("转化计数不对：converted=%v open=%v", data["leads_converted"], data["leads_open"])
	}

	out = callTool(t, tools.PlannerTools(), "dismiss_leads", `{"lead_ids":["`+lead1+`"],"reason":"已否定"}`)
	if !strings.Contains(out, "leads dismissed: 1") {
		t.Fatalf("dismiss_leads 应成功：%s", out)
	}
	data = tools.graphOverviewData()
	if data["leads_open"] != 0 || data["leads_dismissed"] != 1 {
		t.Fatalf("dismiss 后计数不对：open=%v dismissed=%v", data["leads_open"], data["leads_dismissed"])
	}
	if leads, _ := data["recent_leads"].([]map[string]any); len(leads) != 0 {
		t.Fatalf("dismiss 后不应再有 open lead：%+v", leads)
	}
	// 非法 lead id 必须被拒。
	out = callTool(t, tools.PlannerTools(), "add_intent", `{"risk_area":"auth","summary":"对 /x 测枚举","converts_lead_ids":["L999-0"]}`)
	if !strings.Contains(out, "不存在") {
		t.Fatalf("非法 lead id 应被拒：%s", out)
	}
}

// 血缘 kind：parents 里混着 goal/fact 时，planner 要能分清。
func TestOverviewParentKinds(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	fact, err := ts.AddNode(db.KindFact, map[string]any{"summary": "入口观察", "polarity": "positive"}, 5, "confirmed", "work#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"risk_area":"auth","summary":"对 /login 测认证","parent_ids":[`+strconv.FormatInt(fact, 10)+`]}`)
	if !strings.Contains(out, "intent created") {
		t.Fatalf("add_intent 应成功：%s", out)
	}
	data := tools.graphOverviewData()
	items, _ := data["open_intents"].([]map[string]any)
	var target map[string]any
	for _, it := range items {
		if sv, _ := it["summary"].(string); strings.Contains(sv, "测认证") {
			target = it
		}
	}
	if target == nil {
		t.Fatalf("没找到新建的意图：%+v", items)
	}
	pk, _ := target["parent_kinds"].(map[string]string)
	if pk[strconv.FormatInt(fact, 10)] != "fact" {
		t.Fatalf("parent_kinds 应标出 fact：%+v", target)
	}
}

// 未消费事实只催办正向/存疑的；否定收尾不该被当成「还有下文」。
func TestUnconsumedFactsSkipsNegative(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	pos, _ := ts.AddNode(db.KindFact, map[string]any{"summary": "发现 /api/v2 端点", "polarity": "positive"}, 5, "confirmed", "work#1", nil)
	neg, _ := ts.AddNode(db.KindFact, map[string]any{"summary": "SQL 注入不可用", "polarity": "negative"}, 5, "confirmed", "work#1", nil)
	data := tools.graphOverviewData()
	items, _ := data["unconsumed_facts"].([]map[string]any)
	seen := map[int64]bool{}
	for _, it := range items {
		if id, ok := it["id"].(int64); ok {
			seen[id] = true
		}
	}
	if !seen[pos] || seen[neg] {
		t.Fatalf("unconsumed_facts 只应包含正向事实：%+v", items)
	}
}

// 被终止的意图要出现在 recent_done_intents，否则会被当成「没派过」重派。
func TestStoppedIntentVisibleInOverview(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	if err := ts.SetIntentState(intent, "stopped"); err != nil {
		t.Fatal(err)
	}
	data := tools.graphOverviewData()
	items, _ := data["recent_done_intents"].([]map[string]any)
	for _, it := range items {
		if id, ok := it["id"].(int64); ok && id == intent {
			if st, _ := it["state"].(string); st == "stopped" {
				return
			}
		}
	}
	t.Fatalf("stopped 意图应出现在 recent_done_intents：%+v", items)
}
