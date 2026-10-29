package db

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MaxPlannerTemplateBodyBytes caps one template body (32 KiB).
const MaxPlannerTemplateBodyBytes = 32 << 10

var (
	ErrPlannerTemplateNotFound      = errors.New("planner template not found")
	ErrPlannerTemplateNameConflict  = errors.New("planner template name exists")
	ErrPlannerTemplateInvalid       = errors.New("planner template invalid")
	ErrPlannerTemplateDefaultLocked = errors.New("default planner template cannot be deleted")
)

// PlannerTemplate is one editable planner prompt body. Tasks snapshot the body
// at creation time, so editing a template never changes a running task.
type PlannerTemplate struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	NKey      string    `json:"nkey"`
	Body      string    `json:"body"`
	Builtin   bool      `json:"builtin"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const plannerTemplateCols = `id, name, nkey, body, builtin, is_default, created_at, updated_at`

func scanPlannerTemplate(sc interface{ Scan(...any) error }) (*PlannerTemplate, error) {
	var t PlannerTemplate
	if err := sc.Scan(&t.ID, &t.Name, &t.NKey, &t.Body, &t.Builtin, &t.IsDefault, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

var plannerTemplateKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func normalizePlannerTemplate(name, body string) (string, string, error) {
	name = strings.TrimSpace(name)
	body = strings.TrimSpace(body)
	if name == "" {
		return "", "", fmt.Errorf("%w: 名称不能为空", ErrPlannerTemplateInvalid)
	}
	if len(body) == 0 {
		return "", "", fmt.Errorf("%w: 正文不能为空", ErrPlannerTemplateInvalid)
	}
	if len(body) > MaxPlannerTemplateBodyBytes {
		return "", "", fmt.Errorf("%w: 正文最多 %d 字节，当前 %d", ErrPlannerTemplateInvalid, MaxPlannerTemplateBodyBytes, len(body))
	}
	nkey := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	if !plannerTemplateKeyPattern.MatchString(nkey) {
		return "", "", fmt.Errorf("%w: 名称只能包含字母、数字、下划线与连字符", ErrPlannerTemplateInvalid)
	}
	return name, nkey, nil
}

func (d *DB) ListPlannerTemplates() ([]*PlannerTemplate, error) {
	rows, err := d.Query(`SELECT ` + plannerTemplateCols + ` FROM planner_templates
ORDER BY is_default DESC, builtin DESC, updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PlannerTemplate{}
	for rows.Next() {
		t, err := scanPlannerTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) GetPlannerTemplate(id int64) (*PlannerTemplate, error) {
	t, err := scanPlannerTemplate(d.QueryRow(`SELECT `+plannerTemplateCols+` FROM planner_templates WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (d *DB) CreatePlannerTemplate(name, body string) (*PlannerTemplate, error) {
	name, nkey, err := normalizePlannerTemplate(name, body)
	if err != nil {
		return nil, err
	}
	t, err := scanPlannerTemplate(d.QueryRow(`
INSERT INTO planner_templates(name, nkey, body) VALUES ($1,$2,$3)
ON CONFLICT (nkey) DO NOTHING
RETURNING `+plannerTemplateCols, name, nkey, body))
	if err == sql.ErrNoRows {
		return nil, ErrPlannerTemplateNameConflict
	}
	return t, err
}

func (d *DB) UpdatePlannerTemplate(id int64, name, body string) (*PlannerTemplate, error) {
	cur, err := d.GetPlannerTemplate(id)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, ErrPlannerTemplateNotFound
	}
	if cur.Builtin {
		return nil, fmt.Errorf("%w: 内置模板为只读", ErrPlannerTemplateInvalid)
	}
	name, nkey, err := normalizePlannerTemplate(name, body)
	if err != nil {
		return nil, err
	}
	t, err := scanPlannerTemplate(d.QueryRow(`
UPDATE planner_templates SET name=$2, nkey=$3, body=$4, updated_at=now()
WHERE id=$1 RETURNING `+plannerTemplateCols, id, name, nkey, body))
	if err == sql.ErrNoRows {
		return nil, ErrPlannerTemplateNotFound
	}
	if err != nil && strings.Contains(err.Error(), "planner_templates_nkey_key") {
		return nil, ErrPlannerTemplateNameConflict
	}
	return t, err
}

func (d *DB) DeletePlannerTemplate(id int64) error {
	cur, err := d.GetPlannerTemplate(id)
	if err != nil {
		return err
	}
	if cur == nil {
		return ErrPlannerTemplateNotFound
	}
	if cur.Builtin {
		return fmt.Errorf("%w: 内置模板为只读", ErrPlannerTemplateInvalid)
	}
	if cur.IsDefault {
		return ErrPlannerTemplateDefaultLocked
	}
	_, err = d.Exec(`DELETE FROM planner_templates WHERE id=$1`, id)
	return err
}

// SetDefaultPlannerTemplate makes one template the fallback for tasks without a
// snapshot. The partial unique index allows at most one default.
func (d *DB) SetDefaultPlannerTemplate(id int64) (*PlannerTemplate, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE planner_templates SET is_default=false, updated_at=now() WHERE is_default AND id<>$1`, id); err != nil {
		return nil, err
	}
	t, err := scanPlannerTemplate(tx.QueryRow(`
UPDATE planner_templates SET is_default=true, updated_at=now() WHERE id=$1
RETURNING `+plannerTemplateCols, id))
	if err == sql.ErrNoRows {
		return nil, ErrPlannerTemplateNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, tx.Commit()
}

// DefaultPlannerTemplateBody returns the default template's body ("" if none).
func (d *DB) DefaultPlannerTemplateBody() (string, error) {
	var body string
	err := d.QueryRow(`SELECT body FROM planner_templates WHERE is_default LIMIT 1`).Scan(&body)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return body, err
}
