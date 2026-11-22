package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// report_finding 默认写 findings.status=pending，候选闭合写 open_proof_gap。
// 播报板/任务时间线默认必须能看到这些发现（用状态徽标区分待处理/待补证），
// 否则刚发现的漏洞会静默消失，直到人工确认。
func TestBroadcastShowsFindingsByDefault(t *testing.T) {
	s, recorded, request := trafficEvidenceServer(t)
	finding, err := s.m.pg.GetFinding(recorded.FindingID)
	if err != nil || finding == nil || finding.TaskID == nil {
		t.Fatalf("finding=%+v err=%v", finding, err)
	}
	// 候选闭合（open_proof_gap）也属于当前发现；默认播报不应静默丢掉它，
	// 状态徽标会把它标成「存疑待补证」。
	var expID int64
	if err := s.m.pg.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, *finding.TaskID).Scan(&expID); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.m.pg.Exploration(expID).RecordCandidateClosure(context.Background(), db.CandidateClosureInput{
		Worker: "test", VulnClass: "TEST", Name: "candidate in broadcast", Severity: "low",
		Summary: "candidate closure fixture", Outcome: db.FindingOpenProofGap, Confidence: db.ConfidenceLow,
		ProofGap: "needs one more verification step",
	})
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		nodeID int64
		status string
	}{
		{recorded.NodeID, "pending"},
		{candidate.NodeID, "open_proof_gap"},
	}
	for _, check := range checks {
		id := fmt.Sprint(check.nodeID)
		paths := []string{
			"/api/broadcast?size=100&q=" + id,
			fmt.Sprintf("/api/exploration/nodes?task=%d&size=100&q=%s", *finding.TaskID, id),
		}
		verifyBroadcastFinding(t, request, paths, id, check.status)
	}
}

func verifyBroadcastFinding(t *testing.T, request func(string, string, string) *httptest.ResponseRecorder, paths []string, id, wantStatus string) {
	t.Helper()
	for _, path := range paths {
		w := request("GET", path, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var got struct {
			Items []struct {
				ID            string `json:"id"`
				Type          string `json:"type"`
				State         string `json:"state"`
				FindingStatus string `json:"finding_status"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range got.Items {
			if item.ID == id && item.Type == "finding" {
				found = true
				if item.FindingStatus != wantStatus {
					t.Fatalf("%s: finding_status=%q want %q: %+v", path, item.FindingStatus, wantStatus, item)
				}
			}
		}
		if !found {
			t.Fatalf("%s: finding %s missing: %+v", path, id, got.Items)
		}
	}
}
