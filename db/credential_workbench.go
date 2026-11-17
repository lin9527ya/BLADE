package db

import (
	"fmt"
	"strings"
	"time"
)

// 凭据工作台读模型：跨任务的凭据列表 + 目标 + 最近复核 + 动用次数。
//
// 权威数据仍在 exploration_nodes.payload；credential_targets 只是可索引投影，
// credential_checks 是复核流水。这里把三者拼成页面需要的形状，不写任何新状态。

// CredentialWorkbenchFilter 是凭据工作台的筛选条件。零值 = 全部。
type CredentialWorkbenchFilter struct {
	TaskID     int64
	CompanyID  int64
	CampaignID int64
	State      string
	Host       string
	Service    string
	Lifecycle  string
	Query      string
	Verified   *bool
	Page       int
	Size       int
}

// CredentialWorkbenchItem 是一行凭据。
type CredentialWorkbenchItem struct {
	ID            int64              `json:"id"`
	TaskID        int64              `json:"task_id"`
	CampaignID    int64              `json:"campaign_id,omitempty"`
	TaskName      string             `json:"task_name"`
	Summary       string             `json:"summary,omitempty"`
	Kind          string             `json:"kind,omitempty"`
	Username      string             `json:"username,omitempty"`
	Fingerprint   string             `json:"fingerprint,omitempty"`
	State         string             `json:"state"`
	Lifecycle     string             `json:"lifecycle"`
	Targets       []CredentialTarget `json:"targets,omitempty"`
	LastCheckedAt *time.Time         `json:"last_checked_at,omitempty"`
	LastResult    string             `json:"last_result,omitempty"`
	UseCount      int                `json:"use_count"`
	Secret        string             `json:"secret,omitempty"`
	CreatedAt     time.Time          `json:"created_at"`
}

// ListCredentialWorkbench 返回筛选后的凭据与总数。
//
// 分页在 Go 侧完成：lifecycle 依赖 targets/checks 的组合，无法直接写进 SQL。
// 为避免全表扫描，粗筛（任务/公司/战役/state/host/service/q）在 SQL 完成并封顶
// maxCredentialWorkbenchRows；更细的 lifecycle 过滤在内存进行。
func (d *DB) ListCredentialWorkbench(f CredentialWorkbenchFilter, includeSecret bool) ([]*CredentialWorkbenchItem, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size <= 0 {
		f.Size = 50
	}
	if f.Size > 200 {
		f.Size = 200
	}
	conds := []string{"n.kind='credential'", "t.deleted_at IS NULL"}
	args := []any{}
	if f.TaskID > 0 {
		args = append(args, f.TaskID)
		conds = append(conds, fmt.Sprintf("t.id=$%d", len(args)))
	}
	if f.CompanyID > 0 {
		args = append(args, f.CompanyID)
		conds = append(conds, fmt.Sprintf("t.company_id=$%d", len(args)))
	}
	if f.CampaignID > 0 {
		args = append(args, f.CampaignID)
		conds = append(conds, fmt.Sprintf("t.campaign_id=$%d", len(args)))
	}
	if s := strings.TrimSpace(f.State); s != "" {
		args = append(args, s)
		conds = append(conds, fmt.Sprintf("n.state=$%d", len(args)))
	}
	if h := strings.TrimSpace(f.Host); h != "" {
		args = append(args, "%"+strings.ToLower(h)+"%")
		conds = append(conds, fmt.Sprintf("EXISTS (SELECT 1 FROM credential_targets ct WHERE ct.node_id=n.id AND ct.host ILIKE $%d)", len(args)))
	}
	if s := strings.TrimSpace(f.Service); s != "" {
		args = append(args, "%"+strings.ToLower(s)+"%")
		conds = append(conds, fmt.Sprintf("EXISTS (SELECT 1 FROM credential_targets ct WHERE ct.node_id=n.id AND ct.service ILIKE $%d)", len(args)))
	}
	if f.Verified != nil {
		if *f.Verified {
			conds = append(conds, "EXISTS (SELECT 1 FROM credential_targets ct WHERE ct.node_id=n.id AND ct.verified)")
		} else {
			conds = append(conds, "NOT EXISTS (SELECT 1 FROM credential_targets ct WHERE ct.node_id=n.id AND ct.verified)")
		}
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+q+"%")
		conds = append(conds, fmt.Sprintf("(n.payload::text ILIKE $%d OR COALESCE(n.origin,'') ILIKE $%d)", len(args), len(args)))
	}
	rows, err := d.Query(`SELECT n.id, n.state, n.payload, n.created_at,
       t.id, COALESCE(NULLIF(t.name,''), NULLIF(t.goal,''), t.description), COALESCE(t.campaign_id,0)
FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id
WHERE `+strings.Join(conds, " AND ")+`
ORDER BY n.id DESC LIMIT 2000`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	type raw struct {
		nodeID, taskID, campaignID int64
		state, taskName            string
		payload                    []byte
		createdAt                  time.Time
	}
	raws := []raw{}
	ids := []int64{}
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.nodeID, &r.state, &r.payload, &r.createdAt, &r.taskID, &r.taskName, &r.campaignID); err != nil {
			return nil, 0, err
		}
		raws = append(raws, r)
		ids = append(ids, r.nodeID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(ids) == 0 {
		return []*CredentialWorkbenchItem{}, 0, nil
	}
	targets, err := d.credentialTargetsByNode(ids)
	if err != nil {
		return nil, 0, err
	}
	checks, err := d.latestCredentialChecks(ids)
	if err != nil {
		return nil, 0, err
	}
	uses, err := d.credentialUseCounts(ids)
	if err != nil {
		return nil, 0, err
	}
	all := make([]*CredentialWorkbenchItem, 0, len(raws))
	for _, r := range raws {
		n := &Node{ID: r.nodeID, Kind: KindCredential, State: r.state, Payload: r.payload}
		cv := ParseCredential(n)
		item := &CredentialWorkbenchItem{
			ID: r.nodeID, TaskID: r.taskID, CampaignID: r.campaignID, TaskName: r.taskName,
			Summary: cv.Summary, Kind: cv.Kind, Username: cv.Username, Fingerprint: cv.Fingerprint(),
			State: r.state, Targets: targets[r.nodeID], UseCount: uses[r.nodeID], CreatedAt: r.createdAt,
		}
		if includeSecret {
			item.Secret = cv.Secret
		}
		if chk := checks[r.nodeID]; chk != nil {
			item.LastCheckedAt = &chk.CreatedAt
			item.LastResult = chk.Result
		}
		item.Lifecycle = ComputeCredentialLifecycle(CredentialLifecycleInput{
			State: r.state, Targets: item.Targets, LastResult: item.LastResult,
			LastCheckAt: item.LastCheckedAt,
		})
		if lc := strings.ToLower(strings.TrimSpace(f.Lifecycle)); lc != "" && lc != "all" && item.Lifecycle != lc {
			continue
		}
		all = append(all, item)
	}
	total := len(all)
	start := (f.Page - 1) * f.Size
	if start >= total {
		return []*CredentialWorkbenchItem{}, total, nil
	}
	end := start + f.Size
	if end > total {
		end = total
	}
	return all[start:end], total, nil
}

