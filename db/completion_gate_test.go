package db

import (
	"fmt"
	"testing"
)

// 收官审计要能报出四类缺口，并能被豁免逐类放行。
func TestCompletionGapsAndWaivers(t *testing.T) {
	d, st := auditFixture(t)
	// 未闭合候选
	if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id, kind, state, payload)
VALUES ($1,'finding','open_proof_gap','{"summary":"疑似 SSRF"}'::jsonb)`, st.expID); err != nil {
		t.Fatal(err)
	}
	// 未动用据点
	if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id, kind, state, payload)
VALUES ($1,'foothold','active','{"summary":"web01 shell","access":"shell","privilege":"user"}'::jsonb)`, st.expID); err != nil {
		t.Fatal(err)
	}
	// 未处理线索
	var factID int64
	if err := d.QueryRow(`INSERT INTO exploration_nodes(exploration_id, kind, state, payload)
VALUES ($1,'fact','confirmed','{"summary":"观察","leads":[{"text":"看看 /admin"}]}'::jsonb) RETURNING id`, st.expID).Scan(&factID); err != nil {
		t.Fatal(err)
	}

	gaps, err := st.CompletionGaps(nil, CompletionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, g := range gaps {
		kinds[g.Kind] = true
	}
	for _, want := range []string{GapProofGap, GapChain, GapLead} {
		if !kinds[want] {
			t.Fatalf("缺少缺口 %s：%+v", want, gaps)
		}
	}

	// 逐类豁免后缺口消失。
	for _, scope := range []string{"proof_gaps", "chains", "leads"} {
		if _, err := st.AddCoverageWaiver(scope, nil, "", "本轮明确接受的静态风险"); err != nil {
			t.Fatalf("waive %s: %v", scope, err)
		}
	}
	gaps, err = st.CompletionGaps(nil, CompletionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("豁免后不该还有缺口：%+v", gaps)
	}

	// 豁免只影响审计口径，不改图：线索本身仍是 open，直到被转换/驳回。
	if n, err := st.OpenLeadsCount(); err != nil || n != 1 {
		t.Fatalf("豁免不应改变 leads 原始计数：n=%d err=%v", n, err)
	}
	_ = factID
}

// 线索口径：converted_lead_ids 与 dismissed_leads 都要认；否则收官门会永远拦着。
func TestOpenLeadsCountHonorsConversionAndDismissal(t *testing.T) {
	d, st := auditFixture(t)
	mkFact := func(payload string) int64 {
		t.Helper()
		var id int64
		if err := d.QueryRow(`INSERT INTO exploration_nodes(exploration_id, kind, state, payload)
VALUES ($1,'fact','confirmed',$2::jsonb) RETURNING id`, st.expID, payload).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	f1 := mkFact(`{"summary":"a","leads":[{"text":"one"},{"text":"two"}]}`)
	f2 := mkFact(`{"summary":"b","leads":[{"text":"three"}],"dismissed_leads":["L` + fmt.Sprint(f2Dummy) + `-0"]}`)
	_ = f1
	_ = f2

	// 初始：f1 两条 + f2 一条（f2 的 dismissed 指向不存在的 id，故意不生效）
	if n, err := st.OpenLeadsCount(); err != nil || n != 3 {
		t.Fatalf("open leads = %d err=%v，want 3", n, err)
	}
	// 转掉 f1 的第 0 条
	if _, err := st.AddIntent(map[string]any{"summary": "接手线索", "converted_lead_ids": []string{fmt.Sprintf("L%d-0", f1)}}, 5, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.OpenLeadsCount(); n != 2 {
		t.Fatalf("converted 后 open leads = %d，want 2", n)
	}
	// 驳回 f2 的第 0 条
	if _, err := d.Exec(`UPDATE exploration_nodes SET payload = jsonb_set(payload,'{dismissed_leads}','["L`+fmt.Sprint(f2)+`-0"]'::jsonb) WHERE id=$1`, f2); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.OpenLeadsCount(); n != 1 {
		t.Fatalf("dismissed 后 open leads = %d，want 1", n)
	}
}

// f2Dummy 只是为了让第一条 SQL 里的 dismissed id 指向一个不存在的线索。
const f2Dummy = 999999
