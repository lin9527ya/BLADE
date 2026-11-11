package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// 全局战果台账：把 foothold / credential / artifact / footprint 四类探索节点从
// 「任务内部视图」提升为跨任务可检索、可交接的账本。凭据只输出身份/指纹/可登目标，
// 明文永远不经过这里（仍走受控 reveal 接口并留痕）。

// LootFilter 是台账的只读筛选条件。零值 = 全部（四类战果、所有任务）。
type LootFilter struct {
	Kind       string // foothold | credential | artifact | footprint；空/all = 全部
	State      string // 节点状态；空 = 全部
	TaskID     int64
	CompanyID  int64
	CampaignID int64
}

func (f LootFilter) kindClause() (string, []any) {
	kind := strings.TrimSpace(f.Kind)
	switch kind {
	case "", "all":
		return "n.kind IN ('foothold','credential','artifact','footprint')", nil
	case "foothold", "credential", "artifact", "footprint":
		return "n.kind = $1", []any{kind}
	default:
		return "FALSE", nil
	}
}

type lootRow struct {
	node   *Node
	taskID int64
	task   string
	desc   string
	camp   int64
}

// LootItem 是台账的一行。字段按 kind 填充；凭据的 Secret 永不出现。
type LootItem struct {
	ID              int64     `json:"id"`
	Kind            string    `json:"kind"`
	State           string    `json:"state"`
	CreatedAt       time.Time `json:"created_at"`
	TaskID          int64     `json:"task_id"`
	TaskName        string    `json:"task_name"`
	TaskDescription string    `json:"task_description,omitempty"`
	CampaignID      int64     `json:"campaign_id,omitempty"`

	Summary  string `json:"summary,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Host     string `json:"host,omitempty"`
	Locator  string `json:"locator,omitempty"`
	URL      string `json:"url,omitempty"`
	Category string `json:"category,omitempty"`
	Exposure string `json:"exposure,omitempty"`
	Where    string `json:"where,omitempty"`
	Signals  string `json:"signals,omitempty"`
	Cleanup  string `json:"cleanup,omitempty"`

	Access       string `json:"access,omitempty"`
	Privilege    string `json:"privilege,omitempty"`
	Protocol     string `json:"protocol,omitempty"`
	Port         int    `json:"port,omitempty"`
	CredentialID int64  `json:"credential_id,omitempty"`

	Username       string             `json:"username,omitempty"`
	CredentialKind string             `json:"credential_kind,omitempty"`
	Fingerprint    string             `json:"fingerprint,omitempty"`
	Targets        []CredentialTarget `json:"targets,omitempty"`
	VerifiedCount  int                `json:"verified_count,omitempty"`
}

func lootItemFromRow(r lootRow) LootItem {
	n := r.node
	item := LootItem{
		ID: n.ID, Kind: n.Kind, State: n.State, CreatedAt: n.CreatedAt,
		TaskID: r.taskID, TaskName: r.task, TaskDescription: r.desc, CampaignID: r.camp,
	}
	switch n.Kind {
	case KindFoothold:
		v := ParseFoothold(n)
		item.Summary, item.Detail = v.Summary, v.Detail
		item.Host, item.URL, item.Protocol, item.Port = v.Host, v.URL, v.Protocol, v.Port
		item.Access, item.Privilege, item.CredentialID = v.Access, v.Privilege, v.CredentialID
	case KindCredential:
		v := ParseCredential(n)
		item.Summary = v.Summary
		item.Username, item.CredentialKind, item.Fingerprint = v.Username, v.Kind, v.Fingerprint()
		item.Targets = v.Targets
		for _, t := range v.Targets {
			if t.Verified {
				item.VerifiedCount++
			}
		}
	case KindArtifact:
		v := ParseArtifact(n)
		item.Summary, item.Detail = v.Summary, v.Detail
		item.Host, item.Locator, item.URL, item.Cleanup = v.Host, v.Locator, v.URL, v.Cleanup
		item.Category = ArtifactKindLabel(v.Kind)
	case KindFootprint:
		v := ParseFootprint(n)
		item.Summary, item.Detail = v.Summary, v.Detail
		item.Host, item.Locator, item.Where, item.Cleanup = v.Host, v.Locator, strings.Join(v.Where, "、"), v.Cleanup
		item.Category, item.Exposure, item.Signals = FootprintCategoryLabel(v.Category), FootprintExposureLabel(v.Exposure), strings.Join(v.Signals, "、")
	}
	return item
}

func (d *DB) lootQuery(f LootFilter, suffix string, extraArgs []any) (string, []any) {
	kind, args := f.kindClause()
	where := []string{kind, "t.deleted_at IS NULL"}
	if s := strings.TrimSpace(f.State); s != "" {
		args = append(args, s)
		where = append(where, fmt.Sprintf("n.state = $%d", len(args)))
	}
	if f.TaskID > 0 {
		args = append(args, f.TaskID)
		where = append(where, fmt.Sprintf("t.id = $%d", len(args)))
	}
	if f.CampaignID > 0 {
		args = append(args, f.CampaignID)
		where = append(where, fmt.Sprintf("t.campaign_id = $%d", len(args)))
	}
	if f.CompanyID > 0 {
		args = append(args, f.CompanyID)
		where = append(where, fmt.Sprintf(`EXISTS (SELECT 1 FROM task_scope ts WHERE ts.task_id=t.id AND ts.kind='company' AND ts.company_id=$%d)`, len(args)))
	}
	q := `SELECT n.id, n.kind, n.state, n.payload, n.created_at,
       t.id, COALESCE(t.name,''), COALESCE(t.description,''), COALESCE(t.campaign_id,0)
FROM exploration_nodes n
JOIN explorations e ON e.id = n.exploration_id
JOIN tasks t ON t.exploration_id = e.id
WHERE ` + strings.Join(where, " AND ") + ` ` + suffix
	return q, append(args, extraArgs...)
}

func scanLootRows(rows *Rows) ([]LootItem, error) {
	out := []LootItem{}
	for rows.Next() {
		r := lootRow{node: &Node{}}
		var payload []byte
		if err := rows.Scan(&r.node.ID, &r.node.Kind, &r.node.State, &payload, &r.node.CreatedAt,
			&r.taskID, &r.task, &r.desc, &r.camp); err != nil {
			return nil, err
		}
		r.node.Payload = payload
		out = append(out, lootItemFromRow(r))
	}
	return out, rows.Err()
}

// ListLootLedger returns one page plus the total count.
func (d *DB) ListLootLedger(ctx context.Context, f LootFilter, page, size int) ([]LootItem, int, error) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	countSQL, countArgs := d.lootQuery(f, "", nil)
	// Count only needs the joins/where; reuse a COUNT over the same where clause.
	countSQL = strings.Replace(countSQL, `SELECT n.id, n.kind, n.state, n.payload, n.created_at,
       t.id, COALESCE(t.name,''), COALESCE(t.description,''), COALESCE(t.campaign_id,0)`, `SELECT count(*)`, 1)
	var total int
	if err := d.QueryRowContext(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q, args := d.lootQuery(f, `ORDER BY n.id DESC LIMIT $`+fmt.Sprint(len(countArgs)+1)+` OFFSET $`+fmt.Sprint(len(countArgs)+2), []any{size, (page - 1) * size})
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanLootRows(rows)
	return items, total, err
}

// IterateLootLedger streams the full filtered ledger for CSV/JSON export.
func (d *DB) IterateLootLedger(ctx context.Context, f LootFilter, fn func(LootItem) error) error {
	q, args := d.lootQuery(f, `ORDER BY n.id DESC`, nil)
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		r := lootRow{node: &Node{}}
		var payload []byte
		if err := rows.Scan(&r.node.ID, &r.node.Kind, &r.node.State, &payload, &r.node.CreatedAt,
			&r.taskID, &r.task, &r.desc, &r.camp); err != nil {
			return err
		}
		r.node.Payload = payload
		if err := fn(lootItemFromRow(r)); err != nil {
			return err
		}
	}
	return rows.Err()
}
