package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	DeliverableStatusDraft    = "draft"
	DeliverableStatusAccepted = "accepted"
)

type Deliverable struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"`
	TaskID          *int64    `json:"task_id,omitempty"`
	CompanyID       *int64    `json:"company_id,omitempty"`
	CampaignID      *int64    `json:"campaign_id,omitempty"`
	Status          string    `json:"status"`
	LatestVersion   int       `json:"latest_version"`
	LatestSHA256    string    `json:"latest_sha256,omitempty"`
	AcceptanceCount int       `json:"acceptance_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type DeliverableVersion struct {
	ID            int64     `json:"id"`
	DeliverableID int64     `json:"deliverable_id"`
	Version       int       `json:"version"`
	ReportMD      string    `json:"report_md,omitempty"`
	SHA256        string    `json:"sha256"`
	Note          string    `json:"note"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
}

type DeliverableAcceptance struct {
	ID            int64     `json:"id"`
	DeliverableID int64     `json:"deliverable_id"`
	VersionID     int64     `json:"version_id"`
	Version       int       `json:"version"`
	AcceptedBy    string    `json:"accepted_by"`
	Note          string    `json:"note"`
	AcceptedAt    time.Time `json:"accepted_at"`
}

type DeliverableDetail struct {
	Deliverable *Deliverable            `json:"deliverable"`
	Versions    []DeliverableVersion    `json:"versions"`
	Acceptances []DeliverableAcceptance `json:"acceptances"`
}

type DeliverableInput struct {
	Name       string
	Kind       string
	TaskID     *int64
	CompanyID  *int64
	CampaignID *int64
	ReportMD   string
	Note       string
	CreatedBy  string
}

func deliverableSHA(report string) string {
	sum := sha256.Sum256([]byte(report))
	return hex.EncodeToString(sum[:])
}

func normalizeDeliverableKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "pentest"
	}
	return kind
}