func (d *DB) credentialTargetsByNode(ids []int64) (map[int64][]CredentialTarget, error) {
	marks, args := inMarks(ids)
	rows, err := d.Query(`SELECT node_id, host, port, service, protocol, verified, validation_status, observed_at, valid_until
FROM credential_targets WHERE node_id IN (`+marks+`) ORDER BY node_id, ordinal`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]CredentialTarget{}
	for rows.Next() {
		var nodeID int64
		var t CredentialTarget
		var observed, valid *time.Time
		if err := rows.Scan(&nodeID, &t.Host, &t.Port, &t.Service, &t.Protocol, &t.Verified, &t.ValidationStatus, &observed, &valid); err != nil {
			return nil, err
		}
		if observed != nil {
			t.ObservedAt = observed.Format(time.RFC3339)
		}
		if valid != nil {
			t.ValidUntil = valid.Format(time.RFC3339)
		}
		out[nodeID] = append(out[nodeID], t)
	}
	return out, rows.Err()
}

func inMarks(ids []int64) (string, []any) {
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	return strings.Join(marks, ","), args
}

func (d *DB) latestCredentialChecks(ids []int64) (map[int64]*CredentialCheckItem, error) {
	marks, args := inMarks(ids)
	rows, err := d.Query(`SELECT DISTINCT ON (node_id) node_id, result, created_at
FROM credential_checks WHERE node_id IN (`+marks+`) ORDER BY node_id, created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*CredentialCheckItem{}
	for rows.Next() {
		chk := &CredentialCheckItem{}
		if err := rows.Scan(&chk.NodeID, &chk.Result, &chk.CreatedAt); err != nil {
			return nil, err
		}
		out[chk.NodeID] = chk
	}
	return out, rows.Err()
}

func (d *DB) credentialUseCounts(ids []int64) (map[int64]int, error) {
	marks, args := inMarks(ids)
	rows, err := d.Query(`SELECT dst_id, COUNT(*) FROM exploration_edges
WHERE rel='uses' AND dst_id IN (`+marks+`) GROUP BY dst_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
