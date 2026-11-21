package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
)

var ErrDeliverableRequestConflict = errors.New("相同操作号的请求参数已变化")
var ErrDeliverableRequestGone = errors.New("该操作对应的交付物已删除，不会自动重复创建")

func (d *DB) AggregateRequest(ctx context.Context, owner, key, hash string) (*DeliverableDetail, bool, error) {
	var stored string
	var id sql.NullInt64
	err := d.QueryRowContext(ctx, `SELECT request_sha,deliverable_id FROM aggregate_deliverable_requests WHERE owner=$1 AND operation_key=$2`, owner, key).Scan(&stored, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if stored != hash {
		return nil, true, ErrDeliverableRequestConflict
	}
	if !id.Valid {
		return nil, true, ErrDeliverableRequestGone
	}
	result, err := d.WithContext(ctx).GetDeliverable(id.Int64)
	if err == nil && result == nil {
		err = ErrDeliverableRequestGone
	}
	return result, true, err
}

// The receipt and document commit atomically. Duplicate generation can happen
// before this short transaction, but a key can only persist one final document.
func (d *DB) CreateAggregateDeliverable(ctx context.Context, in DeliverableInput, owner, key, hash string) (*DeliverableDetail, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	sum := sha256.Sum256([]byte(owner + "\x00" + key))
	lock := int64(binary.BigEndian.Uint64(sum[:8]))
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lock); err != nil {
		return nil, false, err
	}
	var stored string
	var existing sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT request_sha,deliverable_id FROM aggregate_deliverable_requests WHERE owner=$1 AND operation_key=$2`, owner, key).Scan(&stored, &existing)
	if err == nil {
		if stored != hash {
			return nil, true, ErrDeliverableRequestConflict
		}
		if !existing.Valid {
			return nil, true, ErrDeliverableRequestGone
		}
		if err := tx.Commit(); err != nil {
			return nil, true, err
		}
		result, err := d.WithContext(ctx).GetDeliverable(existing.Int64)
		return result, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	id, err := insertDeliverableTx(ctx, tx, in)
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO aggregate_deliverable_requests(owner,operation_key,request_sha,deliverable_id) VALUES($1,$2,$3,$4)`, owner, key, hash, id); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	result, err := d.WithContext(ctx).GetDeliverable(id)
	return result, false, err
}
