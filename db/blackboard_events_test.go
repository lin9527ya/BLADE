package db

import (
	"context"
	"strings"
	"testing"
)

func TestBlackboardJournalForwardPages(t *testing.T) {
	d, st := intentLeaseFixture(t)
	base, err := st.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := st.AddNode(KindFact, map[string]any{"summary": "observation"}, 0, "confirmed", "test", nil); err != nil {
			t.Fatal(err)
		}
	}
	latest := base
	latest.Revision += 3
	pageEnd := base
	cursor := base.Cursor()
	for _, want := range []struct {
		total int64
		rows  int
		state string
	}{{3, 2, "truncated"}, {1, 1, "complete"}, {0, 0, "complete"}} {
		page, err := st.BlackboardChanges(t.Context(), cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if page.Resync || page.Revision != latest.RevisionString() || len(page.Changes) != want.rows ||
			page.Status.Total == nil || *page.Status.Total != want.total || page.Status.SampleCount != want.rows ||
			page.Status.State != want.state || page.Status.HasMore != (want.total > int64(want.rows)) {
			t.Fatalf("unexpected page after %s: %+v", cursor, page)
		}
		for _, change := range page.Changes {
			pageEnd.Revision++
			if change.Sequence != pageEnd.RevisionString() {
				t.Fatalf("got sequence %s, want %d", change.Sequence, pageEnd.Revision)
			}
		}
		if page.Cursor != pageEnd.Cursor() {
			t.Fatalf("got cursor %s, want %s", page.Cursor, pageEnd.Cursor())
		}
		cursor = page.Cursor
	}

	// Both ends of the range still exist. A gap past the requested page
	// must trigger a resync even when a single page appears contiguous.
	if _, err := d.Exec(`DELETE FROM blackboard_events WHERE exploration_id=$1 AND seq=$2`, st.ID(), base.Revision+2); err != nil {
		t.Fatal(err)
	}
	page, err := st.BlackboardChanges(t.Context(), base.Cursor(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Resync || page.Cursor != latest.Cursor() || page.Revision != latest.RevisionString() ||
		page.Status.Total == nil || *page.Status.Total != 2 || page.Status.SampleCount != 1 || page.Status.State != "truncated" ||
		len(page.Changes) != 1 || page.Changes[0].Sequence != latest.RevisionString() {
		t.Fatalf("unexpected resync page: %+v", page)
	}
}

func TestBlackboardChangesFailedReadIsUnknown(t *testing.T) {
	_, st := intentLeaseFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	page, err := st.BlackboardChanges(ctx, "", 1)
	if err == nil || page.Status.State != "unknown" || page.Status.Total != nil {
		t.Fatalf("failed snapshot: status=%+v err=%v", page.Status, err)
	}
}

func TestBlackboardJournalTracksUpdatesAndDeletes(t *testing.T) {
	d, st := intentLeaseFixture(t)
	id, err := st.AddIntent(map[string]any{"summary": "PRIVATE_PAYLOAD"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	version, err := st.BlackboardVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeState(id, "blocked"); err != nil {
		t.Fatal(err)
	}
	changes, err := st.BlackboardChanges(context.Background(), version.Cursor(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Changes) != 1 || changes.Changes[0].After != "blocked" || changes.Revision == "0" {
		t.Fatalf("update missing: %+v", changes)
	}
	before := changes.Cursor
	if _, err := d.Exec(`DELETE FROM exploration_nodes WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	changes, err = st.BlackboardChanges(context.Background(), before, 100)
	if err != nil || len(changes.Changes) != 1 || changes.Changes[0].Operation != "delete" {
		t.Fatalf("delete missing: %+v %v", changes, err)
	}
	if strings.Contains(changes.Changes[0].ObjectKey, "PRIVATE") {
		t.Fatal("journal stores payload")
	}
}

func TestBlackboardJournalRollbackAndSealedTransaction(t *testing.T) {
	d, st := intentLeaseFixture(t)
	v, _ := st.BlackboardVersion(context.Background())
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state) VALUES($1,'fact','{}','confirmed')`, st.ID()); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	after, _ := st.BlackboardVersion(context.Background())
	if after.Revision != v.Revision {
		t.Fatal("rollback published change")
	}
	tx, err = d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state) VALUES($1,'fact','{}','confirmed')`, st.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SET CONSTRAINTS trg_blackboard_flush IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state) VALUES($1,'fact','{}','confirmed')`, st.ID()); err == nil {
		t.Fatal("writes after publishing allowed")
	}
}

func TestBlackboardReadSnapshotConsistent(t *testing.T) {
	d, st := intentLeaseFixture(t)
	id, err := st.AddIntent(map[string]any{"summary": "snapshot"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ReadSnapshot(context.Background(), func(view *DB) error {
		before, err := view.Exploration(st.ID()).GetNode(id)
		if err != nil {
			return err
		}
		if err := st.SetNodeState(id, "blocked"); err != nil {
			return err
		}
		after, err := view.Exploration(st.ID()).GetNode(id)
		if err != nil {
			return err
		}
		if before.State != after.State {
			t.Fatal("read escaped snapshot")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.ReadSnapshot(context.Background(), func(view *DB) error {
		_, err := view.Exec(`UPDATE exploration_nodes SET state='open' WHERE id=$1`, id)
		return err
	}); err == nil {
		t.Fatal("snapshot permits writes")
	}
}
