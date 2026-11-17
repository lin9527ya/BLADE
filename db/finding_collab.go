package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// FindingComment is one operator note attached to a finding.
type FindingComment struct {
	ID        int64     `json:"id"`
	FindingID int64     `json:"finding_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// FindingCollab is the operator-facing collaboration state of a finding.
type FindingCollab struct {
	Assignee    string           `json:"assignee"`
	DueAt       *time.Time       `json:"due_at,omitempty"`
	CVSSScore   float64          `json:"cvss_score"`
	CVEIDs      []string         `json:"cve_ids"`
	Remediation string           `json:"remediation"`
	Comments    []FindingComment `json:"comments"`
}

// FindingCollabPatch uses explicit Set flags so "clear" is distinguishable from
// "leave unchanged" (the same contract as the task-template PATCH paths).
type FindingCollabPatch struct {
	Assignee    *string
	SetDueAt    bool
	DueAt       *time.Time
	CVSSScore   *float64
	CVEIDs      *[]string
	Remediation *string
}

func (d *DB) GetFindingCollab(id int64) (*FindingCollab, error) {
	var (
		c       FindingCollab
		due     sql.NullTime
		cveJSON []byte
	)
	err := d.QueryRow(`SELECT assignee, due_at, cvss_score, COALESCE(cve_ids,'[]'), remediation
FROM findings WHERE id=$1`, id).Scan(&c.Assignee, &due, &c.CVSSScore, &cveJSON, &c.Remediation)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if due.Valid {
		t := due.Time
		c.DueAt = &t
	}
	c.CVEIDs = []string{}
	if len(cveJSON) > 0 {
		_ = json.Unmarshal(cveJSON, &c.CVEIDs)
	}
	if c.CVEIDs == nil {
		c.CVEIDs = []string{}
	}
	comments, err := d.ListFindingComments(id)
	if err != nil {
		return nil, err
	}
	c.Comments = comments
	return &c, nil
}

func (d *DB) UpdateFindingCollab(id int64, patch FindingCollabPatch) (int64, error) {
	if id <= 0 {
		return 0, fmt.Errorf("invalid finding id")
	}
	var cveJSON any
	if patch.CVEIDs != nil {
		raw, err := json.Marshal(*patch.CVEIDs)
		if err != nil {
			return 0, err
		}
		cveJSON = string(raw)
	}
	res, err := d.Exec(`UPDATE findings SET
  assignee    = CASE WHEN $2::boolean THEN $3 ELSE assignee END,
  due_at      = CASE WHEN $4::boolean THEN $5 ELSE due_at END,
  cvss_score  = CASE WHEN $6::boolean THEN $7 ELSE cvss_score END,
  cve_ids     = CASE WHEN $8::boolean THEN $9::jsonb ELSE cve_ids END,
  remediation = CASE WHEN $10::boolean THEN $11 ELSE remediation END
WHERE id=$1`,
		id,
		patch.Assignee != nil, stringPtr(patch.Assignee),
		patch.SetDueAt, patch.DueAt,
		patch.CVSSScore != nil, floatPtr(patch.CVSSScore),
		patch.CVEIDs != nil, cveJSON,
		patch.Remediation != nil, stringPtr(patch.Remediation))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func stringPtr(v *string) any {
	if v == nil {
		return ""
	}
	return *v
}

func floatPtr(v *float64) any {
	if v == nil {
		return 0
	}
	return *v
}

// ListFindingComments returns oldest-first so the detail page reads as a log.
func (d *DB) ListFindingComments(findingID int64) ([]FindingComment, error) {
	rows, err := d.Query(`SELECT id, finding_id, author, body, created_at
FROM finding_comments WHERE finding_id=$1 ORDER BY id`, findingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FindingComment{}
	for rows.Next() {
		var c FindingComment
		if err := rows.Scan(&c.ID, &c.FindingID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) AddFindingComment(findingID int64, author, body string) (*FindingComment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("评论内容不能为空")
	}
	author = strings.TrimSpace(author)
	if author == "" {
		author = "operator"
	}
	c := &FindingComment{FindingID: findingID, Author: author, Body: body}
	err := d.QueryRow(`INSERT INTO finding_comments(finding_id, author, body)
VALUES($1,$2,$3) RETURNING id, created_at`, findingID, author, body).Scan(&c.ID, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (d *DB) DeleteFindingComment(findingID, commentID int64) (int64, error) {
	res, err := d.Exec(`DELETE FROM finding_comments WHERE id=$1 AND finding_id=$2`, commentID, findingID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FindingRemediationByNode returns the operator-maintained remediation note for
// each finding of a task, keyed by exploration node id. Empty notes are omitted.
func (d *DB) FindingRemediationByNode(taskID int64) (map[int64]string, error) {
	rows, err := d.Query(`SELECT node_id, remediation FROM findings
WHERE task_id=$1 AND node_id IS NOT NULL AND COALESCE(remediation,'') <> ''`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var nodeID int64
		var remediation string
		if err := rows.Scan(&nodeID, &remediation); err != nil {
			return nil, err
		}
		out[nodeID] = remediation
	}
	return out, rows.Err()
}
