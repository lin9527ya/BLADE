package db

import (
	"context"
	"database/sql"
)

// Task-owned finding mutations share the same task-row fence as archive queueing.
func (d *DB) withFindingMutation(ctx context.Context, id int64, fn func(*sql.Tx) error) error {
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		if err := LockFindingEvidenceTx(tx, id, nil); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Lock all affected tasks before case/finding row locks. Cross-task cases must
// not mutate an archived member, and a deterministic order avoids AB/BA locks.
func lockCaseMutationTx(tx *sql.Tx, caseID, taskID int64, findingIDs []int64) error {
	rows, err := tx.Query(`SELECT DISTINCT id FROM tasks WHERE id=$2
OR id IN (SELECT task_id FROM findings WHERE id=ANY($3::bigint[]) OR id IN (
    SELECT finding_id FROM finding_case_members WHERE case_id=$1 OR case_id IN (
        SELECT case_id FROM finding_case_members WHERE finding_id=ANY($3::bigint[]))))
OR id IN (SELECT task_id FROM finding_cases WHERE id=$1 OR id IN (
    SELECT case_id FROM finding_case_members WHERE finding_id=ANY($3::bigint[]))) ORDER BY id`, caseID, taskID, findingIDs)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := LockTaskEvidenceTx(tx, id); err != nil {
			return err
		}
	}
	return nil
}

// key is a package-owned predicate, never a caller-provided SQL identifier.
func (d *DB) withRetestMutation(ctx context.Context, key string, id int64, fn func(*sql.Tx) error) error {
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		var findingID int64
		if err := tx.QueryRow(`SELECT finding_id FROM finding_retests WHERE `+key+`=$1`, id).Scan(&findingID); err != nil {
			return err
		}
		if err := LockFindingEvidenceTx(tx, findingID, nil); err != nil {
			return err
		}
		return fn(tx)
	})
}
