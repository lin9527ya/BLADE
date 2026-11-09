package db

import (
	"context"
	"strings"
	"testing"
)

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
