package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DBFinding is a row in the standalone findings table. It persists across task
// deletion unless the caller explicitly requests related finding cleanup.
type DBFinding struct {
	TrafficCount          int
	EvidenceVersion       int64
	ReportEvidenceVersion int64
	TrafficBindings       []FindingTrafficBinding // populated only for export

	ID              int64
	TaskID          *int64
	NodeID          *int64
	VulnClass       string
	Name            string // 漏洞名称(可读标题);为空时前端回退展示 VulnClass
	Severity        string
	Summary         string
	Evidence        string
	Worker          string
	AssetIDs        []int64
	Status          string
	Confidence      string // 置信度标定 high/medium/low;"" = 未标定
	Counterevidence string // ruled_out 的反证(点名控制点)
	ProofGap        string // open_proof_gap 的缺口说明
	Report          string // 详细报告(Markdown);仅 GetFinding 填充,列表查询不带
	CreatedAt       time.Time
	TaskDescription string // populated via LEFT JOIN on tasks
}

// Finding triage states (findings.status).
const (
	FindingPending       = "pending"        // 待处理
	FindingInProgress    = "in_progress"    // 处理中
	FindingConfirmed     = "confirmed"      // 已确认(真实漏洞,未修复)
	FindingResolved      = "resolved"       // 已处理
	FindingFixed         = "fixed"          // 已修复
	FindingFalsePositive = "false_positive" // 误报
	FindingIgnored       = "ignored"        // 忽略
	FindingDuplicate     = "duplicate"      // 重复
	FindingRiskAccepted  = "risk_accepted"  // 风险接受
	FindingRetracted     = "retracted"      // 已撤回(复测推翻;墓碑保留,原因见 retract_reason)
	FindingRuledOut      = "ruled_out"      // 已排除(反证闭合;counterevidence 点名控制点)
	FindingOpenProofGap  = "open_proof_gap" // 存疑待补证(proof_gap 写清缺什么;不得视为安全)
)

// FindingPromotableForKnowledge 判断一条发现的 triage 状态是否允许沉淀成公司知识（P23）。
// 被推翻/排除/重复的结论沉淀出去会误导后来人，一律拒；其余状态（含待处理）由调用方
// 决定要不要提示——这里只拦"与结论本身矛盾"的那些。
func FindingPromotableForKnowledge(status string) error {
	switch status {
	case FindingFalsePositive, FindingRetracted, FindingRuledOut, FindingDuplicate:
		return fmt.Errorf("发现当前状态是 %s（误报/已撤回/已排除/重复）：被推翻的结论不能沉淀为知识", status)
	}
	return nil
}

// FindingStatusByNodeID 取某个 finding 节点对应的独立 findings 行状态（"" = 没有对应行）。
// 沉淀门（P23）用它：**节点状态不反映 triage 状态**——撤回只翻 findings 行、图节点仍是
// confirmed，只判节点状态会把被推翻的结论放出去（验收抓出来的）。
func (s *ExplorationStore) FindingStatusByNodeID(nodeID int64) (string, error) {
	var st string
	err := s.db.QueryRow(`SELECT COALESCE(status,'') FROM findings WHERE node_id=$1`, nodeID).Scan(&st)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return st, err
}

// ValidFindingStatus reports whether s is a known triage state.
func ValidFindingStatus(s string) bool {
	switch s {
	case FindingPending, FindingInProgress, FindingConfirmed, FindingResolved, FindingFixed,
		FindingFalsePositive, FindingIgnored, FindingDuplicate, FindingRiskAccepted, FindingRetracted,
		FindingRuledOut, FindingOpenProofGap:
		return true
	}
	return false
}

// Confidence levels for the honest-calibration label on findings and candidates.
// "" means 未标定 (legacy rows and reports that predate the field) — the report
// names those explicitly instead of guessing.
const (
	ConfidenceHigh   = "high"   // 可复现的触发链（有对照/可重放）
	ConfidenceMedium = "medium" // 直接观察到强异常，未构造完整对照或未确证可利用
	ConfidenceLow    = "low"    // 据指纹/版本/现象推断（inferred），未直接触发
)

// ValidConfidence reports whether c is a known confidence label ("" = 未标定).
func ValidConfidence(c string) bool {
	switch c {
	case "", ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
		return true
	}
	return false
}

// Finding severity levels (findings.severity).
const (
	SeverityCritical = "critical" // 严重
	SeverityHigh     = "high"     // 高
	SeverityMedium   = "medium"   // 中
	SeverityLow      = "low"      // 低
)

// ValidSeverity reports whether s is a known severity level.
func ValidSeverity(s string) bool {
	switch s {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return true
	}
	return false
}

