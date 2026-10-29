package db

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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

// MaxPlannerTemplateNameRunes caps the display name. Chinese names are allowed —
// 内置场景模板（内网渗透/权限提升/…）就是这么命名的，nkey 对它们做恒等映射。
const MaxPlannerTemplateNameRunes = 120

// plannerTemplateKeyPattern 是纯 ASCII 名的 nkey 字符集。
var plannerTemplateKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// plannerTemplateNKey 把显示名映射成唯一键：
//   - 纯 ASCII 名 → 小写、空格转连字符，必须是 [a-z0-9][a-z0-9_-]*；
//   - 含非 ASCII 字符（内置场景模板「内网渗透」等）→ 小写、空格转连字符后原样作键，
//     字符集放开到「任意文字的字母/数字 + - _」。
//
// 两种口径都拒绝标点与控制字符；空白在更早的 normalize 里已被折叠成单个空格、
// 再转成连字符。nkey 只用于唯一性，不拼路径、不拼 SQL，所以放开中文不引入注入面。
func plannerTemplateNKey(name string) (string, error) {
	key := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	if plannerTemplateKeyPattern.MatchString(key) {
		return key, nil
	}
	if hasNonASCII(key) && validWideTemplateKey(key) {
		return key, nil
	}
	return "", fmt.Errorf("%w: 名称只能包含字母、数字、下划线、连字符，或中文等非 ASCII 字符", ErrPlannerTemplateInvalid)
}

func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

func validWideTemplateKey(key string) bool {
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return key != ""
}

func normalizePlannerTemplate(name, body string) (string, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	body = strings.TrimSpace(body)
	if name == "" {
		return "", "", fmt.Errorf("%w: 名称不能为空", ErrPlannerTemplateInvalid)
	}
	if utf8.RuneCountInString(name) > MaxPlannerTemplateNameRunes {
		return "", "", fmt.Errorf("%w: 名称最多 %d 个字符", ErrPlannerTemplateInvalid, MaxPlannerTemplateNameRunes)
	}
	if len(body) == 0 {
		return "", "", fmt.Errorf("%w: 正文不能为空", ErrPlannerTemplateInvalid)
	}
	if len(body) > MaxPlannerTemplateBodyBytes {
		return "", "", fmt.Errorf("%w: 正文最多 %d 字节，当前 %d", ErrPlannerTemplateInvalid, MaxPlannerTemplateBodyBytes, len(body))
	}
	nkey, err := plannerTemplateNKey(name)
	if err != nil {
		return "", "", err
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

// SeedPlannerTemplateIfEmpty inserts a built-in template once (nkey 已存在即 no-op)，
// 与 SeedPromptIfEmpty 同模式：用户改过或删掉都不会被重启塞回来。
func (d *DB) SeedPlannerTemplateIfEmpty(name, body string, isDefault bool) (bool, error) {
	name, nkey, err := normalizePlannerTemplate(name, body)
	if err != nil {
		return false, err
	}
	res, err := d.Exec(`INSERT INTO planner_templates(name, nkey, body, builtin, is_default)
VALUES ($1,$2,$3,true,$4)
ON CONFLICT (nkey) DO NOTHING`, name, nkey, body, isDefault)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetPlannerTemplateByNKey 按 nkey 查（场景分类按模板名解析 id 用）。
func (d *DB) GetPlannerTemplateByNKey(nkey string) (*PlannerTemplate, error) {
	t, err := scanPlannerTemplate(d.QueryRow(`SELECT `+plannerTemplateCols+` FROM planner_templates WHERE nkey=$1`, nkey))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
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
