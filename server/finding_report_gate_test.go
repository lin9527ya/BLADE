package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// TestFindingConfirmedRules 钉住"能不能写报告"的真值表。判据是三类硬证据
// （high / 处置已确认 / 复测 reproduced|fixed），并且**处置结论优先**：
// 已撤回、误报、已排除、重复的漏洞即使自评 high 也不写。
func TestFindingConfirmedRules(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		confidence, status, verdict string
		want                        bool
	}{
		{"high", db.ConfidenceHigh, db.FindingPending, "", true},
		{"medium", db.ConfidenceMedium, db.FindingPending, "", false},
		{"low", db.ConfidenceLow, db.FindingPending, "", false},
		{"unlabeled", "", db.FindingPending, "", false},
		{"triaged-confirmed", db.ConfidenceMedium, db.FindingConfirmed, "", true},
		{"retest-reproduced", db.ConfidenceMedium, db.FindingPending, "reproduced", true},
		{"retest-fixed", db.ConfidenceLow, db.FindingPending, "fixed", true},
		{"retest-inconclusive", db.ConfidenceMedium, db.FindingPending, "inconclusive", false},
		{"retracted-overrides-high", db.ConfidenceHigh, db.FindingRetracted, "", false},
		{"false-positive-overrides-high", db.ConfidenceHigh, db.FindingFalsePositive, "", false},
		{"ruled-out-overrides-high", db.ConfidenceHigh, db.FindingRuledOut, "reproduced", false},
		{"duplicate-overrides-high", db.ConfidenceHigh, db.FindingDuplicate, "", false},
		{"open-proof-gap-overrides-high", db.ConfidenceHigh, db.FindingOpenProofGap, "", false},
	} {
		if got := findingConfirmed(tc.confidence, tc.status, tc.verdict); got != tc.want {
			t.Errorf("%s: findingConfirmed(%q,%q,%q)=%v, want %v", tc.name, tc.confidence, tc.status, tc.verdict, got, tc.want)
		}
	}
}

// TestUpdateFindingReportRefusesUnconfirmed 端到端：medium 的候选写不进报告；
// 人工把处置状态标为「已确认」后同一份报告才被接收。
func TestUpdateFindingReportRefusesUnconfirmed(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t)
	full, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || full.TaskID == nil {
		t.Fatalf("fixture finding has no task: %+v %v", full, err)
	}
	taskID := *full.TaskID
	var expID int64
	if err := s.m.pg.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, taskID).Scan(&expID); err != nil {
		t.Fatal(err)
	}
	medium, err := s.evidenceStore().Record(context.Background(), db.RecordFindingInput{
		TaskID: taskID, ExplorationID: expID, Worker: "test",
		VulnClass: "TEST", Name: "unconfirmed", Severity: "low", Summary: "仅观察到异常，未确证",
		Confidence: db.ConfidenceMedium,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	tool := s.toolUpdateFindingReport()
	call := func() (string, bool) {
		t.Helper()
		res, err := tool.Call(context.Background(), json.RawMessage(fmt.Sprintf(
			`{"finding_id":%d,"report":"## 报告\n\n这份报告不该在未确认时写进去"}`, medium.NodeID)), nil)
		if err != nil {
			t.Fatalf("tool returned Go error: %v", err)
		}
		return res.Flatten(), res.IsError
	}

	if out, isErr := call(); !isErr {
		t.Fatalf("未确认（medium）的候选写进了报告：%s", out)
	}
	saved, err := s.m.pg.GetFinding(medium.FindingID)
	if err != nil || saved.Report != "" {
		t.Fatalf("被拒后报告仍被写入：%+v %v", saved, err)
	}

	if _, err := s.m.pg.SetFindingStatus(medium.FindingID, db.FindingConfirmed); err != nil {
		t.Fatal(err)
	}
	if out, isErr := call(); isErr {
		t.Fatalf("处置标为已确认后仍被拒：%s", out)
	}
	saved, err = s.m.pg.GetFinding(medium.FindingID)
	if err != nil || saved.Report == "" {
		t.Fatalf("确认后报告没写进去：%+v %v", saved, err)
	}
}

// TestUpdateFindingReportRefusesRetracted：已撤回的漏洞即使 confidence=high 也不写报告——
// 处置结论（复测推翻）优先于上报时的自评。
func TestUpdateFindingReportRefusesRetracted(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t) // fixture: confidence=high
	if _, err := s.m.pg.SetFindingStatus(f.FindingID, db.FindingRetracted); err != nil {
		t.Fatal(err)
	}
	res, err := s.toolUpdateFindingReport().Call(context.Background(), json.RawMessage(fmt.Sprintf(
		`{"finding_id":%d,"report":"## 报告\n\n已撤回的漏洞不该有报告"}`, f.NodeID)), nil)
	if err != nil || !res.IsError {
		t.Fatalf("已撤回的漏洞仍能写报告：%v %s", err, res.Flatten())
	}
}
