package db

import "testing"

// 续跑结算依赖「这条意图以前产出过什么」：图产出（yields/leaves）算 any，
// 其中验证级节点才算 validation；仅锚定资产也只算 any（discovery 口径）。
func TestIntentPriorOutput(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "prior output")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)
	as := d.Assets()

	intent, err := es.AddIntent(map[string]any{"summary": "verify idor"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if anyOut, validation, err := es.IntentPriorOutput(intent); err != nil || anyOut || validation {
		t.Fatalf("新意图不应有历史产出: any=%v validation=%v err=%v", anyOut, validation, err)
	}

	// 只锚定资产：any=true, validation=false（资产登记不是验证结论）。
	assetID, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: "prior-output.example", TaskID: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := es.Anchor(intent, assetID); err != nil {
		t.Fatal(err)
	}
	anyOut, validation, err := es.IntentPriorOutput(intent)
	if err != nil || !anyOut || validation {
		t.Fatalf("仅资产锚定: any=%v validation=%v err=%v, want true/false", anyOut, validation, err)
	}

	// 产出一条事实：validation 也成立。
	fact, err := es.AddNode(KindFact, map[string]any{"summary": "参数 user_id 可越权"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := es.Link(intent, "yields", fact); err != nil {
		t.Fatal(err)
	}
	anyOut, validation, err = es.IntentPriorOutput(intent)
	if err != nil || !anyOut || !validation {
		t.Fatalf("事实产出: any=%v validation=%v err=%v, want true/true", anyOut, validation, err)
	}
}
