package db

import (
	"database/sql"
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
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CompanyID *int64    `json:"company_id,omitempty"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const campaignCols = `id, name, company_id, note, created_at, updated_at`

func scanCampaign(row interface{ Scan(...any) error }) (*Campaign, error) {
	c := &Campaign{}
	err := row.Scan(&c.ID, &c.Name, &c.CompanyID, &c.Note, &c.CreatedAt, &c.UpdatedAt)
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

// SetTaskCampaign 把任务归入/移出战役（campaignID 为 nil = 移出）。
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
