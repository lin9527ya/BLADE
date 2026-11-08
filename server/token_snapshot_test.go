package server

import (
	"encoding/json"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestTokenStatsSnapshotIncludesColdUsage(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	task := s.m.ResolveTask("")
	if task == nil {
		t.Fatal("fixture task missing")
	}
	slot := 1
	i, o, cr := 100, 5, 80
	seq, err := task.Store.AppendActivity(db.Activity{Worker: "work#1", SlotNo: &slot, Kind: "usage", InputTokens: &i, OutputTokens: &o, CacheReadTokens: &cr})
	if err != nil {
		t.Fatal(err)
	}
	w := request("GET", "/api/exploration/tokens?task="+task.ID, "")
	if w.Code != 200 {
		t.Fatalf("snapshot: %d %s", w.Code, w.Body)
	}
	var snapshot db.TokenSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SnapshotCursor < seq || snapshot.Total.InputTokens != 0 {
		t.Fatalf("baseline: %+v", snapshot)
	}
	found := false
	for _, session := range snapshot.Sessions {
		if session.Session == "slot:1" {
			found = true
			if session.Live == nil || session.Live.Seq != seq || session.Live.InputTokens != 100 {
				t.Fatalf("live usage: %+v", session)
			}
		}
	}
	if !found {
		t.Fatal("cold worker omitted")
	}
	_, err = task.Store.AppendActivity(db.Activity{Worker: "work#1", SlotNo: &slot, Kind: "result", InputTokens: &i, OutputTokens: &o, CacheReadTokens: &cr})
	if err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/api/exploration/tokens?task="+task.ID, "")
	if w.Code != 200 {
		t.Fatalf("settled: %d %s", w.Code, w.Body)
	}
	snapshot = db.TokenSnapshot{}
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Total.InputTokens != 100 || snapshot.Sessions[0].Live != nil {
		t.Fatalf("result counted twice: %+v", snapshot)
	}
}
