package db

import (
	"testing"
	"time"
)

func TestActivityIDsFollowCommitOrder(t *testing.T) {
	d, st := auditFixture(t)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := st.lockActivityOrder(tx); err != nil {
		t.Fatal(err)
	}
	var first int64
	if err := tx.QueryRow(`INSERT INTO activity(exploration_id,worker,kind) VALUES ($1,'first','text') RETURNING id`, st.expID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	type result struct {
		id  int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		id, err := st.AppendActivity(Activity{Worker: "second", Kind: "text"})
		done <- result{id, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("later insert committed before first transaction: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.id <= first {
			t.Fatalf("id order: first=%d second=%d", first, r.id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("activity write did not resume after commit")
	}
}