// AddFinding inserts a finding into the standalone findings table. taskID and
// nodeID may be 0 (stored as NULL). name may be "" (frontend falls back to
// vulnclass). Returns the new finding id.
func (d *DB) AddFinding(taskID, nodeID int64, vulnclass, name, severity, summary, evidence, worker string, assetIDs []int64) (int64, error) {
	aidsJSON, _ := json.Marshal(assetIDs)
	if assetIDs == nil {
		aidsJSON = []byte("[]")
	}
	var tid, nid *int64
	if taskID > 0 {
		tid = &taskID
	}
	if nodeID > 0 {
		nid = &nodeID
	}
	var id int64
	err := d.QueryRow(
		`INSERT INTO findings (task_id, node_id, vulnclass, name, severity, summary, evidence, worker, asset_ids)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		tid, nid, vulnclass, name, severity, summary, evidence, worker, string(aidsJSON),
	).Scan(&id)
	return id, err
}

// findingSelectCols is the column list (with task_description join) every finding
// list query selects, so scanFinding stays in sync across callers.
const findingSelectCols = `f.id, f.task_id, f.node_id, f.vulnclass, COALESCE(f.name, ''), f.severity, f.summary,
	       f.evidence, f.worker, f.asset_ids, COALESCE(f.status, 'pending'), f.created_at,
	       COALESCE(t.description, '') AS task_description, f.evidence_version, f.report_evidence_version,
 (SELECT count(*) FROM finding_traffic_bindings b WHERE b.finding_id=f.id),
	       COALESCE(f.confidence, ''), COALESCE(f.counterevidence, ''), COALESCE(f.proof_gap, '')`

// scanFindings materializes rows selected via findingSelectCols.
func scanFindings(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]*DBFinding, error) {
	var out []*DBFinding
	for rows.Next() {
		f := &DBFinding{}
		var aidsJSON string
		if err := rows.Scan(&f.ID, &f.TaskID, &f.NodeID, &f.VulnClass, &f.Name, &f.Severity,
			&f.Summary, &f.Evidence, &f.Worker, &aidsJSON, &f.Status, &f.CreatedAt, &f.TaskDescription, &f.EvidenceVersion, &f.ReportEvidenceVersion, &f.TrafficCount,
			&f.Confidence, &f.Counterevidence, &f.ProofGap); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(aidsJSON), &f.AssetIDs)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListFindings returns all findings (newest first), joined with task description.
// Kept for the dashboard's summary; the paginated 发现 page uses ListFindingsPage.
func (d *DB) ListFindings(limit int) ([]*DBFinding, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := d.Query(`
		SELECT `+findingSelectCols+`
		FROM findings f
		LEFT JOIN tasks t ON f.task_id = t.id
		ORDER BY f.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFindings(rows)
}

// FindingFilter narrows a paginated findings query. Empty-string fields mean "no
// filter on that column". Sort is "severity" (severity desc, then newest) or
// anything else (newest first).
type FindingFilter struct {
	Severity  string // high | medium | low
	Status    string // pending | false_positive | ignored | resolved
	VulnClass string
	TaskID    string // 任务 id(字符串形式;空/非法 = 不按任务筛选)
	Query     string // 名称/类型/摘要/证据/报告正文的模糊检索关键词
	Sort      string // "severity" | "time"
	// AssetScope 是资产树的节点 key(a:<id> / c:<id> / r:<domain> / __none__),
	// 选中一个节点等于选中它的整棵子树。空 = 不按资产筛选。
	AssetScope string

	// 下面三个由 applyAssetScope 从 AssetScope 解析而来,调用方不用设置。
	assetIDs  []int64 // 子树里所有资产 id
	assetNone bool    // 只要「未关联资产」的发现
	assetMiss bool    // 选中的节点在当前筛选下不存在 → 结果恒空
}

// FindingUnassignedTask is the task filter sentinel for findings whose task is
// absent. That includes rows created without a task and rows retained after their
// originating task was deleted (the findings FK is ON DELETE SET NULL).
const FindingUnassignedTask = "__unassigned__"

// where builds the WHERE clause (shared by the page and count queries) plus its
// positional args. All values are parameterized; Query also escapes ILIKE
// wildcards so user input is always matched literally.
func (f FindingFilter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(col, val string) {
		if val == "" {
			return
		}
		args = append(args, val)
		conds = append(conds, fmt.Sprintf("f.%s = $%d", col, len(args)))
	}
	add("severity", f.Severity)
	add("status", f.Status)
	add("vulnclass", f.VulnClass)
	// task_id 是 bigint 列,按整数比较(不能走上面的文本 add);空/非法值忽略。
	if f.TaskID == FindingUnassignedTask {
		conds = append(conds, "(f.task_id IS NULL OR t.id IS NULL)")
	} else if tid, err := strconv.ParseInt(f.TaskID, 10, 64); err == nil && tid > 0 {
		args = append(args, tid)
		conds = append(conds, fmt.Sprintf("f.task_id = $%d", len(args)))
	}
	// 资产筛选:asset_ids 是 jsonb 数组,@> ANY(...) 能走 idx_findings_asset_ids。
	switch {
	case f.assetMiss:
		conds = append(conds, "FALSE")
	case f.assetNone:
		// 「未关联资产」= asset_ids 为空,或者里面的 id 一个都不在 assets 表里
		// (资产已被删除)。两类都进资产树的未关联桶,这里必须同样收下,否则桶上
		// 的计数会大于点开后能查到的条数。
		conds = append(conds, `(
			jsonb_array_length(COALESCE(f.asset_ids, '[]'::jsonb)) = 0
			OR NOT EXISTS (
				SELECT 1 FROM jsonb_array_elements_text(f.asset_ids) e(v)
				JOIN assets a ON a.id = e.v::bigint
			)
		)`)
	case len(f.assetIDs) > 0:
		args = append(args, assetIDContainments(f.assetIDs))
		conds = append(conds, fmt.Sprintf("f.asset_ids @> ANY($%d::jsonb[])", len(args)))
	}
	if query := strings.TrimSpace(f.Query); query != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
		args = append(args, "%"+escaped+"%")
		placeholder := fmt.Sprintf("$%d", len(args))
		conds = append(conds, fmt.Sprintf(`(
			COALESCE(f.name, '') ILIKE %s ESCAPE '\' OR
			f.vulnclass ILIKE %s ESCAPE '\' OR
			f.summary ILIKE %s ESCAPE '\' OR
			f.evidence ILIKE %s ESCAPE '\' OR
			COALESCE(f.report, '') ILIKE %s ESCAPE '\'
		)`, placeholder, placeholder, placeholder, placeholder, placeholder))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListFindingsPage returns one page of findings matching the filter, plus the
// total count of matching rows (for the frontend pager). page is 1-based.
func (d *DB) ListFindingsPage(f FindingFilter, page, pageSize int) ([]*DBFinding, int, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	f, err := d.applyAssetScope(f)
	if err != nil {
		return nil, 0, err
	}
	where, args := f.where()

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM findings f LEFT JOIN tasks t ON f.task_id=t.id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// Avoid overflowing (page-1)*pageSize for an arbitrarily large page number.
	// Once the requested page is beyond the exact count, no data query is needed.
	if total == 0 || page > (total-1)/pageSize+1 {
		return []*DBFinding{}, total, nil
	}

	order := "f.created_at DESC, f.id DESC"
	if f.Sort == "severity" {
		// critical > high > medium > low > 其它, then newest first.
		order = `CASE f.severity WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 WHEN 'low' THEN 1 ELSE 0 END DESC, f.created_at DESC, f.id DESC`
	}
	pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	q := fmt.Sprintf(`
		SELECT %s
		FROM findings f
		LEFT JOIN tasks t ON f.task_id = t.id%s
		ORDER BY %s
		LIMIT $%d OFFSET $%d`, findingSelectCols, where, order, len(args)+1, len(args)+2)
	rows, err := d.Query(q, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, err := scanFindings(rows)
	return out, total, err
}

// FindingGroup is one task-level bucket in the global findings view. TaskID is
// nil for both findings that never had a task and findings retained after task
// deletion; those records intentionally share one "unassigned/deleted" bucket.
type FindingGroup struct {
	TaskID          *int64    `json:"task_id"`
	TaskName        string    `json:"task_name"` // 可选任务名称;空=未命名
	TaskDescription string    `json:"task_description"`
	TaskStatus      string    `json:"task_status"`
	Count           int       `json:"count"`
	Critical        int       `json:"critical"`
	High            int       `json:"high"`
	Medium          int       `json:"medium"`
	Low             int       `json:"low"`
	LastFoundAt     time.Time `json:"last_found_at"`
}

// ListFindingGroups returns a page of task groups matching the same filters as
// ListFindingsPage. The group count and finding count are independent totals so
// clients can page groups without losing the exact export/selection count.
func (d *DB) ListFindingGroups(f FindingFilter, page, pageSize int) ([]FindingGroup, int, int, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}
	f, err := d.applyAssetScope(f)
	if err != nil {
		return nil, 0, 0, err
	}
	where, args := f.where()
	grouped := ` FROM findings f LEFT JOIN tasks t ON f.task_id=t.id` + where +
		` GROUP BY t.id, t.name, t.description, t.status, t.paused, t.queued`

	var groupTotal, findingTotal int
	countQuery := `SELECT COUNT(*), COALESCE(SUM(finding_count),0) FROM (` +
		`SELECT COUNT(*) AS finding_count` + grouped + `) grouped_findings`
	if err := d.QueryRow(countQuery, args...).Scan(&groupTotal, &findingTotal); err != nil {
		return nil, 0, 0, err
	}
	if groupTotal == 0 || page > (groupTotal-1)/pageSize+1 {
		return []FindingGroup{}, groupTotal, findingTotal, nil
	}

	order := "MAX(f.created_at) DESC, t.id DESC NULLS LAST"
	if f.Sort == "severity" {
		order = `MAX(CASE f.severity WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 WHEN 'low' THEN 1 ELSE 0 END) DESC, MAX(f.created_at) DESC, t.id DESC NULLS LAST`
	}
	pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	query := fmt.Sprintf(`SELECT t.id, COALESCE(t.name,''), COALESCE(t.description,''), COALESCE(
		CASE
			WHEN t.status IN ('done','failed','timeout') THEN t.status
			WHEN t.queued THEN 'queued'
			WHEN t.paused THEN 'paused'
			ELSE t.status
		END, ''),
		COUNT(*),
		COUNT(*) FILTER (WHERE f.severity='critical'),
		COUNT(*) FILTER (WHERE f.severity='high'),
		COUNT(*) FILTER (WHERE f.severity='medium'),
		COUNT(*) FILTER (WHERE f.severity='low'),
		MAX(f.created_at)%s
		ORDER BY %s LIMIT $%d OFFSET $%d`, grouped, order, len(args)+1, len(args)+2)
	rows, err := d.Query(query, pageArgs...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	groups := []FindingGroup{}
	for rows.Next() {
		var group FindingGroup
		var taskID sql.NullInt64
		if err := rows.Scan(&taskID, &group.TaskName, &group.TaskDescription, &group.TaskStatus, &group.Count,
			&group.Critical, &group.High, &group.Medium, &group.Low, &group.LastFoundAt); err != nil {
			return nil, 0, 0, err
		}
		if taskID.Valid {
			id := taskID.Int64
			group.TaskID = &id
		}
		groups = append(groups, group)
	}
	return groups, groupTotal, findingTotal, rows.Err()
}

// ErrFindingOriginUnavailable means a retained finding no longer has a live
// owning task and finding node from which a follow-up intent can be derived.
var ErrFindingOriginUnavailable = errors.New("finding origin is no longer available")

// AddFindingFollowUpIntent atomically creates a priority-10 human intent from a
// live finding node, copies that finding's asset anchors, records the
// finding --derived_from--> intent lineage edge, and persists its audit activity.
// The returned activity is the committed row and can be broadcast as-is without
// calling AppendActivity again.
func (s *ExplorationStore) AddFindingFollowUpIntent(findingID, findingNodeID int64, description string, audit Activity) (int64, Activity, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, Activity{}, err
	}
	defer tx.Rollback()

	var liveNodeID int64
	err = tx.QueryRow(`SELECT n.id
		FROM findings f
		JOIN tasks t ON t.id=f.task_id
		JOIN exploration_nodes n ON n.id=f.node_id AND n.exploration_id=t.exploration_id
		WHERE f.id=$1 AND f.node_id=$2 AND t.exploration_id=$3 AND n.kind='finding'
		FOR SHARE OF f, t, n`, findingID, findingNodeID, s.expID).Scan(&liveNodeID)
	if err == sql.ErrNoRows {
		return 0, Activity{}, ErrFindingOriginUnavailable
	}
	if err != nil {
		return 0, Activity{}, err
	}

	anchors := []int64{}
	anchorRows, err := tx.Query(`SELECT asset_id FROM exploration_anchors WHERE node_id=$1 ORDER BY asset_id`, liveNodeID)
	if err != nil {
		return 0, Activity{}, err
	}
	for anchorRows.Next() {
		var assetID int64
		if err := anchorRows.Scan(&assetID); err != nil {
			anchorRows.Close()
			return 0, Activity{}, err
		}
		anchors = append(anchors, assetID)
	}
	if err := anchorRows.Err(); err != nil {
		anchorRows.Close()
		return 0, Activity{}, err
	}
	if err := anchorRows.Close(); err != nil {
		return 0, Activity{}, err
	}

	payload := map[string]any{
		"summary":                description,
		"source_finding_id":      findingID,
		"source_finding_node_id": liveNodeID,
	}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, Activity{}, err
	}
	var intentID int64
	if err := tx.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin)
		VALUES ($1,'intent',$2,10,'open','human') RETURNING id`, s.expID, raw).Scan(&intentID); err != nil {
		return 0, Activity{}, err
	}
	if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id,asset_id)
		SELECT $1, asset_id FROM exploration_anchors WHERE node_id=$2
		ON CONFLICT DO NOTHING`, intentID, liveNodeID); err != nil {
		return 0, Activity{}, err
	}
	if _, err := tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id)
		VALUES ($1,$2,$3,$4)`, s.expID, liveNodeID, RelDerivedFrom, intentID); err != nil {
		return 0, Activity{}, err
	}

	audit.NodeID = &intentID
	if summary := strings.TrimSpace(audit.Summary); summary != "" {
		audit.Summary = fmt.Sprintf("%s #%d", summary, intentID)
	} else {
		audit.Summary = ""
	}
	metadata := audit.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	if err := tx.QueryRow(`
