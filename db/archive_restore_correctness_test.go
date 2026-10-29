package db

import (
	"encoding/json"
	"testing"
)

// A running side-question row restored from an archive would collide with the
// partial unique index (session_key WHERE status='running') forever. Restore
// must interrupt it and bump the sequence.
func TestArchiveRestoreInterruptsRunningSideQuestions(t *testing.T) {
	d, s := sideFixture(t)
	e, _, err := d.StartSideRequest(t.Context(), s, "archived-running", "long question")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRow(`SELECT json_agg(t) FROM side_question_requests t WHERE session_key=$1`, s.Parent.Key()).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	// Keep the session row (FK), but remove the request so the archived copy is
	// the only one being inserted.
	if _, err = tx.Exec(`DELETE FROM side_question_requests WHERE session_key=$1`, s.Parent.Key()); err != nil {
		t.Fatal(err)
	}
	if err = insertArchiveRows(tx, "side_question_requests", json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	var status, errMsg string
	var sequence int64
	if err = tx.QueryRow(`SELECT status, error, sequence FROM side_question_requests WHERE id=$1`, e.ID).
		Scan(&status, &errMsg, &sequence); err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" {
		t.Fatalf("running row restored as %q, want interrupted", status)
	}
	if errMsg == "" {
		t.Fatal("interrupted row must explain why")
	}
	if sequence < 1 {
		t.Fatalf("sequence must advance so the session can accept a new question, got %d", sequence)
	}
}

// rowExists must surface database errors instead of reporting "row missing":
// the caller nulls out references on false.
func TestRowExistsPropagatesErrors(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if _, err := rowExists(tx, "task_categories", 1); err != nil {
		t.Fatalf("valid lookup should not error: %v", err)
	}
	if _, err := rowExists(tx, "not_a_table", 1); err == nil {
		t.Fatal("unsupported table must error, not silently return false")
	}

	// Abort the transaction, then ask again: the query must fail and the error
	// must reach the caller rather than becoming "row missing".
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := rowExists(tx, "task_categories", 1); err == nil {
		t.Fatal("closed transaction must surface an error")
	}
}
