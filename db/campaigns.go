package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 战役：任务之上的一层（见 schema.sql 里 campaigns 的注释）。
//
// 刻意保持很薄：只做**归属与聚合**，不做编排——依赖、顺序、调度都还在任务层。
// 一个任务至多属于一个战役（0/1，可为空）：「同一个任务算不算两次作业的一部分」是个
// 没有答案的问题，允许 N:N 只会让人纠结。

// ErrCampaignInvalid 标记战役入参不合法（与 IO 错误区分开，调用方据此回 400 而不是 500）。
var ErrCampaignInvalid = errors.New("invalid campaign")

// Campaign 是一次作业：一个客户、若干目标、一份交付物。
type Campaign struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CompanyID *int64 `json:"company_id,omitempty"`
	Note      string `json:"note"`
	// P1 编排字段：goal/scope 只做展示与交接，**不参与 RoE**——授权范围仍由
	// task_scope/company_scope 决定，战役 scope 不自动写入它们。
	Goal                    string          `json:"goal"`
	Scope                   json.RawMessage `json:"scope,omitempty"`
	Phase                   string          `json:"phase"`
	Status                  string          `json:"status"`
	StartsAt                *time.Time      `json:"starts_at,omitempty"`
	EndsAt                  *time.Time      `json:"ends_at,omitempty"`
	CredentialAttemptBudget int             `json:"credential_attempt_budget"` // -1=继承全局；0=不限
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

const campaignCols = `id, name, company_id, note, created_at, updated_at,
	COALESCE(goal,''), COALESCE(scope,'{}'::jsonb), COALESCE(phase,''), COALESCE(status,'active'),
	starts_at, ends_at, COALESCE(credential_attempt_budget,-1)`