INSERT INTO activity(exploration_id, node_id, worker, kind, tool, tool_use_id, is_error, summary, detail, metadata, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7,NULLIF($8,''),NULLIF($9,''),$10,$11,$12,$13,$14)
RETURNING id, created_at`, s.expID, audit.NodeID, utf8Clean(audit.Worker), utf8Clean(audit.Kind), utf8Clean(audit.Tool), utf8Clean(audit.ToolUseID), audit.IsError,
		utf8Clean(audit.Summary), utf8Clean(audit.Detail), metadata, audit.InputTokens, audit.OutputTokens, audit.CacheReadTokens, audit.CacheWriteTokens).
		Scan(&audit.ID, &audit.CreatedAt); err != nil {
		return 0, Activity{}, err
	}
	audit.Metadata = metadata
	if err := tx.Commit(); err != nil {
		return 0, Activity{}, err
	}
	return intentID, audit, nil
}

// ListFindingsForExport returns findings for the 发现 page 导出功能，携带完整
// report 字段、不分页。ids 非空时按这批 finding id 精确导出(勾选导出),忽略
// filter;ids 为空时按 filter 导出(导出当前筛选/全部)。结果按严重等级降序、
// 再按时间倒序,与「导出汇总报告」的分组顺序一致。
func (d *DB) ListFindingsForExport(f FindingFilter, ids []int64) ([]*DBFinding, error) {
	const order = `ORDER BY CASE f.severity WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 WHEN 'low' THEN 1 ELSE 0 END DESC, f.created_at DESC`
	cols := findingSelectCols + `, COALESCE(f.report, '')`

	var q string
	var args []any
	if len(ids) > 0 {
		ph := make([]string, len(ids))
		for i, id := range ids {
			ph[i] = fmt.Sprintf("$%d", i+1)
			args = append(args, id)
		}
		q = `SELECT ` + cols + `
			FROM findings f
			LEFT JOIN tasks t ON f.task_id = t.id
			WHERE f.id IN (` + strings.Join(ph, ",") + `)
			` + order
	} else {
		scoped, err := d.applyAssetScope(f)
		if err != nil {
			return nil, err
		}
		where, wargs := scoped.where()
		q = `SELECT ` + cols + `
			FROM findings f
			LEFT JOIN tasks t ON f.task_id = t.id` + where + `
			` + order
		args = wargs
	}

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*DBFinding
	for rows.Next() {
		f := &DBFinding{}
		var aidsJSON string
		if err := rows.Scan(&f.ID, &f.TaskID, &f.NodeID, &f.VulnClass, &f.Name, &f.Severity,
			&f.Summary, &f.Evidence, &f.Worker, &aidsJSON, &f.Status, &f.CreatedAt,
			&f.TaskDescription, &f.EvidenceVersion, &f.ReportEvidenceVersion, &f.TrafficCount,
			&f.Confidence, &f.Counterevidence, &f.ProofGap, &f.Report); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(aidsJSON), &f.AssetIDs)
		out = append(out, f)
	}
	return out, rows.Err()
}

// FindingStats is the whole-table aggregate powering the 发现 page's stat cards
// and vuln-class filter — computed server-side so it stays exact regardless of
// pagination.
type FindingStats struct {
	Total       int                 `json:"total"`
	Pending     int                 `json:"pending"`
	Critical    int                 `json:"critical"`
	High        int                 `json:"high"`
	Medium      int                 `json:"medium"`
	Low         int                 `json:"low"`
	VulnClasses []string            `json:"vulnclasses"`
	Tasks       []FindingTaskOption `json:"tasks"` // 有漏洞的任务(供「按任务」下拉)
}

// FindingTaskOption is one entry in the 发现 page's 任务 filter: a task that has at
// least one finding, with its description and finding count. Description is empty when
// the task has since been deleted (finding rows persist), so the frontend falls back to
// the id.
type FindingTaskOption struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"` // 可选任务名称;空=未命名
	Description string `json:"description"`
	Count       int    `json:"count"`
}

