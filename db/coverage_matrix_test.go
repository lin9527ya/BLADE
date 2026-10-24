package db

import (
	"sort"
	"testing"
)

// coverageMatrixFixture builds one task with an explicit root-domain scope and
// its exploration. Cleanup removes the exploration, task and scope wholesale.
func coverageMatrixFixture(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	task, err := d.CreateTask("coverage-matrix-test", "matrix goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var expID int64
	if err := d.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task.ID).Scan(&expID); err != nil {
		t.Fatalf("exploration for task: %v", err)
	}
	// 这些资产用的是**固定域名**（matrix-test.example），残留会让下一轮的计数断言偏大；
	// 而删 exploration/task 不会带走 assets。所以清理里要显式删资产，入口处也要先清一次
	// ——只加清理无法自愈已经存在的污染。
	clearMatrixAssets := func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE domain LIKE '%matrix-test.example%' OR url LIKE '%matrix-test.example%'`)
	}
	t.Cleanup(func() {
		clearMatrixAssets()
		// **先删任务、再删 exploration**：tasks.exploration_id 是 ON DELETE RESTRICT，
		// 反过来会被外键拦下，而这里的错误一直是 `_` 忽略的——于是 exploration 从来没被删掉，
		// 节点（含 intent 状态）一直残留，污染后续用例（no_output 残留就是这么把迁移用例
		// 的老约束降级撞死的）。
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
		d.Close()
	})
	clearMatrixAssets()
	if _, err := d.Assets().AddAgentScope(task.ID, "root_domain", "matrix-test.example", "test scope", "agent"); err != nil {
		t.Fatal(err)
	}
	return d, task.ID, expID
}

// TestCoverageMatrixOutcomesAndEvidence derives the per-asset risk-area matrix
// from anchored intents: outcome buckets per state — 已测(done/negative) /
// 未测成(blocked/exhausted) / 进行中 — plus fact evidence per asset; a missing
// risk_area folds to "other".
func TestCoverageMatrixOutcomesAndEvidence(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	as := d.Assets()
	a1, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "api.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "www.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	st := d.Exploration(expID)

	tested, err := st.AddIntent(map[string]any{"summary": "sqli on api", "risk_area": "sqli"}, 5, []int64{a1}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetIntentState(tested, "negative"); err != nil {
		t.Fatal(err)
	}
	failed, err := st.AddIntent(map[string]any{"summary": "ssrf on api", "risk_area": "ssrf"}, 5, []int64{a1}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetIntentState(failed, "blocked"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddIntent(map[string]any{"summary": "xss on www"}, 5, []int64{a2}, "planner"); err != nil {
		t.Fatal(err)
	}

	// One anchored fact as a1's evidence.
	var factID int64
	if err := d.QueryRow(`INSERT INTO exploration_nodes(exploration_id, kind, state, payload, priority)
		VALUES ($1,'fact','confirmed','{}'::jsonb,0) RETURNING id`, expID).Scan(&factID); err != nil {
		t.Fatal(err)
	}
	if err := st.Anchor(factID, a1); err != nil {
		t.Fatal(err)
	}

	assets, err := st.CoverageMatrix(taskID)
	if err != nil {
		t.Fatal(err)
	}
	// UpsertSubdomain also upserts the root domain, so the scope (root_domain=matrix-test.example)
	// matches three assets: api… < matrix-test.example < www… (alphabetical).
	if len(assets) != 3 {
		t.Fatalf("want 3 in-scope assets, got %d: %+v", len(assets), assets)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Label < assets[j].Label })
	api, root, www := assets[0], assets[1], assets[2]
	if root.Type != "root_domain" || len(root.Cells) != 0 || root.Facts != 0 {
		t.Fatalf("root asset = %+v, want bare in-scope root", root)
	}
	if api.Facts != 1 {
		t.Fatalf("api facts = %d, want 1", api.Facts)
	}
	if len(api.Cells) != 2 {
		t.Fatalf("api cells = %+v, want 2 (sqli,ssrf)", api.Cells)
	}
	byArea := map[string]CoverageMatrixCell{}
	for _, c := range api.Cells {
		byArea[c.RiskArea] = c
	}
	if c := byArea["sqli"]; c.Tested != 1 || c.Failed != 0 || len(c.Intents) != 1 {
		t.Fatalf("sqli cell = %+v", c)
	}
	if c := byArea["ssrf"]; c.Failed != 1 || c.Tested != 0 {
		t.Fatalf("ssrf cell = %+v", c)
	}
	if len(www.Cells) != 1 || www.Cells[0].RiskArea != "other" || www.Cells[0].InFlight != 1 {
		t.Fatalf("www cells = %+v, want one in-flight 'other'", www.Cells)
	}
	if www.Facts != 0 {
		t.Fatalf("www facts = %d, want 0", www.Facts)
	}
}

// no_output（跑完了却什么都没写回）必须记「未测成」，绝不能进「已测」——
// 它恰恰是"看起来跑过了、其实什么都没测到"的那一类。
func TestCoverageMatrixCountsNoOutputAsFailed(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	as := d.Assets()
	a1, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "api.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	st := d.Exploration(expID)
	id, err := st.AddIntent(map[string]any{"summary": "sqli on api（空跑）", "risk_area": "sqli"}, 5, []int64{a1}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetIntentState(id, "no_output"); err != nil {
		t.Fatal(err)
	}
	assets, err := st.CoverageMatrix(taskID)
	if err != nil {
		t.Fatal(err)
	}
	var cell *CoverageMatrixCell
	for i := range assets {
		for j := range assets[i].Cells {
			if assets[i].Cells[j].RiskArea == "sqli" {
				cell = &assets[i].Cells[j]
			}
		}
	}
	if cell == nil {
		t.Fatalf("找不到 sqli 单元格：%+v", assets)
	}
	if cell.Failed != 1 || cell.Tested != 0 || cell.InFlight != 0 {
		t.Fatalf("no_output 应记未测成，实际 %+v", *cell)
	}
}

// 丢弃/叫停的意图既不是"进行中"也不是"测过"：它们不该在覆盖矩阵里留下永远"在跑"的格子。
func TestCoverageMatrixIgnoresDroppedAndStopped(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	as := d.Assets()
	a1, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "api.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	st := d.Exploration(expID)
	mk := func(summary, state string) {
		id, err := st.AddIntent(map[string]any{"summary": summary, "risk_area": "sqli"}, 5, []int64{a1}, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetIntentState(id, state); err != nil {
			t.Fatal(err)
		}
	}
	mk("被丢弃的方向", "deleted")
	mk("被叫停的方向", "stopped")

	sqliCell := func() *CoverageMatrixCell {
		assets, err := st.CoverageMatrix(taskID)
		if err != nil {
			t.Fatal(err)
		}
		for i := range assets {
			if assets[i].Label != "api.matrix-test.example" {
				continue
			}
			for j := range assets[i].Cells {
				if assets[i].Cells[j].RiskArea == "sqli" {
					return &assets[i].Cells[j]
				}
			}
		}
		return nil
	}
	if cell := sqliCell(); cell != nil {
		t.Fatalf("只有被丢弃/叫停的意图时，不该留下格子（会被读成进行中）：%+v", *cell)
	}

	// 再加一条真有结论的：格子只应计"已测"，前两条不能算进行中。
	mk("测过的方向", "negative")
	cell := sqliCell()
	if cell == nil {
		t.Fatal("有结论的意图应生成格子")
	}
	if cell.Tested != 1 || cell.InFlight != 0 || cell.Failed != 0 {
		t.Fatalf("覆盖计数不对：%+v", *cell)
	}
}
