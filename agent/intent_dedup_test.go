package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 相似度的边界：换标点/换空格不算新方向；机理不同就一定是新方向；太短的描述不判。
func TestSummarySimilarityCatchesParaphrase(t *testing.T) {
	same := summarySimilarity("探测 /admin 后台的 SQL 注入", "探测 admin 后台的 SQL 注入")
	if same < intentDupSimilarity {
		t.Fatalf("只差标点的同一方向相似度应高于阈值：%.2f", same)
	}
	diff := summarySimilarity("探测 /admin 后台的 SQL 注入", "对上传接口做任意文件读取验证")
	if diff >= intentDupSimilarity {
		t.Fatalf("机理不同的方向不该被判为重复：%.2f", diff)
	}
	if summarySimilarity("短描述", "短描述") != 0 {
		t.Fatal("过短的描述不该参与相似度判定")
	}
}

// 防打转：同资产 + 同风险类 + 同义描述 → 拒；换风险类/换资产/换机理 → 放行。
// 发散性是本系统的主要目标，这条防护只能挡"同一句话又说一遍"。
func TestAddIntentBlocksNearDuplicateOfActiveIntent(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("dedup active", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	assets := d.Assets()
	assetID, err := assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: "dedup-active.example"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = assets.DeleteByIDs([]int64{assetID}) })
	otherAsset, err := assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: "dedup-other.example"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = assets.DeleteByIDs([]int64{otherAsset}) })

	ts := d.Exploration(task.ExplorationID)
	tools := NewToolSet(ts, "planner")
	tools.SetTaskID(task.ID)
	mk := func(summary, risk string, assetIDs ...int64) (int64, error) {
		ids := make([]json.RawMessage, 0, len(assetIDs))
		for _, a := range assetIDs {
			b, _ := json.Marshal(a)
			ids = append(ids, b)
		}
		// 每次调用用新的额度快照 —— 与生产里 add_intent 的行为一致。
		id, _, err := tools.addOneIntent(intentItem{Summary: summary, RiskArea: risk, AssetIDs: ids}, tools.beginIntentQuota())
		return id, err
	}

	const base = "探测 /admin 后台登录口的 SQL 注入"
	firstID, err := mk(base, "sqli", assetID)
	if err != nil {
		t.Fatalf("第一条方向应可创建：%v", err)
	}

	// ① 同一方向再说一遍（只差标点与空格）→ 拒，并指出被谁挡住了。
	_, err = mk("探测 admin 后台登录口的 SQL 注入", "sqli", assetID)
	if err == nil {
		t.Fatal("同义重复应被拒")
	}
	if !strings.Contains(err.Error(), "几乎是同一条方向") || !strings.Contains(err.Error(), "intent-") {
		t.Fatalf("拒绝理由应指认已有意图：%v", err)
	}
	_ = firstID

	// ② 换风险类：放行（不同打法）。
	if _, err := mk(base, "xss", assetID); err != nil {
		t.Fatalf("换风险类应放行：%v", err)
	}
	// ③ 换资产：放行（另一个目标上的同一类测试）。
	if _, err := mk(base, "sqli", otherAsset); err != nil {
		t.Fatalf("换资产应放行：%v", err)
	}
	// ④ 同资产同风险类、但机理不同：放行——这条最关键，错了就会压死发散性。
	if _, err := mk("对 /admin 后台做撞库，用常见口令字典验证锁定策略", "auth", assetID); err != nil {
		t.Fatalf("机理不同的方向必须放行：%v", err)
	}
	// ⑤ 没有 asset、风险类为 other（纯想法）：与其它风险类不构成重复。
	if _, err := mk(base, "other", 0); err != nil {
		t.Fatalf("没有结构性锚点的方向不该被拦：%v", err)
	}
}

// 同一方向已经结束过 3 次：第四次必须换打法，否则就是纯粹的消耗。
func TestAddIntentBlocksAfterRepeatedAttempts(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("dedup retry", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	assets := d.Assets()
	assetID, err := assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: "dedup-retry.example"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = assets.DeleteByIDs([]int64{assetID}) })

	ts := d.Exploration(task.ExplorationID)
	tools := NewToolSet(ts, "planner")
	tools.SetTaskID(task.ID)
	mk := func(summary string) (int64, error) {
		b, _ := json.Marshal(assetID)
		id, _, err := tools.addOneIntent(intentItem{Summary: summary, RiskArea: "sqli", AssetIDs: []json.RawMessage{b}}, tools.beginIntentQuota())
		return id, err
	}

	const base = "爆破 /admin 后台登录口的 SQL 注入点"
	var lastID int64
	for i := 0; i < intentDupAttemptCap; i++ {
		id, err := mk(base)
		if err != nil {
			t.Fatalf("第 %d 次尝试不该被拦：%v", i+1, err)
		}
		lastID = id
		if err := ts.SetNodeState(id, "done"); err != nil {
			t.Fatal(err)
		}
	}

	_, err = mk("爆破 admin 后台登录口的 SQL 注入点")
	if err == nil {
		t.Fatalf("第 %d 次同义尝试应被拒", intentDupAttemptCap+1)
	}
	if !strings.Contains(err.Error(), "尝试过") {
		t.Fatalf("拒绝理由应说明尝试次数：%v", err)
	}
	if !strings.Contains(err.Error(), "record_fact") {
		t.Fatalf("拒绝理由应给出下一步（把否定结论写成 fact）：%v", err)
	}
	_ = lastID

	// 换一条真正不同的打法：不能被计数连坐。
	if _, err := mk("枚举 /admin 后台的可疑参数做盲注布尔判定"); err != nil {
		t.Fatalf("不同打法不该被尝试次数连坐：%v", err)
	}
}
