package server

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 报告正文只能算「已确认」：medium 候选即使存在，也只能进「待确认候选」段，
// 不能出现在确认发现计数/正文标题里。
func TestReportSeparatesUnconfirmed(t *testing.T) {
	s, f, request := trafficEvidenceServer(t)
	full, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || full.TaskID == nil {
		t.Fatalf("fixture finding: %+v %v", full, err)
	}
	taskID := *full.TaskID
	var expID int64
	if err := s.m.pg.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, taskID).Scan(&expID); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.evidenceStore().Record(context.Background(), db.RecordFindingInput{
		TaskID: taskID, ExplorationID: expID, Worker: "test",
		VulnClass: "TEST", Name: "unconfirmed-candidate", Severity: "low",
		Summary: "仅观察到异常，未复现", Confidence: db.ConfidenceMedium,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	taskIDText := strconv.FormatInt(taskID, 10)
	if _, ok := s.m.Task(taskIDText); !ok {
		t.Fatal("task not found")
	}
	w := request("GET", "/api/report?task="+taskIDText, "")
	if w.Code != 200 {
		t.Fatalf("report: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, "## 待确认候选") || !strings.Contains(body, "unconfirmed-candidate") {
		t.Fatalf("unconfirmed finding missing from report: %s", body)
	}
	confirmed := strings.Index(body, "## 发现")
	pending := strings.Index(body, "## 待确认候选")
	if confirmed < 0 || pending < 0 || pending < confirmed {
		t.Fatalf("section order: %s", body)
	}
	// 该候选不能出现在正文发现标题里（### n. [..] 名称）。
	head := body[confirmed:pending]
	if strings.Contains(head, "unconfirmed-candidate") {
		t.Fatalf("unconfirmed finding leaked into confirmed body: %s", head)
	}
	_ = candidate
}
