package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 据点工作台：黑板上 kind='foothold' 的读模型 + 关联隧道。
// 据点本身没有"会话"——它是一条可复用的登录方式；隧道是挂在它上面的长执行。

// FootholdWorkbenchFilter 零值 = 全部。
type FootholdWorkbenchFilter struct {
	TaskID     int64
	CompanyID  int64
	CampaignID int64
	State      string
	Host       string
	Query      string
	Page       int
	Size       int
}

// FootholdTunnelView 是挂在据点上的隧道。
type FootholdTunnelView struct {
	ExecutionID int64      `json:"execution_id"`
	LocalPort   int        `json:"local_port,omitempty"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
}

// FootholdWorkbenchItem 是一行据点。
type FootholdWorkbenchItem struct {
	ID                    int64                `json:"id"`
	TaskID                int64                `json:"task_id"`
	CampaignID            int64                `json:"campaign_id,omitempty"`
	TaskName              string               `json:"task_name"`
	Summary               string               `json:"summary,omitempty"`
	Access                string               `json:"access,omitempty"`
	Privilege             string               `json:"privilege,omitempty"`
	Protocol              string               `json:"protocol,omitempty"`
	Host                  string               `json:"host,omitempty"`
	Port                  int                  `json:"port,omitempty"`
	URL                   string               `json:"url,omitempty"`
	CredentialID          int64                `json:"credential_id,omitempty"`
	CredentialFingerprint string               `json:"credential_fingerprint,omitempty"`
	State                 string               `json:"state"`
	LastUsedAt            *time.Time           `json:"last_used_at,omitempty"`
	Tunnels               []FootholdTunnelView `json:"tunnels,omitempty"`
	CreatedAt             time.Time            `json:"created_at"`
}

// ListFootholdWorkbench 返回据点列表与总数。
func (d *DB) ListFootholdWorkbench(f FootholdWorkbenchFilter) ([]*FootholdWorkbenchItem, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size <= 0 {
		f.Size = 50
	}
	if f.Size > 200 {
		f.Size = 200
	}
	conds := []string{"n.kind='foothold'", "t.deleted_at IS NULL"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.TaskID > 0 {
		add("t.id=$%d", f.TaskID)
	}
	if f.CompanyID > 0 {
		add("t.company_id=$%d", f.CompanyID)
	}
	if f.CampaignID > 0 {
		add("t.campaign_id=$%d", f.CampaignID)
	}
	if s := strings.TrimSpace(f.State); s != "" {
		add("n.state=$%d", s)
	}
	if h := strings.TrimSpace(f.Host); h != "" {
		add("(n.payload->>'host') ILIKE $%d", "%"+strings.ToLower(h)+"%")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		add("(n.payload::text ILIKE $%d OR COALESCE(n.origin,'') ILIKE $%d)", "%"+q+"%")
		// add() only appends one arg; fix the second placeholder manually.
		conds[len(conds)-1] = fmt.Sprintf("(n.payload::text ILIKE $%d OR COALESCE(n.origin,'') ILIKE $%d)", len(args), len(args))
	}
	where := strings.Join(conds, " AND ")
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Size, (f.Page-1)*f.Size)
	rows, err := d.Query(`SELECT n.id, n.state, n.payload, n.created_at,
       t.id, COALESCE(NULLIF(t.name,''), NULLIF(t.goal,''), t.description), COALESCE(t.campaign_id,0)
FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id
WHERE `+where+`
ORDER BY n.id DESC LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []*FootholdWorkbenchItem{}
	ids := []int64{}
	for rows.Next() {
		var id, taskID, campaignID int64
		var state, taskName string
		var payload []byte
		var createdAt time.Time
		if err := rows.Scan(&id, &state, &payload, &createdAt, &taskID, &taskName, &campaignID); err != nil {
			return nil, 0, err
		}
		fv := ParseFoothold(&Node{ID: id, Kind: KindFoothold, State: state, Payload: payload})
		item := &FootholdWorkbenchItem{
			ID: id, TaskID: taskID, CampaignID: campaignID, TaskName: taskName,
			Summary: fv.Summary, Access: fv.Access, Privilege: fv.Privilege,
			Protocol: fv.Protocol, Host: fv.Host, Port: fv.Port, URL: fv.URL,
			CredentialID: fv.CredentialID, State: state, CreatedAt: createdAt,
		}
		items = append(items, item)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(items) == 0 {
		return items, total, nil
	}
	tunnels, lastUsed, err := d.footholdActivity(ids)
	if err != nil {
		return nil, 0, err
	}
	for _, item := range items {
		item.Tunnels = tunnels[item.ID]
		item.LastUsedAt = lastUsed[item.ID]
	}
	return items, total, nil
}

// footholdActivity 批量取关联隧道与最近使用时间。
func (d *DB) footholdActivity(ids []int64) (map[int64][]FootholdTunnelView, map[int64]*time.Time, error) {
	marks, args := inMarks(ids)
	textIDs := make([]string, len(ids))
	for i, id := range ids {
		textIDs[i] = fmt.Sprint(id)
	}
	args = append(args, textIDs)
	tunnelArg := len(args)
	rows, err := d.Query(`SELECT id, status, started_at, created_at, meta
FROM tool_executions
WHERE (tool='tunnel_open' OR description LIKE '隧道：%')
  AND COALESCE(meta->>'foothold_id','') = ANY($`+fmt.Sprint(tunnelArg)+`::text[])
ORDER BY id DESC`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	tunnels := map[int64][]FootholdTunnelView{}
	lastUsed := map[int64]*time.Time{}
	for rows.Next() {
		var id int64
		var status string
		var started, created *time.Time
		var meta []byte
		if err := rows.Scan(&id, &status, &started, &created, &meta); err != nil {
			return nil, nil, err
		}
		var m struct {
			FootholdID int64 `json:"foothold_id"`
			LocalPort  int   `json:"local_port"`
		}
		_ = json.Unmarshal(meta, &m)
		if m.FootholdID == 0 {
			continue
		}
		if ExecutionActive(status) {
			tunnels[m.FootholdID] = append(tunnels[m.FootholdID], FootholdTunnelView{
				ExecutionID: id, LocalPort: m.LocalPort, Status: status, StartedAt: started,
			})
		}
		at := started
		if at == nil {
			at = created
		}
		if at != nil && (lastUsed[m.FootholdID] == nil || at.After(*lastUsed[m.FootholdID])) {
			lastUsed[m.FootholdID] = at
		}
	}
	_ = marks
	return tunnels, lastUsed, rows.Err()
}

// TunnelItem 是隧道面板的一行。
type TunnelItem struct {
	ExecutionID int64      `json:"execution_id"`
	TaskID      int64      `json:"task_id"`
	TaskName    string     `json:"task_name,omitempty"`
	Description string     `json:"description,omitempty"`
	Status      string     `json:"status"`
	LocalPort   int        `json:"local_port,omitempty"`
	FootholdID  int64      `json:"foothold_id,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	// Listening 由 server 侧对 127.0.0.1:<port> 做一次本地 TCP 探测填入，不触碰目标。
	Listening bool `json:"listening"`
}

// ListTunnels 列出隧道执行（最新的在前）。
func (d *DB) ListTunnels(limit int) ([]*TunnelItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.Query(`SELECT id, task_id, COALESCE(description,''), status, started_at, created_at, meta
FROM tool_executions
WHERE tool='tunnel_open' OR description LIKE '隧道：%'
ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TunnelItem{}
	for rows.Next() {
		var it TunnelItem
		var taskIDText string
		var meta []byte
		if err := rows.Scan(&it.ExecutionID, &taskIDText, &it.Description, &it.Status, &it.StartedAt, &it.CreatedAt, &meta); err != nil {
			return nil, err
		}
		it.TaskID = parseInt64(taskIDText)
		var m struct {
			FootholdID int64 `json:"foothold_id"`
			LocalPort  int   `json:"local_port"`
			TTLMs      int64 `json:"ttl_ms"`
		}
		_ = json.Unmarshal(meta, &m)
		it.FootholdID = m.FootholdID
		it.LocalPort = m.LocalPort
		if m.TTLMs > 0 {
			base := it.StartedAt
			if base == nil {
				base = &it.CreatedAt
			}
			exp := base.Add(time.Duration(m.TTLMs) * time.Millisecond)
			it.ExpiresAt = &exp
		}
		out = append(out, &it)
	}
	return out, rows.Err()
}

func parseInt64(s string) int64 {
	var n int64
	fmt.Sscan(strings.TrimSpace(s), &n)
	return n
}