func scanCampaign(row interface{ Scan(...any) error }) (*Campaign, error) {
	c := &Campaign{}
	err := row.Scan(&c.ID, &c.Name, &c.CompanyID, &c.Note, &c.CreatedAt, &c.UpdatedAt,
		&c.Goal, &c.Scope, &c.Phase, &c.Status, &c.StartsAt, &c.EndsAt, &c.CredentialAttemptBudget)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// campaignNKey 归一化战役名做唯一键：判重看的是「同一个名字」，不该因为多打一个空格
// 或多敲了大小写就算两次作业。
func campaignNKey(name string) (string, error) {
	n := strings.ToLower(strings.Join(strings.Fields(name), " "))
	if n == "" {
		return "", fmt.Errorf("%w：战役名不能为空", ErrCampaignInvalid)
	}
	return n, nil
}

// CreateCampaign 新建一次作业。同名（归一化后）会被 nkey 唯一约束挡住。
func (d *DB) CreateCampaign(name, note string, companyID *int64) (*Campaign, error) {
	nkey, err := campaignNKey(name)
	if err != nil {
		return nil, err
	}
	c, err := scanCampaign(d.QueryRow(`
INSERT INTO campaigns(name, nkey, company_id, note) VALUES ($1,$2,$3,$4) RETURNING `+campaignCols,
		strings.Join(strings.Fields(name), " "), nkey, companyID, strings.TrimSpace(note)))
	if err != nil {
		if strings.Contains(err.Error(), "campaigns_nkey_key") {
			return nil, fmt.Errorf("%w：已存在同名战役", ErrCampaignInvalid)
		}
		return nil, err
	}
	return c, nil
}

// ListCampaigns 列出全部战役，新建的在前。
func (d *DB) ListCampaigns() ([]*Campaign, error) {
	rows, err := d.Query(`SELECT ` + campaignCols + ` FROM campaigns ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCampaign 取一次作业；不存在返回 (nil, nil)。
func (d *DB) GetCampaign(id int64) (*Campaign, error) {
	return scanCampaign(d.QueryRow(`SELECT `+campaignCols+` FROM campaigns WHERE id=$1`, id))
}

// UpdateCampaign 改名/备注/客户。改名同样受唯一约束约束。
func (d *DB) UpdateCampaign(id int64, name, note string, companyID *int64) (*Campaign, error) {
	nkey, err := campaignNKey(name)
	if err != nil {
		return nil, err
	}
	c, err := scanCampaign(d.QueryRow(`
UPDATE campaigns SET name=$2, nkey=$3, company_id=$4, note=$5 WHERE id=$1 RETURNING `+campaignCols,
		id, strings.Join(strings.Fields(name), " "), nkey, companyID, strings.TrimSpace(note)))
	if err != nil {
		if strings.Contains(err.Error(), "campaigns_nkey_key") {
			return nil, fmt.Errorf("%w：已存在同名战役", ErrCampaignInvalid)
		}
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("%w：战役不存在", ErrCampaignInvalid)
	}
	return c, nil
}

// DeleteCampaign 删一次作业。**只解绑、不删任务**——任务是被归档与交付的单位，
// 不该因为"把作业归错了组"而消失（FK 是 ON DELETE SET NULL）。
// 返回被解绑的任务数，让调用方能把这件事告诉用户。
func (d *DB) DeleteCampaign(id int64) (int64, error) {
	var bound int64
	if err := d.QueryRow(`SELECT count(*) FROM tasks WHERE campaign_id=$1`, id).Scan(&bound); err != nil {
		return 0, err
	}
	res, err := d.Exec(`DELETE FROM campaigns WHERE id=$1`, id)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, err
	} else if n == 0 {
		return 0, fmt.Errorf("%w：战役不存在", ErrCampaignInvalid)
	}
	return bound, nil
}

// CampaignContext is the campaign/phase context visible to an exploration. It is
// optional: standalone tasks return nil. The planner uses it as strategy context
// (campaign goal/phase), not as an authorization boundary.
type CampaignContext struct {
	CampaignID int64             `json:"campaign_id"`
	Name       string            `json:"name"`
	Goal       string            `json:"goal,omitempty"`
	Phase      string            `json:"phase,omitempty"`
	Status     string            `json:"status,omitempty"`
	Scope      string            `json:"scope,omitempty"`
	PhaseName  string            `json:"phase_name,omitempty"`
	PhaseState string            `json:"phase_state,omitempty"`
	TaskCount  int               `json:"task_count,omitempty"`
	Tasks      []CampaignTaskRef `json:"tasks,omitempty"`
}

// CampaignTaskRef is a compact sibling task in the same campaign.
type CampaignTaskRef struct {
	ID     int64  `json:"id"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

// CampaignContext returns the campaign/phase bound to this exploration, or nil
// for standalone tasks. Read-only; missing campaign rows are not an error.
func (s *ExplorationStore) CampaignContext() (*CampaignContext, error) {
	var c CampaignContext
	var scope sql.NullString
	var phaseName, phaseState sql.NullString
	err := s.db.QueryRow(`
SELECT t.campaign_id, c.name, COALESCE(c.goal,''), COALESCE(c.phase,''), COALESCE(c.status,''),
       COALESCE(c.scope::text,'{}'), COALESCE(p.name,''), COALESCE(p.status,'')
FROM tasks t
JOIN campaigns c ON c.id=t.campaign_id
LEFT JOIN campaign_phases p ON p.id=t.campaign_phase_id
WHERE t.exploration_id=$1 AND t.deleted_at IS NULL
LIMIT 1`, s.expID).Scan(&c.CampaignID, &c.Name, &c.Goal, &c.Phase, &c.Status, &scope, &phaseName, &phaseState)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Scope = scope.String
	c.PhaseName = phaseName.String
	c.PhaseState = phaseState.String
	if err := s.db.QueryRow(`SELECT count(*) FROM tasks WHERE campaign_id=$1 AND deleted_at IS NULL`, c.CampaignID).Scan(&c.TaskCount); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, COALESCE(NULLIF(name,''), description), COALESCE(status,'') FROM tasks
WHERE campaign_id=$1 AND deleted_at IS NULL ORDER BY id DESC LIMIT 8`, c.CampaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t CampaignTaskRef
		if err := rows.Scan(&t.ID, &t.Name, &t.Status); err != nil {
			return nil, err
		}
		c.Tasks = append(c.Tasks, t)
	}
	return &c, rows.Err()
}

// SetTaskCampaign 把任务归入/移出战役（campaignID 为 nil = 移出）。// SetTaskCampaign 把任务归入/移出战役（campaignID 为 nil = 移出）。
// 任务必须存在且未删除——给不存在的任务挂战役会静默没效果，不如当场报错。
func (d *DB) SetTaskCampaign(taskID int64, campaignID *int64) error {
	res, err := d.Exec(`UPDATE tasks SET campaign_id=$2 WHERE id=$1 AND deleted_at IS NULL`, taskID, campaignID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%w：任务不存在或已删除", ErrCampaignInvalid)
	}
	return nil
}

// CampaignTaskIDs 取一次作业下的任务 id（新建的在前，与任务列表同序感）。
func (d *DB) CampaignTaskIDs(campaignID int64) ([]int64, error) {
	rows, err := d.Query(`SELECT id FROM tasks WHERE campaign_id=$1 AND deleted_at IS NULL ORDER BY id DESC`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------- P1：战役目标与阶段 ----------

// CampaignPhase 是战役里的一个阶段（planned/active/done/aborted）。
type CampaignPhase struct {
	ID         int64      `json:"id"`
	CampaignID int64      `json:"campaign_id"`
	Name       string     `json:"name"`
	Position   int        `json:"position"`
	Status     string     `json:"status"`
	Note       string     `json:"note"`
	StartsAt   *time.Time `json:"starts_at,omitempty"`
	EndsAt     *time.Time `json:"ends_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

const campaignPhaseCols = `id, campaign_id, name, position, status, note, starts_at, ends_at, created_at, updated_at`

func scanCampaignPhase(row interface{ Scan(...any) error }) (*CampaignPhase, error) {
	p := &CampaignPhase{}
	err := row.Scan(&p.ID, &p.CampaignID, &p.Name, &p.Position, &p.Status, &p.Note,
		&p.StartsAt, &p.EndsAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// UpdateCampaignMeta 更新 P1 编排字段（goal/scope/phase/status/时间窗）。
func (d *DB) UpdateCampaignMeta(id int64, goal string, scope json.RawMessage, phase, status string, startsAt, endsAt *time.Time, credentialBudget int) (*Campaign, error) {
	if len(scope) == 0 {
		scope = json.RawMessage(`{}`)
	}
	if !json.Valid(scope) {
		return nil, fmt.Errorf("%w：scope 不是合法 JSON", ErrCampaignInvalid)
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "active"
	}
	c, err := scanCampaign(d.QueryRow(`UPDATE campaigns
SET goal=$2, scope=$3::jsonb, phase=$4, status=$5, starts_at=$6, ends_at=$7, credential_attempt_budget=$8
WHERE id=$1 RETURNING `+campaignCols,
		id, strings.TrimSpace(goal), string(scope), strings.TrimSpace(phase), status, startsAt, endsAt, credentialBudget))
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("%w：战役不存在", ErrCampaignInvalid)
	}
	return c, nil
}

// ListCampaignPhases 按 position 列出一个战役的阶段。
func (d *DB) ListCampaignPhases(campaignID int64) ([]*CampaignPhase, error) {
	rows, err := d.Query(`SELECT `+campaignPhaseCols+` FROM campaign_phases
WHERE campaign_id=$1 ORDER BY position, id`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CampaignPhase{}
	for rows.Next() {
		p, err := scanCampaignPhase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateCampaignPhase 新建阶段；同名不唯一（阶段是标签，不是主键）。
func (d *DB) CreateCampaignPhase(campaignID int64, name string, position int, status, note string, startsAt, endsAt *time.Time) (*CampaignPhase, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w：阶段名不能为空", ErrCampaignInvalid)
	}
	if strings.TrimSpace(status) == "" {
		status = "planned"
	}
	return scanCampaignPhase(d.QueryRow(`INSERT INTO campaign_phases(campaign_id,name,position,status,note,starts_at,ends_at)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+campaignPhaseCols,
		campaignID, name, position, strings.TrimSpace(status), strings.TrimSpace(note), startsAt, endsAt))
}

// UpdateCampaignPhase 整体更新一个阶段（不改归属性）。
func (d *DB) UpdateCampaignPhase(campaignID, phaseID int64, name string, position int, status, note string, startsAt, endsAt *time.Time) (*CampaignPhase, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w：阶段名不能为空", ErrCampaignInvalid)
	}
	if strings.TrimSpace(status) == "" {
		status = "planned"
	}
	return scanCampaignPhase(d.QueryRow(`UPDATE campaign_phases
SET name=$3, position=$4, status=$5, note=$6, starts_at=$7, ends_at=$8
WHERE id=$2 AND campaign_id=$1 RETURNING `+campaignPhaseCols,
		campaignID, phaseID, name, position, strings.TrimSpace(status), strings.TrimSpace(note), startsAt, endsAt))
}

// DeleteCampaignPhase 删除阶段；任务的 campaign_phase_id 由 FK SET NULL 自动清空。
func (d *DB) DeleteCampaignPhase(campaignID, phaseID int64) (int64, error) {
	res, err := d.Exec(`DELETE FROM campaign_phases WHERE id=$2 AND campaign_id=$1`, campaignID, phaseID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetTaskCampaignPhase 把一个任务挂到阶段（0 = 取消归属）。任务必须已属于该战役。
func (d *DB) SetTaskCampaignPhase(campaignID, taskID, phaseID int64) (int64, error) {
	if phaseID > 0 {
		var ok bool
		if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM campaign_phases WHERE id=$1 AND campaign_id=$2)`, phaseID, campaignID).Scan(&ok); err != nil {
			return 0, err
		}
		if !ok {
			return 0, fmt.Errorf("%w：阶段不属于该战役", ErrCampaignInvalid)
		}
	}
	var phase any
	if phaseID > 0 {
		phase = phaseID
	}
	res, err := d.Exec(`UPDATE tasks SET campaign_phase_id=$3
WHERE id=$2 AND campaign_id=$1 AND deleted_at IS NULL`, campaignID, taskID, phase)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CampaignTaskPhases 返回战役内 task_id → phase_id 的映射。
func (d *DB) CampaignTaskPhases(campaignID int64) (map[int64]int64, error) {
	rows, err := d.Query(`SELECT id, COALESCE(campaign_phase_id,0) FROM tasks
WHERE campaign_id=$1 AND deleted_at IS NULL`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var tid, pid int64
		if err := rows.Scan(&tid, &pid); err != nil {
			return nil, err
		}
		out[tid] = pid
	}
	return out, rows.Err()
}

// CampaignCredentialBudget resolves the campaign-level credential budget for a
// task. ok=false means the task has no campaign or the campaign inherits (-1).
func (d *DB) CampaignCredentialBudget(taskID int64) (int, bool) {
	if taskID <= 0 {
		return 0, false
	}
	var n int
	if err := d.QueryRow(`SELECT COALESCE(c.credential_attempt_budget,-1)
FROM tasks t JOIN campaigns c ON c.id=t.campaign_id
WHERE t.id=$1 AND t.deleted_at IS NULL`, taskID).Scan(&n); err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
