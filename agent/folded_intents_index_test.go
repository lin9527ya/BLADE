package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 折叠进冷摘要的已结束意图必须能在态势里被"看见"：只给 digest id 时 planner 要逐个
// expand_digest 才能判断"这个方向是不是试过"（实测任务 6：82 条里 31 条只剩 id）。
func TestFoldedIntentsIndexVisible(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	// 造一批已结束意图并折叠进一个 digest
	digest, err := ts.AddNode(db.KindDigest, map[string]any{"body": "这一片探索的结论"}, 0, "active", "system", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		id, err := ts.AddIntent(map[string]any{
			"summary": fmt.Sprintf("历史方向 %d：测试 /admin 的 SQL 注入，结论是参数化查询已修复", i), "risk_area": "sqli",
		}, 5, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := ts.SetIntentState(id, "done"); err != nil {
			t.Fatal(err)
		}
		if err := ts.Link(digest, db.RelCovers, id); err != nil {
			t.Fatal(err)
		}
	}
	data := tools.graphOverviewData()
	raw, _ := json.Marshal(data["folded_intents"])
	var folded []map[string]any
	if err := json.Unmarshal(raw, &folded); err != nil {
		t.Fatalf("folded_intents 不是数组：%v", err)
	}
	if len(folded) != 5 {
		t.Fatalf("应列出 5 条折叠意图，got %d", len(folded))
	}
	for _, f := range folded {
		if _, ok := f["id"]; !ok {
			t.Fatal("缺少 id")
		}
		s, _ := f["s"].(string)
		if s == "" || !strings.Contains(s, "历史方向") {
			t.Fatalf("摘要缺失：%v", f)
		}
		if len([]rune(s)) > foldedIntentSummaryMax+1 {
			t.Fatalf("摘要应截断到 %d 字，got %d", foldedIntentSummaryMax, len([]rune(s)))
		}
		if f["state"] != "done" {
			t.Fatalf("应带终态，got %v", f["state"])
		}
	}
	if total, _ := data["folded_intents_total"].(int); total != 5 {
		t.Fatalf("total 应为 5，got %v", data["folded_intents_total"])
	}
	// 折叠成员不应同时出现在 recent_done_intents（避免重复计数）。
	recent, _ := data["recent_done_intents"].([]map[string]any)
	for _, r := range recent {
		if strings.Contains(fmt.Sprint(r["summary"]), "历史方向") {
			t.Fatalf("折叠成员不应出现在 recent_done_intents：%v", r)
		}
	}
}

// 硬上限：超过 100 条只给前 100，total 仍是真实总数（模型据此知道还有多少没显示）。
func TestFoldedIntentsIndexCap(t *testing.T) {
	if foldedIntentsCap != 100 {
		t.Fatalf("硬上限应为 100，got %d", foldedIntentsCap)
	}
	tools, ts, _ := newToolWorker(t)
	digest, err := ts.AddNode(db.KindDigest, map[string]any{"body": "x"}, 0, "active", "system", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		id, err := ts.AddIntent(map[string]any{"summary": fmt.Sprintf("方向 %03d", i), "risk_area": "other"}, 5, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := ts.SetIntentState(id, "no_output"); err != nil {
			t.Fatal(err)
		}
		if err := ts.Link(digest, db.RelCovers, id); err != nil {
			t.Fatal(err)
		}
	}
	data := tools.graphOverviewData()
	folded, _ := data["folded_intents"].([]map[string]any)
	if len(folded) != foldedIntentsCap {
		t.Fatalf("应截断到 %d 条，got %d", foldedIntentsCap, len(folded))
	}
	if total, _ := data["folded_intents_total"].(int); total != 105 {
		t.Fatalf("total 应为真实总数 105，got %v", data["folded_intents_total"])
	}
}
