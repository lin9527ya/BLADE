package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/policy"
)

// 队列背压：可领意图 ≥ 2×workers 时拒收常规方向；priority ≥8 是逃生口；
// 链路步骤（前置未满足）不占额度——它们此刻也进不了 frontier。
func TestIntentQueueBackpressure(t *testing.T) {
	tools, ts, _ := newToolWorker(t) // 已有一条 open 意图
	tools.SetWorkerCap(1)            // 上限 = 2×1 = 2
	add := func(summary string, priority int, extra string) string {
		return callTool(t, tools.PlannerTools(), "add_intent",
			fmt.Sprintf(`{"summary":%q,"priority":%d%s}`, summary, priority, extra))
	}
	if out := add("第二条常规方向", 5, ""); !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("到上限前应放行：%s", out)
	}
	if out := add("第三条常规方向", 5, ""); !strings.Contains(out, "队列里已有") {
		t.Fatalf("饱和后常规方向应被拒：%s", out)
	}
	if out := add("刚发现凭据驱动的跟进", 8, ""); !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("priority≥8 应绕过队列上限：%s", out)
	}
	open, err := ts.ListByKind(db.KindIntent, 50)
	if err != nil {
		t.Fatal(err)
	}
	var firstOpen int64
	for _, n := range open {
		if n.State == "open" {
			firstOpen = n.ID
			break
		}
	}
	if out := add("串行链的下一步", 5, fmt.Sprintf(`,"depends_on":[%d]`, firstOpen)); !strings.HasPrefix(out, "intent created: ") {
		t.Fatalf("带未满足前置的链路步骤不该被队列上限拦：%s", out)
	}

	// 概览把饱和状态摆给 planner 看。
	overview := tools.graphOverviewData()
	q, ok := overview["intent_queue"].(map[string]any)
	if !ok {
		t.Fatalf("概览缺少 intent_queue：%#v", overview["intent_queue"])
	}
	if q["saturated"] != true || q["workers"] != 1 || q["soft_limit"] != 2 {
		t.Fatalf("饱和标记不对：%+v", q)
	}
	if got, _ := overview["claimable_intents"].(int); got < 2 {
		t.Fatalf("claimable_intents 不对：%v", overview["claimable_intents"])
	}
}

// 批量提交必须逐条记账：一次 4 条的批次不能突破在途上限（此前只加 round，能超 3 条）。
func TestIntentBatchRespectsOpenCap(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	for i := 0; i < 3; i++ {
		if _, err := ts.AddIntent(map[string]any{"summary": fmt.Sprintf("预置方向 %d", i)}, 5, nil, "planner"); err != nil {
			t.Fatal(err)
		}
	}
	// 现有 4 条 open；上限 5 → 本批只应再建 1 条。
	tools.SetPolicy(&policy.Effective{Version: 1, MaxIntentsPerRound: 4, MaxOpenIntents: 5})
	out := callTool(t, tools.PlannerTools(), "add_intent",
		`{"intents":[{"summary":"批次一"},{"summary":"批次二"},{"summary":"批次三"},{"summary":"批次四"}]}`)
	var res struct {
		IDs    []int64           `json:"ids"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("批量返回不是 JSON：%v\n%s", err, out)
	}
	created := 0
	for _, id := range res.IDs {
		if id > 0 {
			created++
		}
	}
	if created != 1 || len(res.Errors) != 3 {
		t.Fatalf("上限 5、已有 4 条时一批 4 条只能建 1 条：created=%d errors=%v", created, res.Errors)
	}
	if open, _ := ts.CountOpenIntents(); open != 5 {
		t.Fatalf("在途数应停在上限 5，实际 %d", open)
	}
}

// drop_intent：清掉排队意图，并**解锁依赖它的下游**（否则下游永远等不到前置）。
func TestDropIntentUnblocksDependents(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	pre := lootNodeID(t, callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"前置：拿到入口"}`))
	down := lootNodeID(t, callTool(t, tools.PlannerTools(), "add_intent",
		fmt.Sprintf(`{"summary":"下游：用入口的产出","depends_on":[%d]}`, pre)))

	out := callTool(t, tools.PlannerTools(), "drop_intent",
		fmt.Sprintf(`{"intent_id":%d,"reason":"被新事实覆盖，不再需要"}`, pre))
	if !strings.Contains(out, "dropped") || !strings.Contains(out, fmt.Sprintf("#%d", down)) {
		t.Fatalf("删除应回执并列出被解锁的下游：%s", out)
	}
	if fr, _ := ts.Frontier(50); !hasIntent(fr, down) {
		t.Fatal("前置被删后下游应立即可领")
	}
	// 理由必填；正在跑的不许删。
	if got := callTool(t, tools.PlannerTools(), "drop_intent", fmt.Sprintf(`{"intent_id":%d}`, down)); !strings.Contains(got, "reason 必填") {
		t.Fatalf("空理由应被拒：%s", got)
	}
	if ok, err := ts.ClaimIntent(down, "work#1", 60*1e9); err != nil || !ok {
		t.Fatalf("领取下游：%v %v", ok, err)
	}
	if got := callTool(t, tools.PlannerTools(), "drop_intent", fmt.Sprintf(`{"intent_id":%d,"reason":"x"}`, down)); !strings.Contains(got, "先 kill_work") {
		t.Fatalf("运行中的意图不能直接丢：%s", got)
	}
}
