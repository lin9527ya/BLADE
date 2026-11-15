package db

import (
	"strings"
)

// SearchTasksByText is the lightweight task lookup used by the global command
// palette. It intentionally returns bare Task rows (no hydrated context/metrics):
// the palette only needs a title, status and id to navigate.
func (d *DB) SearchTasksByText(query string, limit int) ([]*Task, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []*Task{}, nil
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	pattern := "%" + escaped + "%"
	rows, err := d.Query(`SELECT `+taskCols+` FROM tasks
WHERE deleted_at IS NULL AND (
  COALESCE(name,'') ILIKE $1 ESCAPE '\' OR
  COALESCE(description,'') ILIKE $1 ESCAPE '\' OR
  COALESCE(goal,'') ILIKE $1 ESCAPE '\' OR
  id::text = $2
)
ORDER BY id DESC LIMIT $3`, pattern, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Task, 0, limit)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SearchAssetsByText searches the shared asset catalog by the fields a human
// would type into a command palette. SQL parameters keep %, _ and backslash
// literal, matching the findings search contract.
func (s *AssetStore) SearchAssetsByText(query string, limit int) ([]*Asset, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []*Asset{}, nil
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	pattern := "%" + escaped + "%"
	rows, err := s.db.Query(assetSelectCols+`
WHERE concat_ws(' ', COALESCE(domain,''), COALESCE(root_domain,''), COALESCE(ip,''),
                COALESCE(url,''), COALESCE(app_name,''), COALESCE(bundle_id,''),
                COALESCE(page_title,''), COALESCE(service_name,''), COALESCE(method,'')) ILIKE $1 ESCAPE '\'
ORDER BY last_seen DESC, id DESC LIMIT $2`, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

// TasksByIDs returns lightweight task rows without hydrating context. Used by
// the asset detail page to name the tasks that touched an asset.
func (d *DB) TasksByIDs(ids []int64) ([]*Task, error) {
	if len(ids) == 0 {
		return []*Task{}, nil
	}
	uniq := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return []*Task{}, nil
	}
	rows, err := d.Query(`SELECT `+taskCols+` FROM tasks
WHERE deleted_at IS NULL AND id = ANY($1::bigint[]) ORDER BY id DESC`, uniq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Task, 0, len(uniq))
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