// FindingStats returns whole-table counts (by severity + pending) and the sorted
// set of distinct vuln classes.
func (d *DB) FindingStats() (*FindingStats, error) {
	st := &FindingStats{VulnClasses: []string{}, Tasks: []FindingTaskOption{}}
	err := d.QueryRow(`SELECT
		COUNT(*),
		COUNT(*) FILTER (WHERE status = 'pending'),
		COUNT(*) FILTER (WHERE severity = 'critical'),
		COUNT(*) FILTER (WHERE severity = 'high'),
		COUNT(*) FILTER (WHERE severity = 'medium'),
		COUNT(*) FILTER (WHERE severity = 'low')
		FROM findings`).Scan(&st.Total, &st.Pending, &st.Critical, &st.High, &st.Medium, &st.Low)
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(`SELECT DISTINCT vulnclass FROM findings WHERE vulnclass <> '' ORDER BY vulnclass`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var vc string
		if err := rows.Scan(&vc); err != nil {
			return nil, err
		}
		st.VulnClasses = append(st.VulnClasses, vc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 任务下拉:有漏洞的任务,带描述(任务删除后为空,前端回退 id)和条数,最新有漏洞的排前。
	trows, err := d.Query(`SELECT f.task_id, COALESCE(t.name, ''), COALESCE(t.description, ''), COUNT(*)
		FROM findings f
		LEFT JOIN tasks t ON f.task_id = t.id
		WHERE f.task_id IS NOT NULL
		GROUP BY f.task_id, t.name, t.description
		ORDER BY MAX(f.created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var opt FindingTaskOption
		if err := trows.Scan(&opt.ID, &opt.Name, &opt.Description, &opt.Count); err != nil {
			return nil, err
		}
		st.Tasks = append(st.Tasks, opt)
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	vulnclasses := make(map[string]bool, len(st.VulnClasses))
	for _, vulnclass := range st.VulnClasses {
		vulnclasses[vulnclass] = true
	}
	for _, aggregate := range archived {
		cold := aggregate.FindingStats
		st.Total += cold.Total
		st.Pending += cold.Pending
		st.Critical += cold.Critical
		st.High += cold.High
		st.Medium += cold.Medium
		st.Low += cold.Low
		for _, vulnclass := range cold.VulnClasses {
			if vulnclass != "" {
				vulnclasses[vulnclass] = true
			}
		}
	}
	st.VulnClasses = st.VulnClasses[:0]
	for vulnclass := range vulnclasses {
		st.VulnClasses = append(st.VulnClasses, vulnclass)
	}
	sort.Strings(st.VulnClasses)
	return st, nil
}

// GetFinding returns a single finding row (with task_description joined and the
// full Markdown report), or nil when no row has that id. Unlike the list queries
// it also selects `report` — that column is only needed on the detail page.
func (d *DB) GetFinding(id int64) (*DBFinding, error) {
	f := &DBFinding{}
	var aidsJSON string
	err := d.QueryRow(`SELECT `+findingSelectCols+`, COALESCE(f.report, '')
		FROM findings f
		LEFT JOIN tasks t ON f.task_id = t.id
		WHERE f.id = $1`, id).Scan(
		&f.ID, &f.TaskID, &f.NodeID, &f.VulnClass, &f.Name, &f.Severity,
		&f.Summary, &f.Evidence, &f.Worker, &aidsJSON, &f.Status, &f.CreatedAt,
		&f.TaskDescription, &f.EvidenceVersion, &f.ReportEvidenceVersion, &f.TrafficCount,
		&f.Confidence, &f.Counterevidence, &f.ProofGap, &f.Report)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(aidsJSON), &f.AssetIDs)
	return f, nil
}

// DeleteFinding removes a finding entirely: the standalone findings row and its
// originating exploration node (kind='finding'), so it disappears from the findings
// list, the per-task 发现 Tab, and the exploration graph alike. Deleting the node
// cascades its edges + node_assets and nulls any activity referencing it. Returns
// rows affected (0 = no finding with that id).
func (d *DB) DeleteFinding(id int64) (n int64, err error) {
	err = d.WithEvidenceTx(context.Background(), func(tx *sql.Tx) error {
		if err := LockFindingEvidenceTx(tx, id, nil); err != nil {
			if errors.Is(err, ErrFindingNotFound) {
				return nil
			}
			return err
		}
		var nodeID sql.NullInt64
		if err := tx.QueryRow(`DELETE FROM findings WHERE id=$1 RETURNING node_id`, id).Scan(&nodeID); err != nil {
			return err
		}
		if nodeID.Valid {
			if _, err := tx.Exec(`DELETE FROM exploration_nodes WHERE id=$1 AND kind='finding'`, nodeID.Int64); err != nil {
				return err
			}
		}
		n = 1
		return nil
	})
	return
}

// DeleteFindingsByTask removes all findings rows of a task. The originating
// exploration finding nodes are cascade-deleted separately when the task's
// exploration subgraph is dropped. Returns rows deleted.
func (d *DB) DeleteFindingsByTask(taskID int64) (int64, error) {
	res, err := d.Exec(`DELETE FROM findings WHERE task_id=$1`, taskID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetFindingStatus updates one finding's triage state. Returns rows affected.
//
// 底层 setter：只改状态、不登记推送事件。生产代码改状态请走
// SetFindingStatusWithNotify —— 直接调本函数会让「状态变更推送」静默失效。
// 保留它是为了让不关心通知的用例（参数校验、复测流程）能单独驱动状态。
func (d *DB) SetFindingStatus(id int64, status string) (int64, error) {
	res, err := d.Exec(`UPDATE findings SET status=$1 WHERE id=$2`, status, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LatestRetestVerdicts maps finding NODE ids of one task to the verdict of the
// latest completed retest carrying a verdict (reproduced / fixed / inconclusive).
// The report surfaces the non-"reproduced" ones: 复核存疑或确认修复。
func (d *DB) LatestRetestVerdicts(taskID int64) (map[int64]string, error) {
	out := map[int64]string{}
	if taskID <= 0 {
		return out, nil
	}
	rows, err := d.Query(`SELECT f.node_id, r.verdict
FROM finding_retests r JOIN findings f ON f.id = r.finding_id
WHERE f.task_id=$1 AND f.node_id IS NOT NULL AND r.status='completed' AND r.verdict <> ''
ORDER BY r.id DESC`, taskID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID int64
		var verdict string
		if err := rows.Scan(&nodeID, &verdict); err != nil {
			return out, err
		}
		if _, seen := out[nodeID]; !seen {
			out[nodeID] = verdict // id DESC → first row per node is the latest
		}
	}
	return out, rows.Err()
}

// FindingConfirmationByNodeID reports what is known about whether a finding was
// actually confirmed to exist — three independent signals: the reporter's own
// confidence calibration, the triage status, and the latest completed retest
// verdict. The report gate (server.toolUpdateFindingReport) uses them so an
// unconfirmed hypothesis never gets a "vulnerability report".
//
// found=false 表示这条 node 还没有独立漏洞行（极老的库/异常数据），交给上层按原逻辑报错。
func (d *DB) FindingConfirmationByNodeID(nodeID int64) (confidence, status, retestVerdict string, found bool, err error) {
	row := d.QueryRow(`SELECT COALESCE(f.confidence,''), COALESCE(f.status,''),
		COALESCE((SELECT r.verdict FROM finding_retests r
		          WHERE r.finding_id=f.id AND r.status='completed' AND r.verdict <> ''
		          ORDER BY r.id DESC LIMIT 1),'')
		FROM findings f WHERE f.node_id=$1 ORDER BY f.id DESC LIMIT 1`, nodeID)
	switch err = row.Scan(&confidence, &status, &retestVerdict); {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", "", false, nil
	case err != nil:
		return "", "", "", false, err
	}
	return confidence, status, retestVerdict, true, nil
}

// SetFindingRetractReason records why a finding was retracted. Call it BEFORE
// flipping status to retracted（先因后果：状态翻转会触发推送，原因应已就位）。
func (d *DB) SetFindingRetractReason(id int64, reason string) (int64, error) {
	res, err := d.Exec(`UPDATE findings SET retract_reason=$1 WHERE id=$2`, reason, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetFindingReportByNodeID sets the Markdown report on the standalone finding row
// whose node_id matches — report_finding returns that node id, so an agent tool
// can address the finding it just created. Returns rows affected (0 when no row).
func (d *DB) SetFindingReportByNodeID(nodeID int64, report string) (int64, error) {
	return d.SetFindingReportVersionByNodeID(context.Background(), nodeID, report, nil)
}

// setFindingCol updates one text column on the standalone finding row AND mirrors
// the new value into the originating exploration node's payload under jsonKey, so the
// per-task 发现 Tab (which reads the node payload, not this table) stays in sync.
// Returns rows affected (0 when no finding has that id); the node sync is best-effort.
// col and jsonKey MUST be trusted constants (they are interpolated into SQL) — never
// pass user input.
func (d *DB) setFindingCol(id int64, col, jsonKey, val string) (int64, error) {
	var nodeID *int64
	err := d.QueryRow(`UPDATE findings SET `+col+`=$1 WHERE id=$2 RETURNING node_id`, val, id).Scan(&nodeID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if nodeID != nil {
		// finding 列已经改好了，这里只是同步节点 payload；两者不在同一事务里，所以失败
		// 不回滚——但必须留下痕迹，否则两边会静默不一致（图上显示旧值，列表显示新值）。
		if _, err := d.Exec(`UPDATE exploration_nodes
			SET payload = jsonb_set(payload, '{`+jsonKey+`}', to_jsonb($1::text))
			WHERE id = $2`, val, *nodeID); err != nil {
			log.Printf("[findings] 同步节点 payload 失败 finding=%d node=%d key=%s: %v", id, *nodeID, jsonKey, err)
		}
	}
	return 1, nil
}

// SetFindingSeverity updates one finding's severity (+ node payload sync). Returns
// rows affected (0 when no finding has that id).
func (d *DB) SetFindingSeverity(id int64, severity string) (int64, error) {
	return d.setFindingCol(id, "severity", "severity", severity)
}

// SetFindingName updates one finding's 漏洞名称 (+ node payload sync). Empty name is
// allowed — the frontend falls back to the vuln class for display.
func (d *DB) SetFindingName(id int64, name string) (int64, error) {
	return d.setFindingCol(id, "name", "name", name)
}

// SetFindingVulnClass updates one finding's 漏洞类别 (+ node payload sync).
func (d *DB) SetFindingVulnClass(id int64, vulnclass string) (int64, error) {
	return d.setFindingCol(id, "vulnclass", "vulnclass", vulnclass)
}

// FindingMeta is the standalone-row data (id, triage state, anchored assets) the
// per-task view grafts onto its exploration-node findings.
type FindingMeta struct {
	TrafficCount int

	ID       int64
	Status   string
	AssetIDs []int64
	// RetractReason is set when Status == FindingRetracted.
	RetractReason string
	// Confidence is the honest-calibration label (high/medium/low; "" = 未标定).
	Confidence string
	// Counterevidence is set when Status == FindingRuledOut: the control point
	// that blocks the candidate and why it is off the attacker path.
	Counterevidence string
	// ProofGap is set when Status == FindingOpenProofGap: what is missing to
	// close the candidate ("没找到控制点 ≠ 安全").
	ProofGap string
}

// FindingMetaByNodeID maps a task's finding node ids to their standalone-row
// metadata (status + anchored asset ids) via the asset store, so callers holding
// only an AssetStore (e.g. the agent ToolSet) can reach it without a raw *DB.
func (a *AssetStore) FindingMetaByNodeID(taskID int64) (map[int64]FindingMeta, error) {
	return a.db.FindingMetaByNodeID(taskID)
}

// FindingMetaByNodeID maps a task's finding node ids to their standalone-row
// metadata, so the per-task view (which reads exploration nodes) can show and
// edit the same status — and the same anchored assets — as the global 发现 page.
func (d *DB) FindingMetaByNodeID(taskID int64) (map[int64]FindingMeta, error) {
	out := map[int64]FindingMeta{}
	if taskID <= 0 {
		return out, nil
	}
	rows, err := d.Query(`SELECT node_id, id, COALESCE(status,'pending'), asset_ids, COALESCE(retract_reason,''), (SELECT count(*) FROM finding_traffic_bindings b WHERE b.finding_id=findings.id),
			COALESCE(confidence,''), COALESCE(counterevidence,''), COALESCE(proof_gap,'') FROM findings
		WHERE task_id=$1 AND node_id IS NOT NULL`, taskID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var nid int64
		var m FindingMeta
		var aidsJSON string
		if err := rows.Scan(&nid, &m.ID, &m.Status, &aidsJSON, &m.RetractReason, &m.TrafficCount,
			&m.Confidence, &m.Counterevidence, &m.ProofGap); err != nil {
			return out, err
		}
		_ = json.Unmarshal([]byte(aidsJSON), &m.AssetIDs)
		out[nid] = m
	}
	return out, rows.Err()
}

// FindingReportDetail 是报告 Agent 写的详细分析及其证据版本快照。
// ReportEvidenceVersion 是撰写时读取（并回传）的证据版本；与 findings 当前的
// EvidenceVersion 不一致，说明写完之后证据又被补充/替换，这段分析可能已过期。
// -1 表示撰写时已有证据但未声明所读版本（见 update_finding_report 的落库规则）。
type FindingReportDetail struct {
	Report                string
	EvidenceVersion       int64
	ReportEvidenceVersion int64
}

// FindingReportsByNode 返回一个任务里「报告 Agent 写的详细报告」——node_id → 详情。
//
// 这份内容是报告 Agent 用 update_finding_report 写的：利用过程、影响、关键位置、
// 根因与修复建议。它此前只存在详情页里，总报告完全没用上——交付物最值钱的一段被丢了。
// 只取非空行；调用方按 node id 合并进正文。同时带上证据版本，供调用方判断分析是否过期。
func (d *DB) FindingReportsByNode(taskID int64) (map[int64]FindingReportDetail, error) {
	rows, err := d.Query(`SELECT node_id, report, evidence_version, report_evidence_version FROM findings
WHERE task_id=$1 AND node_id IS NOT NULL AND COALESCE(report,'') <> ''`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]FindingReportDetail{}
	for rows.Next() {
		var nodeID int64
		var detail FindingReportDetail
		if err := rows.Scan(&nodeID, &detail.Report, &detail.EvidenceVersion, &detail.ReportEvidenceVersion); err != nil {
			return nil, err
		}
		out[nodeID] = detail
	}
	return out, rows.Err()
}

// FindingClosedForRisk 判断一个 triage 状态是否意味着「这条结论不再是当前风险」。
// 只用于**计数口径**（findings_total / finding_list / 相似度去重）：撤回、误报、
// 重复、已排除都不算确认发现；ignored/fixed/resolved 仍算「发现过的漏洞」，由报告
// 自己的 Closed 段处理，不在这里抹掉。
func FindingClosedForRisk(status string) bool {
	switch status {
	case FindingRetracted, FindingFalsePositive, FindingDuplicate, FindingRuledOut:
		return true
	}
	return false
}

// CountActiveFindings 数本任务里仍然有效的漏洞节点（findings.status 未被撤回/误报/
// 重复/排除；没有独立 findings 行的遗留节点按有效计）。
func (s *ExplorationStore) CountActiveFindings() (int, error) {
	var n int
	err := s.db.QueryRow(`
SELECT COUNT(*) FROM exploration_nodes n
WHERE n.exploration_id=$1 AND n.kind='finding'
  AND COALESCE((SELECT f.status FROM findings f WHERE f.node_id=n.id ORDER BY f.id DESC LIMIT 1),'') NOT IN
      ('retracted','false_positive','duplicate','ruled_out')`, s.expID).Scan(&n)
	return n, err
}

// ListActiveFindings 返回本任务仍然有效的漏洞节点（最新在前，limit<=0 = 50）。
func (s *ExplorationStore) ListActiveFindings(limit int) ([]*Node, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes n
WHERE n.exploration_id=$1 AND n.kind='finding'
  AND COALESCE((SELECT f.status FROM findings f WHERE f.node_id=n.id ORDER BY f.id DESC LIMIT 1),'') NOT IN
      ('retracted','false_positive','duplicate','ruled_out')
ORDER BY n.id DESC LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// ClosedFindingNodeIDs 返回给定节点里已被 triage 关闭（撤回/误报/重复/排除）的集合。
// list_findings 要跨「本任务 + 直接关联任务」，所以按节点 id 批量查，不按 exploration 过滤。
func (s *ExplorationStore) ClosedFindingNodeIDs(nodeIDs []int64) (map[int64]bool, error) {
	out := make(map[int64]bool)
	if len(nodeIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`
SELECT DISTINCT node_id FROM findings
WHERE node_id = ANY($1::bigint[])
  AND status IN ('retracted','false_positive','duplicate','ruled_out')`, nodeIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SimilarFinding 是相似度检查用的一条已有发现（只取判断所需的字段）。
type SimilarFinding struct {
	NodeID    int64
	Summary   string
	VulnClass string
	AssetIDs  []int64
	Status    string
}

// ActiveFindingsByClass 取本任务里同 vulnclass 且仍然有效的发现，供 report_finding
// 做「同资产 + 同类 + 摘要相似」的重复检查。比较放在调用方做（归一化规则属于
// agent 的语义层，DB 只负责取数）。
func (s *ExplorationStore) ActiveFindingsByClass(vulnClass string, limit int) ([]SimilarFinding, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
SELECT f.node_id, COALESCE(f.summary,''), COALESCE(f.vulnclass,''), COALESCE(f.asset_ids,'[]'::jsonb), COALESCE(f.status,'')
FROM findings f
JOIN exploration_nodes n ON n.id=f.node_id AND n.exploration_id=$1
WHERE lower(trim(COALESCE(f.vulnclass,''))) = lower(trim($2))
  AND COALESCE(f.status,'') NOT IN ('retracted','false_positive','duplicate','ruled_out')
ORDER BY f.id DESC LIMIT $3`, s.expID, vulnClass, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SimilarFinding
	for rows.Next() {
		var f SimilarFinding
		var assets []byte
		if err := rows.Scan(&f.NodeID, &f.Summary, &f.VulnClass, &assets, &f.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(assets, &f.AssetIDs)
		out = append(out, f)
	}
	return out, rows.Err()
}

// MergeFindings 把一条或多条重复登记的漏洞并入保留项（canonical）：
//   - 重复项的 findings.status 置为 duplicate，retract_reason 记录合并理由；
//   - 重复项的资产锚点并入保留项（证据不因合并丢失）；
//   - 图节点保留（墓碑），报告会把它列在「已关闭/重复」里，不会凭空消失。
//
// 只允许并入仍然有效的保留项；重复项本身必须存在于本任务。返回实际合并的条数。
func (s *ExplorationStore) MergeFindings(canonicalNodeID int64, duplicateNodeIDs []int64, reason string) (int64, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, errors.New("合并漏洞必须写理由：为什么这些是同一个漏洞")
	}
	if canonicalNodeID <= 0 {
		return 0, errors.New("canonical_id 必填")
	}
	if len(duplicateNodeIDs) == 0 {
		return 0, errors.New("duplicate_ids 至少给一条")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var canonFindingID int64
	var canonStatus string
	err = tx.QueryRow(`SELECT f.id, COALESCE(f.status,'') FROM findings f
JOIN exploration_nodes n ON n.id=f.node_id AND n.exploration_id=$2
WHERE f.node_id=$1 ORDER BY f.id DESC LIMIT 1`, canonicalNodeID, s.expID).Scan(&canonFindingID, &canonStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("保留项 #%d 不是本任务的有效漏洞记录（找不到独立 findings 行）", canonicalNodeID)
	}
	if err != nil {
		return 0, err
	}
	if FindingClosedForRisk(canonStatus) {
		return 0, fmt.Errorf("保留项 #%d 当前状态是 %s：不能作为合并目标（先把保留项恢复为有效状态）", canonicalNodeID, canonStatus)
	}

	var merged int64
	seen := map[int64]bool{}
	for _, dupNodeID := range duplicateNodeIDs {
		if dupNodeID <= 0 || dupNodeID == canonicalNodeID || seen[dupNodeID] {
			continue
		}
		seen[dupNodeID] = true
		var dupFindingID int64
		err := tx.QueryRow(`SELECT f.id FROM findings f
JOIN exploration_nodes n ON n.id=f.node_id AND n.exploration_id=$2
WHERE f.node_id=$1 ORDER BY f.id DESC LIMIT 1`, dupNodeID, s.expID).Scan(&dupFindingID)
		if errors.Is(err, sql.ErrNoRows) {
			return merged, fmt.Errorf("重复项 #%d 不是本任务的有效漏洞记录", dupNodeID)
		}
		if err != nil {
			return merged, err
		}
		if _, err := tx.Exec(`UPDATE findings SET status=$1, retract_reason=$2 WHERE id=$3`,
			FindingDuplicate, reason, dupFindingID); err != nil {
			return merged, err
		}
		if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id, asset_id)
SELECT $1, asset_id FROM exploration_anchors WHERE node_id=$2
ON CONFLICT DO NOTHING`, canonicalNodeID, dupNodeID); err != nil {
			return merged, err
		}
		merged++
	}
	if merged == 0 {
		return 0, errors.New("没有可合并的重复项（duplicate_ids 为空、等于保留项或重复）")
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return merged, nil
}
