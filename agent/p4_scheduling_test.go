package agent

import (
	"encoding/json"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestShouldReview(t *testing.T) {
	cases := []struct {
		name              string
		rounds, facts, ly int
		want              bool
	}{
		{"轮次不够（<3）即使收成下降也不复盘", 2, 10, 5, false},
		{"满 5 轮", 5, 0, 0, true},
		{"新增 4 条事实", 3, 4, 0, true},
		{"连续 3 轮无新收成", 3, 0, 3, true},
		{"普通轮", 4, 1, 0, false},
	}
	for _, c := range cases {
		if got := shouldReview(c.rounds, c.facts, c.ly); got != c.want {
			t.Fatalf("%s: shouldReview(%d,%d,%d)=%v want %v", c.name, c.rounds, c.facts, c.ly, got, c.want)
		}
	}
}

// 深度跟进（动用战果 / 由发现派生）默认排到广度意图前面（7），但显式 priority 优先。
func TestAddIntentDefaultsDepthPriority(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	lootOut := callTool(t, tools.WorkerTools(), "report_foothold",
		`{"risk_area":"other","summary":"web01 shell","access":"shell","privilege":"user",`+
			`"evidence":"id","confidence":"high"}`)
	// report_foothold 的回执是 "foothold recorded: N"；直接从图里取最新一条据点更稳。
	lootNodes, err := ts.ListByKind(db.KindFoothold, 1)
	if err != nil || len(lootNodes) == 0 {
		t.Fatalf("据点没记上：%v %s", err, lootOut)
	}
	lootID := lootNodes[0].ID

	id, _, err := tools.addOneIntent(intentItem{
		Summary: "用 web01 的 shell 提权", RiskArea: "other",
		UsesLootIDs: []json.RawMessage{json.RawMessage(itoaJSON(lootID))},
	}, tools.beginIntentQuota())
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	n, _ := ts.GetNode(id)
	if n.Priority != 7 {
		t.Fatalf("带战果的意图默认优先级 = %d，want 7", n.Priority)
	}

	plain, _, err := tools.addOneIntent(intentItem{Summary: "扫描另一个站点", RiskArea: "other"}, tools.beginIntentQuota())
	if err != nil {
		t.Fatalf("add plain: %v", err)
	}
	if n, _ := ts.GetNode(plain); n.Priority != 5 {
		t.Fatalf("普通意图默认优先级 = %d，want 5", n.Priority)
	}
}

// 同资产同风险类同措辞，但技术或完成口径不同 → 不是重复，放行（深度变体不能被去重挡住）。
func TestDedupAllowsDifferentTechniquesOrKind(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTask("dedup techniques", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	assets := d.Assets()
	assetID, err := assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: "dedup-tech.example"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = assets.DeleteByIDs([]int64{assetID}) })

	ts := d.Exploration(task.ExplorationID)
	tools := NewToolSet(ts, "planner")
	tools.SetTaskID(task.ID)
	mk := func(it intentItem) (int64, error) {
		it.AssetIDs = []json.RawMessage{json.RawMessage(itoaJSON(assetID))}
		id, _, err := tools.addOneIntent(it, tools.beginIntentQuota())
		return id, err
	}
	const base = "探测 /admin 登录口的凭据问题"
	first, err := mk(intentItem{Summary: base, RiskArea: "auth", Techniques: []string{"T1190"}})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// 同措辞、同风险类、同资产，但技术不同 → 放行。
	if _, err := mk(intentItem{Summary: base, RiskArea: "auth", Techniques: []string{"T1552.001"}}); err != nil {
		t.Fatalf("不同技术应放行：%v", err)
	}
	// 技术相同、措辞相近 → 仍拦。
	if _, err := mk(intentItem{Summary: base, RiskArea: "auth", Techniques: []string{"T1190"}}); err == nil {
		t.Fatalf("同技术同措辞应被拦（首条 id=%d）", first)
	}
	// 完成口径不同（verify vs discovery）→ 放行，即使技术相同。
	if _, err := mk(intentItem{Summary: base, RiskArea: "auth", Kind: "verify", Techniques: []string{"T1190"}}); err != nil {
		t.Fatalf("不同完成口径应放行：%v", err)
	}
}

func itoaJSON(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}
