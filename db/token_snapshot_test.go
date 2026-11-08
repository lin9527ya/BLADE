package db

import (
	"context"
	"reflect"
	"testing"
)

func TestTokenSnapshotCompletedAndLive(t *testing.T) {
	_, st := auditFixture(t)
	appendFrame := func(worker, kind string, slot *int, i, o, cr, cw int) int64 {
		t.Helper()
		id, err := st.AppendActivity(Activity{Worker: worker, Kind: kind, SlotNo: slot,
			InputTokens: &i, OutputTokens: &o, CacheReadTokens: &cr, CacheWriteTokens: &cw})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	appendFrame("planner", "usage", nil, 100, 10, 80, 0)
	lastResult := appendFrame("planner", "result", nil, 100, 10, 80, 0)
	liveSeq := appendFrame("planner", "usage", nil, 50, 5, 20, 3)
	slot := 1
	appendFrame("work#1", "usage", &slot, 40, 4, 10, 0)
	slotLive := appendFrame("work#1", "usage", &slot, 70, 7, 30, 0)
	cursor, err := st.AppendActivity(Activity{Kind: "thinking", Worker: "planner", Summary: "non-accounting row"})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := st.TokenSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.SnapshotCursor != cursor {
		t.Fatalf("cursor=%d want %d", snap.SnapshotCursor, cursor)
	}
	if snap.Total != (TokenCounts{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 80}) {
		t.Fatalf("completed total: %+v", snap.Total)
	}
	bySession := map[string]SessionTokenSnapshot{}
	for _, s := range snap.Sessions {
		bySession[s.Session] = s
	}
	plan := bySession["plan"]
	if plan.LastResultSeq != lastResult || plan.Live == nil || plan.Live.Seq != liveSeq || plan.Live.InputTokens != 50 {
		t.Fatalf("plan: %+v", plan)
	}
	cold := bySession["slot:1"]
	if cold.Live == nil || cold.Live.Seq != slotLive || cold.Live.InputTokens != 70 || cold.InputTokens != 0 {
		t.Fatalf("cold session: %+v", cold)
	}
	if len(snap.Workers) != 1 || snap.Workers[0].InputTokens != 100 {
		t.Fatalf("workers: %+v", snap.Workers)
	}
	// result 落库后，刚才的累计 usage 不再是 live；只保留完成总计。
	appendFrame("work#1", "result", &slot, 70, 7, 30, 0)
	snap, err = st.TokenSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Sessions {
		if s.Session == "slot:1" && (s.Live != nil || s.InputTokens != 70) {
			t.Fatalf("settled slot: %+v", s)
		}
	}
	if snap.Total.InputTokens != 170 {
		t.Fatalf("settled total: %+v", snap.Total)
	}
}

func TestTokenSnapshotEmptyAndLegacySession(t *testing.T) {
	_, st := auditFixture(t)
	snap, err := st.TokenSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Workers) != 0 || len(snap.Sessions) != 0 || snap.Total != (TokenCounts{}) {
		t.Fatalf("empty snapshot: %+v", snap)
	}
	intent, err := st.AddIntent(map[string]any{"summary": "legacy"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	for _, worker := range []string{"work#1", "work#2"} {
		i, o, cr := 100, 5, 70
		if _, err := st.AppendActivity(Activity{Worker: worker, Kind: "result", NodeID: &intent, InputTokens: &i, OutputTokens: &o, CacheReadTokens: &cr}); err != nil {
			t.Fatal(err)
		}
	}
	snap, err = st.TokenSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := st.TokenStatsBySession()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Sessions) != 1 || !reflect.DeepEqual(snap.Sessions[0].SessionTokenUsage, sessions[0]) {
		t.Fatalf("legacy totals mismatch: %+v / %+v", snap.Sessions, sessions)
	}
	if len(snap.Workers) != 2 || snap.Total.InputTokens != 200 {
		t.Fatalf("worker reassignment: %+v", snap)
	}
}
