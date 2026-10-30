package server

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// 空跑收笔：正常结束但什么都没写回，必须落 no_output 而不是 done——
// 否则 planner 当成"已覆盖"不再派、覆盖度当成"已测"，整条方向静默消失。
func TestSettleIntentState(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		reason  harness.TerminalReason
		wrote   agent.WriteCounts
		outcome string
		kind    string
		prior   intentPriorOutput
		want    string
	}{
		{"有写回的正常结束 → done", nil, harness.ReasonCompleted, agent.WriteCounts{Facts: 1}, "", "", intentPriorOutput{}, "done"},
		{"只写了资产也算有产出（旧意图口径）", nil, harness.ReasonCompleted, agent.WriteCounts{Assets: 3}, "", "", intentPriorOutput{}, "done"},
		{"什么都没写回 → no_output", nil, harness.ReasonCompleted, agent.WriteCounts{}, "", "", intentPriorOutput{}, "no_output"},
		{"语义 negative 压过空跑（否定结论本身就是产出）", nil, harness.ReasonCompleted, agent.WriteCounts{}, "negative", "", intentPriorOutput{}, "negative"},
		{"语义 out-of-scope 压过空跑", nil, harness.ReasonCompleted, agent.WriteCounts{}, "out-of-scope", "", intentPriorOutput{}, "out-of-scope"},
		{"出错 → blocked（不被空跑覆盖）", errors.New("boom"), harness.ReasonCompleted, agent.WriteCounts{}, "", "", intentPriorOutput{}, "blocked"},
		{"撞步数上限 → exhausted（即使写了点东西）", nil, harness.ReasonMaxTurns, agent.WriteCounts{Facts: 1}, "", "", intentPriorOutput{}, "exhausted"},
		{"超时 → exhausted", nil, harness.ReasonTimeout, agent.WriteCounts{}, "", "", intentPriorOutput{}, "exhausted"},
		{"discovery 意图只登记资产 → done", nil, harness.ReasonCompleted, agent.WriteCounts{Assets: 5}, "", "discovery", intentPriorOutput{}, "done"},
		{"verify 意图只登记资产 → no_output（没验证任何东西）", nil, harness.ReasonCompleted, agent.WriteCounts{Assets: 5}, "", "verify", intentPriorOutput{}, "no_output"},
		{"verify 意图写回了事实 → done", nil, harness.ReasonCompleted, agent.WriteCounts{Assets: 2, Facts: 1}, "", "verify", intentPriorOutput{}, "done"},
		{"verify 意图写回了发现 → done", nil, harness.ReasonCompleted, agent.WriteCounts{Findings: 1}, "", "verify", intentPriorOutput{}, "done"},
		{"verify 意图的否定结论（语义 negative）仍算产出", nil, harness.ReasonCompleted, agent.WriteCounts{Assets: 1}, "negative", "verify", intentPriorOutput{}, "negative"},
		{"verify 意图跑超时 → exhausted 压过完成门", nil, harness.ReasonTimeout, agent.WriteCounts{Assets: 1}, "", "verify", intentPriorOutput{}, "exhausted"},
		// 续跑：本次没写新东西，但前段已经产出过 → 不能判 no_output 抹掉前段成果。
		{"续跑本次无新写回，但历史有产出 → done", nil, harness.ReasonCompleted, agent.WriteCounts{}, "", "", intentPriorOutput{Any: true}, "done"},
		{"续跑仅历史资产锚定（discovery）→ done", nil, harness.ReasonCompleted, agent.WriteCounts{}, "", "discovery", intentPriorOutput{Any: true}, "done"},
		{"verify 续跑无新验证产出，历史也只有资产 → no_output", nil, harness.ReasonCompleted, agent.WriteCounts{Assets: 1}, "", "verify", intentPriorOutput{Any: true}, "no_output"},
		{"verify 续跑无新验证产出，但历史有验证产出 → done", nil, harness.ReasonCompleted, agent.WriteCounts{}, "", "verify", intentPriorOutput{Any: true, Validation: true}, "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := settleIntentState(tc.err, tc.reason, tc.wrote, tc.outcome, tc.kind, tc.prior); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestIntentKindOf(t *testing.T) {
	cases := []struct {
		payload string
		want    string
	}{
		{`{"summary":"x","intent_kind":"verify"}`, "verify"},
		{`{"summary":"x","intent_kind":" discovery "}`, "discovery"},
		{`{"summary":"x"}`, ""},
		{``, ""},
		{`not-json`, ""},
	}
	for _, tc := range cases {
		var n *db.Node
		if tc.payload != "" {
			n = &db.Node{Payload: []byte(tc.payload)}
		}
		if got := intentKindOf(n); got != tc.want {
			t.Fatalf("intentKindOf(%q) = %q, want %q", tc.payload, got, tc.want)
		}
	}
}

// verify 意图收尾时把真实验证过的资产写进 payload；非终态或非 verify 不写。
func TestRecordValidatedAssets(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	task, err := s.m.CreateTask("validated-assets", "g", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var expID int64
	if err := s.m.pg.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task.ID).Scan(&expID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = s.m.pg.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	})
	pgTask, ok := s.m.Task(task.ID)
	if !ok {
		t.Fatal("task handle missing")
	}
	st := pgTask.Store
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	a1, err := s.m.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://one.validated-test.example", TaskID: tid})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.m.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://two.validated-test.example", TaskID: tid})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.m.pg.Exec(`DELETE FROM assets WHERE url LIKE '%validated-test.example%' OR domain LIKE '%validated-test.example%'`)
	})
	intentID, err := st.AddIntent(map[string]any{"summary": "v", "risk_area": "auth", "intent_kind": "verify", "asset_ids": []int64{a1, a2}}, 5, []int64{a1, a2}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := st.GetNode(intentID)
	if err != nil || intent == nil {
		t.Fatal(err)
	}
	wrote := agent.WriteCounts{Facts: 1, ValidationAssets: map[int64]bool{a1: true}}
	recordValidatedAssets(pgTask, intent, "verify", "done", wrote)
	reloaded, err := st.GetNode(intentID)
	if err != nil || reloaded == nil {
		t.Fatal(err)
	}
	var p struct {
		Validated []int64 `json:"validated_asset_ids"`
	}
	if err := json.Unmarshal(reloaded.Payload, &p); err != nil || len(p.Validated) != 1 || p.Validated[0] != a1 {
		t.Fatalf("validated_asset_ids = %v (err %v), payload=%s", p.Validated, err, reloaded.Payload)
	}

	// 非终态不写。
	recordValidatedAssets(pgTask, intent, "verify", "blocked", agent.WriteCounts{ValidationAssets: map[int64]bool{a2: true}})
	reloaded, _ = st.GetNode(intentID)
	_ = json.Unmarshal(reloaded.Payload, &p)
	if len(p.Validated) != 1 {
		t.Fatalf("blocked intent must not record validated assets: %v", p.Validated)
	}
}
