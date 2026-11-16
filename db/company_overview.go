package db

import (
	"fmt"
	"time"
)

// CompanyFindingSummary is the cross-task finding aggregate for one company.
// Findings are linked to a company through their anchored assets, not through
// task ownership: a shared asset may be used by several tasks.
type CompanyFindingSummary struct {
	Total    int          `json:"total"`
	Critical int          `json:"critical"`
	High     int          `json:"high"`
	Medium   int          `json:"medium"`
	Low      int          `json:"low"`
	Recent   []*DBFinding `json:"recent"`
}

// CountsByTypeForCompany returns asset counts grouped by type for a company.
func (s *AssetStore) CountsByTypeForCompany(companyID int64) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT type, COUNT(*) FROM assets WHERE company_id=$1 GROUP BY type`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var typ string
		var n int
		if err := rows.Scan(&typ, &n); err != nil {
			return nil, err
		}
		out[typ] = n
	}
	return out, rows.Err()
}

const companyFindingWhere = `EXISTS (
  SELECT 1 FROM jsonb_array_elements_text(f.asset_ids) e(v)
  JOIN assets a ON a.id = e.v::bigint
  WHERE a.company_id = $1
)`

// CompanyFindings returns severity counts plus the newest findings for a company.
func (d *DB) CompanyFindings(companyID int64, recentLimit int) (*CompanyFindingSummary, error) {
	if companyID <= 0 {
		return nil, fmt.Errorf("invalid company id")
	}
	out := &CompanyFindingSummary{Recent: []*DBFinding{}}
	err := d.QueryRow(`SELECT COUNT(*),
  COALESCE(SUM(CASE WHEN severity='critical' THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN severity='high' THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN severity='medium' THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN severity='low' THEN 1 ELSE 0 END),0)
FROM findings f WHERE `+companyFindingWhere, companyID).Scan(&out.Total, &out.Critical, &out.High, &out.Medium, &out.Low)
	if err != nil {
		return nil, err
	}
	if recentLimit <= 0 {
		recentLimit = 20
	}
	if recentLimit > 100 {
		recentLimit = 100
	}
	rows, err := d.Query(`SELECT `+findingSelectCols+`
FROM findings f LEFT JOIN tasks t ON f.task_id=t.id
WHERE `+companyFindingWhere+`
ORDER BY f.created_at DESC, f.id DESC LIMIT $2`, companyID, recentLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recent, err := scanFindings(rows)
	if err != nil {
		return nil, err
	}
	out.Recent = recent
	return out, nil
}

// CompanyRecentActivity is a compact timeline for the 360 overview. It reuses
// the existing activity table rather than building a second event log.
type CompanyRecentActivity struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Tool      string    `json:"tool,omitempty"`
	Summary   string    `json:"summary"`
	TaskID    int64     `json:"task_id"`
	TaskName  string    `json:"task_name,omitempty"`
	Worker    string    `json:"worker,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (d *DB) CompanyRecentActivity(companyID int64, limit int) ([]CompanyRecentActivity, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := d.Query(`
SELECT a.id, a.kind, COALESCE(a.tool,''), a.summary, t.id, COALESCE(t.name,''), COALESCE(a.worker,''), a.created_at
FROM activity a
JOIN tasks t ON t.exploration_id = a.exploration_id
WHERE t.deleted_at IS NULL AND EXISTS (
  SELECT 1 FROM task_scope ts WHERE ts.task_id=t.id AND ts.kind='company' AND ts.company_id=$1
)
ORDER BY a.id DESC LIMIT $2`, companyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CompanyRecentActivity{}
	for rows.Next() {
		var item CompanyRecentActivity
		if err := rows.Scan(&item.ID, &item.Kind, &item.Tool, &item.Summary, &item.TaskID, &item.TaskName, &item.Worker, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
