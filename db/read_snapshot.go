package db

import (
	"context"
	"database/sql"
	"errors"
)

// ReadStatus distinguishes unknown totals from an empty, successfully-read set.
type ReadStatus struct {
	State       string `json:"state"`
	Total       *int64 `json:"total,omitempty"`
	SampleCount int    `json:"sample_count"`
	HasMore     bool   `json:"has_more,omitempty"`
	ReadMore    string `json:"read_more,omitempty"`
}

func ReadSetStatus(total int64, samples int, readMore string) ReadStatus {
	state := "complete"
	if total > int64(samples) {
		state = "truncated"
	}
	return ReadStatus{State: state, Total: &total, SampleCount: samples, HasMore: total > int64(samples), ReadMore: readMore}
}

// ReadSnapshot binds all related store queries to one MVCC view without copying locks.
func (d *DB) ReadSnapshot(ctx context.Context, fn func(*DB) error) error {
	if d.readTx != nil {
		return fn(d)
	}
	tx, err := d.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	view := &DB{DB: d.DB, readTx: tx, readCtx: ctx}
	if err := fn(view); err != nil {
		return err
	}
	if len(view.readErrors) > 0 {
		return errors.Join(view.readErrors...)
	}
	return tx.Commit()
}
func (d *DB) InReadSnapshot() bool { return d.readTx != nil }
func (d *DB) observeRead(err error) {
	if d.readTx != nil && err != nil && err != sql.ErrNoRows {
		d.readErrors = append(d.readErrors, err)
	}
}
func (d *DB) Query(query string, args ...any) (*Rows, error) {
	return d.QueryContext(context.Background(), query, args...)
}
func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*Rows, error) {
	var rows *sql.Rows
	var tx *sql.Tx
	var err error
	if d.readTx != nil {
		rows, err = d.readTx.QueryContext(d.readCtx, query, args...)
	} else if d.authority != nil {
		tx, err = d.BeginTx(ctx, nil)
		if err == nil {
			rows, err = tx.QueryContext(ctx, query, args...)
		}
	} else {
		rows, err = d.DB.QueryContext(ctx, query, args...)
	}
	if err != nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		d.observeRead(err)
		return nil, err
	}
	return &Rows{Rows: rows, tx: tx, observe: d.observeRead}, nil
}
func (d *DB) QueryRow(query string, args ...any) *Row {
	return d.QueryRowContext(context.Background(), query, args...)
}
func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *Row {
	if d.readTx != nil {
		return &Row{row: d.readTx.QueryRowContext(d.readCtx, query, args...), observe: func(err error) {
			if err != nil && err != sql.ErrNoRows {
				d.readErrors = append(d.readErrors, err)
			}
		}}
	}
	if d.authority != nil {
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return &Row{err: err}
		}
		return &Row{row: tx.QueryRowContext(ctx, query, args...), tx: tx}
	}
	return &Row{row: d.DB.QueryRowContext(ctx, query, args...)}
}
func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	return d.ExecContext(context.Background(), query, args...)
}
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if d.readTx != nil {
		return d.readTx.ExecContext(d.readCtx, query, args...)
	}
	if d.authority != nil {
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		return result, tx.Commit()
	}
	return d.DB.ExecContext(ctx, query, args...)
}
func (d *DB) Begin() (*sql.Tx, error) { return d.BeginTx(context.Background(), nil) }
func (d *DB) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	if d.readTx != nil {
		return nil, errors.New("cannot write or nest a transaction in a read snapshot")
	}
	tx, err := d.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	if d.authority != nil {
		if err := CheckWriteAuthorityTx(ctx, tx, *d.authority); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	return tx, nil
}