func (d *DB) CreateDeliverable(in DeliverableInput) (*DeliverableDetail, error) {
	return d.CreateDeliverableContext(context.Background(), in)
}
func (d *DB) CreateDeliverableContext(ctx context.Context, in DeliverableInput) (*DeliverableDetail, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := insertDeliverableTx(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.WithContext(ctx).GetDeliverable(id)
}
func insertDeliverableTx(ctx context.Context, tx *sql.Tx, in DeliverableInput) (int64, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return 0, fmt.Errorf("交付物名称不能为空")
	}
	kind := normalizeDeliverableKind(in.Kind)
	createdBy := strings.TrimSpace(in.CreatedBy)
	if createdBy == "" {
		createdBy = "operator"
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO deliverables(name,kind,task_id,company_id,campaign_id) VALUES($1,$2,$3,$4,$5) RETURNING id`, name, kind, in.TaskID, in.CompanyID, in.CampaignID).Scan(&id); err != nil {
		return 0, err
	}
	if strings.TrimSpace(in.ReportMD) != "" || strings.TrimSpace(in.Note) != "" {
		report := strings.TrimSpace(in.ReportMD)
		if _, err := tx.ExecContext(ctx, `INSERT INTO deliverable_versions(deliverable_id,version,report_md,sha256,note,created_by) VALUES($1,1,$2,$3,$4,$5)`, id, report, deliverableSHA(report), strings.TrimSpace(in.Note), createdBy); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (d *DB) ListDeliverables() ([]*Deliverable, error) {
	rows, err := d.Query(`
SELECT d.id,d.name,d.kind,d.task_id,d.company_id,d.campaign_id,d.status,
       COALESCE(v.version,0),COALESCE(v.sha256,''),
       (SELECT count(*) FROM deliverable_acceptances a WHERE a.deliverable_id=d.id),
       d.created_at,d.updated_at
FROM deliverables d
LEFT JOIN LATERAL (
  SELECT version,sha256 FROM deliverable_versions
  WHERE deliverable_id=d.id ORDER BY version DESC LIMIT 1
) v ON true
ORDER BY d.updated_at DESC, d.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deliverable{}
	for rows.Next() {
		var item Deliverable
		var taskID, companyID, campaignID sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &taskID, &companyID, &campaignID, &item.Status,
			&item.LatestVersion, &item.LatestSHA256, &item.AcceptanceCount, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.TaskID = nullInt64Ptr(taskID)
		item.CompanyID = nullInt64Ptr(companyID)
		item.CampaignID = nullInt64Ptr(campaignID)
		out = append(out, &item)
	}
	return out, rows.Err()
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func (d *DB) GetDeliverable(id int64) (*DeliverableDetail, error) {
	var item Deliverable
	var taskID, companyID, campaignID sql.NullInt64
	err := d.QueryRow(`
SELECT d.id,d.name,d.kind,d.task_id,d.company_id,d.campaign_id,d.status,
       COALESCE(v.version,0),COALESCE(v.sha256,''),
       (SELECT count(*) FROM deliverable_acceptances a WHERE a.deliverable_id=d.id),
       d.created_at,d.updated_at
FROM deliverables d
LEFT JOIN LATERAL (
  SELECT version,sha256 FROM deliverable_versions
  WHERE deliverable_id=d.id ORDER BY version DESC LIMIT 1
) v ON true
WHERE d.id=$1`, id).Scan(&item.ID, &item.Name, &item.Kind, &taskID, &companyID, &campaignID, &item.Status,
		&item.LatestVersion, &item.LatestSHA256, &item.AcceptanceCount, &item.CreatedAt, &item.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.TaskID = nullInt64Ptr(taskID)
	item.CompanyID = nullInt64Ptr(companyID)
	item.CampaignID = nullInt64Ptr(campaignID)
	versions, err := d.ListDeliverableVersions(id, false)
	if err != nil {
		return nil, err
	}
	acceptances, err := d.ListDeliverableAcceptances(id)
	if err != nil {
		return nil, err
	}
	return &DeliverableDetail{Deliverable: &item, Versions: versions, Acceptances: acceptances}, nil
}

// ListDeliverableVersions returns newest first. includeBody=false keeps the list
// payload small; the detail/export paths read the body explicitly.
func (d *DB) ListDeliverableVersions(deliverableID int64, includeBody bool) ([]DeliverableVersion, error) {
	cols := `id,deliverable_id,version,'' AS report_md,sha256,note,created_by,created_at`
	if includeBody {
		cols = `id,deliverable_id,version,report_md,sha256,note,created_by,created_at`
	}
	rows, err := d.Query(`SELECT `+cols+` FROM deliverable_versions
WHERE deliverable_id=$1 ORDER BY version DESC`, deliverableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeliverableVersion{}
	for rows.Next() {
		var v DeliverableVersion
		if err := rows.Scan(&v.ID, &v.DeliverableID, &v.Version, &v.ReportMD, &v.SHA256, &v.Note, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (d *DB) ListDeliverableAcceptances(deliverableID int64) ([]DeliverableAcceptance, error) {
	rows, err := d.Query(`SELECT a.id,a.deliverable_id,a.version_id,v.version,a.accepted_by,a.note,a.accepted_at
FROM deliverable_acceptances a JOIN deliverable_versions v ON v.id=a.version_id
WHERE a.deliverable_id=$1 ORDER BY a.id DESC`, deliverableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeliverableAcceptance{}
	for rows.Next() {
		var a DeliverableAcceptance
		if err := rows.Scan(&a.ID, &a.DeliverableID, &a.VersionID, &a.Version, &a.AcceptedBy, &a.Note, &a.AcceptedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) GetDeliverableVersion(deliverableID, versionID int64) (*DeliverableVersion, error) {
	var v DeliverableVersion
	err := d.QueryRow(`SELECT id,deliverable_id,version,report_md,sha256,note,created_by,created_at
FROM deliverable_versions WHERE id=$1 AND deliverable_id=$2`, versionID, deliverableID).Scan(
		&v.ID, &v.DeliverableID, &v.Version, &v.ReportMD, &v.SHA256, &v.Note, &v.CreatedBy, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (d *DB) AddDeliverableVersion(deliverableID int64, reportMD, note, createdBy string) (*DeliverableVersion, error) {
	report := strings.TrimSpace(reportMD)
	if report == "" {
		return nil, fmt.Errorf("版本正文不能为空")
	}
	if createdBy = strings.TrimSpace(createdBy); createdBy == "" {
		createdBy = "operator"
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var exists int64
	if err := tx.QueryRow(`SELECT id FROM deliverables WHERE id=$1 FOR UPDATE`, deliverableID).Scan(&exists); err == sql.ErrNoRows {
		return nil, fmt.Errorf("交付物不存在")
	} else if err != nil {
		return nil, err
	}
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM deliverable_versions WHERE deliverable_id=$1`, deliverableID).Scan(&version); err != nil {
		return nil, err
	}
	var id int64
	if err := tx.QueryRow(`INSERT INTO deliverable_versions(deliverable_id,version,report_md,sha256,note,created_by)
VALUES($1,$2,$3,$4,$5,$6) RETURNING id,created_at`,
		deliverableID, version, report, deliverableSHA(report), strings.TrimSpace(note), createdBy).Scan(&id, new(time.Time)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE deliverables SET updated_at=now(), status='draft' WHERE id=$1`, deliverableID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetDeliverableVersion(deliverableID, id)
}

func (d *DB) AcceptDeliverable(deliverableID, versionID int64, acceptedBy, note string) (*DeliverableAcceptance, error) {
	if acceptedBy = strings.TrimSpace(acceptedBy); acceptedBy == "" {
		acceptedBy = "operator"
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var version int
	if err := tx.QueryRow(`SELECT version FROM deliverable_versions WHERE id=$1 AND deliverable_id=$2`, versionID, deliverableID).Scan(&version); err == sql.ErrNoRows {
		return nil, fmt.Errorf("交付物版本不存在")
	} else if err != nil {
		return nil, err
	}
	var id int64
	var acceptedAt time.Time
	if err := tx.QueryRow(`INSERT INTO deliverable_acceptances(deliverable_id,version_id,accepted_by,note)
VALUES($1,$2,$3,$4) RETURNING id,accepted_at`, deliverableID, versionID, acceptedBy, strings.TrimSpace(note)).Scan(&id, &acceptedAt); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE deliverables SET status='accepted', updated_at=now() WHERE id=$1`, deliverableID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &DeliverableAcceptance{ID: id, DeliverableID: deliverableID, VersionID: versionID, Version: version, AcceptedBy: acceptedBy, Note: strings.TrimSpace(note), AcceptedAt: acceptedAt}, nil
}

func (d *DB) DeleteDeliverable(id int64) (int64, error) {
	res, err := d.Exec(`DELETE FROM deliverables WHERE id=$1`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeliverableDiff returns a unified diff between two versions of one deliverable.
func (d *DB) DeliverableDiff(deliverableID, fromVersionID, toVersionID int64) (string, error) {
	from, err := d.GetDeliverableVersion(deliverableID, fromVersionID)
	if err != nil || from == nil {
		return "", fmt.Errorf("对比源版本不存在")
	}
	to, err := d.GetDeliverableVersion(deliverableID, toVersionID)
	if err != nil || to == nil {
		return "", fmt.Errorf("对比目标版本不存在")
	}
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(from.ReportMD),
		B:        difflib.SplitLines(to.ReportMD),
		FromFile: fmt.Sprintf("v%d", from.Version),
		ToFile:   fmt.Sprintf("v%d", to.Version),
		Context:  3,
	})
}
