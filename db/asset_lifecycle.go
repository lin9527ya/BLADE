package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// AssetFilter is the server-side filter shared by the asset list and export.
// DSL is the same expression accepted by QueryDSL; the other fields are ANDed.
type AssetFilter struct {
	Type      string
	CompanyID int64
	TaskID    int64
	Status    string
	Provider  string
	DSL       string
}

func (f AssetFilter) where() (string, []any, error) {
	where := "1=1"
	args := []any{}
	if strings.TrimSpace(f.DSL) != "" {
		node, err := ParseDSL(f.DSL)
		if err != nil {
			return "", nil, err
		}
		dslWhere, dslArgs, err := buildDSLWhere(node)
		if err != nil {
			return "", nil, err
		}
		where, args = dslWhere, dslArgs
	}
	if f.Type != "" {
		args = append(args, f.Type)
		where += fmt.Sprintf(" AND type = $%d", len(args))
	}
	if f.CompanyID > 0 {
		args = append(args, f.CompanyID)
		where += fmt.Sprintf(" AND company_id = $%d", len(args))
	}
	if f.TaskID > 0 {
		args = append(args, f.TaskID)
		where += fmt.Sprintf(" AND $%d = ANY(task_ids)", len(args))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if f.Provider != "" {
		args = append(args, f.Provider)
		where += fmt.Sprintf(" AND provider = $%d", len(args))
	}
	return where, args, nil
}

func (s *AssetStore) CountFiltered(f AssetFilter) (int, error) {
	where, args, err := f.where()
	if err != nil {
		return 0, err
	}
	var n int
	err = s.db.QueryRow("SELECT count(*) FROM assets WHERE "+where, args...).Scan(&n)
	return n, err
}

func (s *AssetStore) QueryFiltered(f AssetFilter, limit, offset int) ([]*Asset, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where, args, err := f.where()
	if err != nil {
		return nil, err
	}
	args = append(args, limit, offset)
	q := assetSelectCols + " WHERE " + where +
		fmt.Sprintf(" ORDER BY last_seen DESC, id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets, err := scanAssets(rows)
	if err != nil {
		return nil, err
	}
	if f.TaskID > 0 {
		if err := s.hydrateTaskAssetSources(f.TaskID, assets); err != nil {
			return nil, err
		}
	}
	return assets, nil
}

func (s *AssetStore) UpdateAssetLifecycle(id int64, status, provider string) error {
	res, err := s.db.Exec(`UPDATE assets SET
status = COALESCE(NULLIF($2,''), status),
provider = COALESCE(NULLIF($3,''), provider)
WHERE id = $1`, id, status, provider)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AssetSource is one provenance entry for an asset, e.g. agent / csv / fofa.
type AssetSource struct {
	Source          string `json:"source"`
	FirstSeen       string `json:"first_seen"`
	LastSeen        string `json:"last_seen"`
	LastSyncBatchID *int64 `json:"last_sync_batch_id,omitempty"`
}

func (s *AssetStore) AssetSources(assetID int64) ([]AssetSource, error) {
	rows, err := s.db.Query(`SELECT source, first_seen::text, last_seen::text, last_sync_batch_id
FROM asset_sources WHERE asset_id=$1 ORDER BY last_seen DESC, source`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetSource{}
	for rows.Next() {
		var item AssetSource
		var batch sql.NullInt64
		if err := rows.Scan(&item.Source, &item.FirstSeen, &item.LastSeen, &batch); err != nil {
			return nil, err
		}
		if batch.Valid {
			v := batch.Int64
			item.LastSyncBatchID = &v
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// RecordAssetSource upserts provenance and makes source the asset's current provider.
// batchID <= 0 means "no sync batch" (agent/task writes).
func (s *AssetStore) RecordAssetSource(assetID int64, source string, batchID int64) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return fmt.Errorf("source is required")
	}
	if _, err := s.db.Exec(`INSERT INTO asset_sources(asset_id, source, last_seen, last_sync_batch_id)
VALUES($1,$2,now(),NULLIF($3,0))
ON CONFLICT (asset_id, source) DO UPDATE SET
  last_seen = now(),
  last_sync_batch_id = COALESCE(EXCLUDED.last_sync_batch_id, asset_sources.last_sync_batch_id)`,
		assetID, source, batchID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE assets SET
provider = $2,
last_sync_batch_id = COALESCE(NULLIF($3,0), last_sync_batch_id),
last_seen = now()
WHERE id = $1`, assetID, source, batchID)
	return err
}

func (s *AssetStore) CreateAssetSyncBatch(source string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`INSERT INTO asset_sync_batches(source) VALUES($1) RETURNING id`, source).Scan(&id)
	return id, err
}

func (s *AssetStore) FinishAssetSyncBatch(id int64, status string, total, created, updated int, note string) error {
	if status == "" {
		status = "completed"
	}
	_, err := s.db.Exec(`UPDATE asset_sync_batches SET
status=$2, total_count=$3, created_count=$4, updated_count=$5, note=$6, finished_at=now()
WHERE id=$1`, id, status, total, created, updated, note)
	return err
}

// IterateFiltered scans one query under the caller's snapshot. It does not
// count first, materialize all assets, or paginate by a mutable sort key.
func (s *AssetStore) IterateFiltered(ctx context.Context, f AssetFilter, consume func(*Asset) error) error {
	where, args, err := f.where()
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, assetSelectCols+" WHERE "+where+" ORDER BY last_seen DESC,id DESC", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		a, err := scanAssetRow(rows)
		if err != nil {
			return err
		}
		if err := consume(a); err != nil {
			return err
		}
	}
	return rows.Err()
}
