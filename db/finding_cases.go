package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// FindingCase 是「漏洞案例」：把同一根因/同一资产的重复上报归并成一个独立缺陷。
// 原始 findings 保留（成员），案例持有统一报告与版本；保存统一报告用实际读取的
// 案例 version 做 CAS，report_version 记录该版本（成员变化后即过期）。
type FindingCase struct {
	ID             int64        `json:"id"`
	TaskID         *int64       `json:"task_id,omitempty"`
	OriginTaskID   int64        `json:"origin_task_id"`
	Title          string       `json:"title"`
	Reason         string       `json:"reason"`
	Severity       string       `json:"severity"`
	SeverityReason string       `json:"severity_reason"`
	Report         string       `json:"report"`
	Version        int64        `json:"version"`
	ReportVersion  int64        `json:"report_version"`
	Active         bool         `json:"active"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	MemberCount    int          `json:"member_count"`
	Members        []*DBFinding `json:"members,omitempty"`
}

// FindingCaseCandidate 是一条「可能和当前发现是同一漏洞」的候选及评分理由。
type FindingCaseCandidate struct {
	Finding *DBFinding `json:"finding"`
	Score   int        `json:"score"`
	Reasons []string   `json:"reasons"`
}

// FindingCaseSuggestion 是一条「疑似同一漏洞」的分组建议（确定性评分生成，不经 LLM）。
type FindingCaseSuggestion struct {
	ID         int64     `json:"id"`
	TaskID     int64     `json:"task_id"`
	FindingIDs []int64   `json:"finding_ids"`
	Title      string    `json:"title"`
	Reason     string    `json:"reason"`
	Score      int       `json:"score"`
	State      string    `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
}

// FindingCaseStats 是 case-aware 的统计口径：独立缺陷数 vs 上报记录数。
type FindingCaseStats struct {
	Cases     int `json:"cases"`
	Ungrouped int `json:"ungrouped"`
	Distinct  int `json:"distinct"`
	Reports   int `json:"reports"`
}

// CreateFindingCase 建一个案例并把 findings 收为成员。要求每条 finding 都属于该任务且
// 尚未加入其它案例（一个发现只能属于一个案例，避免统计重复）。
func (d *DB) CreateFindingCase(taskID, originTaskID int64, title, reason, severity string, findingIDs []int64, actor string) (*FindingCase, error) {
	if taskID <= 0 || len(findingIDs) == 0 {
		return nil, fmt.Errorf("task_id 与 finding_ids 必填")
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM findings WHERE id = ANY($1) AND task_id=$2 FOR UPDATE`, findingIDs, taskID)
	if err != nil {
		return nil, err
	}
	found := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		found[id] = true
	}
	rows.Close()
	for _, id := range findingIDs {
		if !found[id] {
			return nil, fmt.Errorf("发现 %d 不属于任务 %d", id, taskID)
		}
	}
	var taken int
	if err := tx.QueryRow(`SELECT count(*) FROM finding_case_members WHERE finding_id = ANY($1)`, findingIDs).Scan(&taken); err != nil {
		return nil, err
	}
	if taken > 0 {
		return nil, fmt.Errorf("有 %d 条发现已经在其它案例里；先从原案例移除再归并", taken)
	}
	var caseID int64
	if err := tx.QueryRow(`INSERT INTO finding_cases(task_id, origin_task_id, title, reason, severity)
VALUES ($1,$2,$3,$4,$5) RETURNING id`, taskID, originTaskID, strings.TrimSpace(title), strings.TrimSpace(reason), strings.TrimSpace(severity)).Scan(&caseID); err != nil {
		return nil, err
	}
	for _, id := range findingIDs {
		if _, err := tx.Exec(`INSERT INTO finding_case_members(finding_id, case_id) VALUES ($1,$2)`, id, caseID); err != nil {
			return nil, err
		}
	}
	if err := insertFindingCaseEvent(tx, caseID, "create", actor, reason, findingIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetFindingCase(caseID)
}

// GetFindingCase 读一个案例及其成员发现。
func (d *DB) GetFindingCase(id int64) (*FindingCase, error) {
	c := &FindingCase{}
	err := d.QueryRow(`SELECT id, task_id, origin_task_id, title, reason, COALESCE(severity,''), COALESCE(severity_reason,''),
COALESCE(report,''), version, report_version, active, created_at, updated_at
FROM finding_cases WHERE id=$1`, id).Scan(&c.ID, &c.TaskID, &c.OriginTaskID, &c.Title, &c.Reason, &c.Severity, &c.SeverityReason,
		&c.Report, &c.Version, &c.ReportVersion, &c.Active, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(`SELECT `+findingSelectCols+`
FROM findings f
LEFT JOIN tasks t ON f.task_id=t.id
JOIN finding_case_members m ON m.finding_id=f.id
WHERE m.case_id=$1 ORDER BY f.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members, err := scanFindings(rows)
	if err != nil {
		return nil, err
	}
	if len(members) > 0 {
		// findingSelectCols deliberately omits the long report body; the unified
		// report workflow needs every member's original report, so load them in
		// one extra keyed query instead of widening every list scan.
		ids := make([]int64, 0, len(members))
		for _, m := range members {
			ids = append(ids, m.ID)
		}
		reportRows, err := d.Query(`SELECT id, COALESCE(report,'') FROM findings WHERE id=ANY($1)`, ids)
		if err != nil {
			return nil, err
		}
		reports := map[int64]string{}
		for reportRows.Next() {
			var id int64
			var report string
			if err := reportRows.Scan(&id, &report); err != nil {
				reportRows.Close()
				return nil, err
			}
			reports[id] = report
		}
		if err := reportRows.Err(); err != nil {
			reportRows.Close()
			return nil, err
		}
		reportRows.Close()
		for _, m := range members {
			m.Report = reports[m.ID]
		}
	}
	c.Members = members
	c.MemberCount = len(members)
	return c, nil
}

// ListFindingCases 列出某任务的案例（默认只列 active），带成员数。
func (d *DB) ListFindingCases(taskID int64, limit int) ([]*FindingCase, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.Query(`SELECT c.id, c.task_id, c.origin_task_id, c.title, c.reason, COALESCE(c.severity,''), COALESCE(c.severity_reason,''),
COALESCE(c.report,''), c.version, c.report_version, c.active, c.created_at, c.updated_at,
(SELECT count(*) FROM finding_case_members m WHERE m.case_id=c.id)
FROM finding_cases c WHERE c.task_id=$1 AND c.active ORDER BY c.id DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FindingCase
	for rows.Next() {
		c := &FindingCase{}
		if err := rows.Scan(&c.ID, &c.TaskID, &c.OriginTaskID, &c.Title, &c.Reason, &c.Severity, &c.SeverityReason,
			&c.Report, &c.Version, &c.ReportVersion, &c.Active, &c.CreatedAt, &c.UpdatedAt, &c.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddFindingCaseMembers 把更多发现加入案例。已在其它案例里的发现会被拒绝。
func (d *DB) AddFindingCaseMembers(caseID int64, findingIDs []int64, reason, actor string) (int, error) {
	if caseID <= 0 || len(findingIDs) == 0 {
		return 0, fmt.Errorf("case_id 与 finding_ids 必填")
	}
	tx, err := d.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var taskID int64
	if err := tx.QueryRow(`SELECT COALESCE(task_id,0) FROM finding_cases WHERE id=$1 AND active`, caseID).Scan(&taskID); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("案例 %d 不存在或已关闭", caseID)
		}
		return 0, err
	}
	var taken int
	if err := tx.QueryRow(`SELECT count(*) FROM finding_case_members WHERE finding_id = ANY($1)`, findingIDs).Scan(&taken); err != nil {
		return 0, err
	}
	if taken > 0 {
		return 0, fmt.Errorf("有 %d 条发现已经在其它案例里", taken)
	}
	added := 0
	for _, id := range findingIDs {
		res, err := tx.Exec(`INSERT INTO finding_case_members(finding_id, case_id) SELECT $1,$2 WHERE EXISTS (SELECT 1 FROM findings WHERE id=$1 AND task_id=$3)`, id, caseID, taskID)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	if added == 0 {
		return 0, fmt.Errorf("没有可加入的发现（可能不属于该任务）")
	}
	if _, err := tx.Exec(`UPDATE finding_cases SET version=version+1, updated_at=now() WHERE id=$1`, caseID); err != nil {
		return 0, err
	}
	if err := insertFindingCaseEvent(tx, caseID, "add_members", actor, reason, findingIDs); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
}

// RemoveFindingCaseMember 从案例移除一条发现；成员清空时案例自动关闭。
func (d *DB) RemoveFindingCaseMember(caseID, findingID int64, reason, actor string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM finding_case_members WHERE case_id=$1 AND finding_id=$2`, caseID, findingID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("发现 %d 不在案例 %d 里", findingID, caseID)
	}
	if _, err := tx.Exec(`UPDATE finding_cases SET version=version+1, updated_at=now(),
active = EXISTS (SELECT 1 FROM finding_case_members WHERE case_id=$1) WHERE id=$1`, caseID); err != nil {
		return err
	}
	if err := insertFindingCaseEvent(tx, caseID, "remove_member", actor, reason, []int64{findingID}); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateFindingCaseReport saves the unified report under the case version the
// caller actually read. report_version records that evidence version; any member
// or finding change bumps version and therefore makes the saved report stale.
func (d *DB) UpdateFindingCaseReport(ctx context.Context, id, version int64, title, report, severity, severityReason string) (*FindingCase, error) {
	if strings.TrimSpace(report) == "" {
		return nil, fmt.Errorf("统一报告不能为空")
	}
	severity = strings.ToLower(strings.TrimSpace(severity))
	if severity != "" && !ValidSeverity(severity) {
		return nil, fmt.Errorf("severity 取值必须是 critical|high|medium|low")
	}
	err := d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		c, err := scanCase(tx.QueryRow(`SELECT id, task_id, origin_task_id, title, reason, COALESCE(severity,''), COALESCE(severity_reason,''),
COALESCE(report,''), version, report_version, active, created_at, updated_at
FROM finding_cases WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if !c.Active {
			return fmt.Errorf("案例已关闭")
		}
		if version <= 0 {
			version = c.Version
		}
		if c.Version != version {
			return fmt.Errorf("案例版本冲突：材料已变化，请重新读取后再写统一报告")
		}
		if strings.TrimSpace(title) == "" {
			title = c.Title
		}
		if severity == "" {
			severity = c.Severity
		}
		if strings.TrimSpace(severityReason) == "" {
			severityReason = c.SeverityReason
		}
		if c.Title == title && c.Report == report && c.Severity == severity && c.SeverityReason == severityReason && c.ReportVersion == version {
			return nil
		}
		if _, err := tx.Exec(`UPDATE finding_cases SET title=$2,report=$3,severity=$4,severity_reason=$5,report_version=$6,updated_at=now() WHERE id=$1`,
			id, title, report, severity, severityReason, version); err != nil {
			return err
		}
		return insertFindingCaseEvent(tx, id, "report_updated", "reporter", "统一报告已更新；评级依据："+severityReason, nil)
	})
	if err != nil {
		return nil, err
	}
	return d.GetFindingCase(id)
}

// FindingCaseCandidates 给一条发现找可能重复的候选（同任务、未归并）。
// 评分参考 ARTEX：同漏洞类型 +2、资产重叠 +2、同名称 +1、两者同时 +3。
func (d *DB) FindingCaseCandidates(taskID, findingID int64, limit int) ([]FindingCaseCandidate, error) {
	if limit <= 0 {
		limit = 10
	}
	target, err := d.GetFinding(findingID)
	if err != nil || target == nil {
		return nil, fmt.Errorf("发现 %d 不存在", findingID)
	}
	rows, err := d.Query(`SELECT `+findingSelectCols+`
FROM findings f LEFT JOIN tasks t ON f.task_id=t.id
WHERE f.task_id=$1 AND f.id<>$2 AND COALESCE(f.status,'pending') NOT IN ('ruled_out','open_proof_gap','false_positive','duplicate','retracted','ignored')
AND NOT EXISTS (SELECT 1 FROM finding_case_members m WHERE m.finding_id=f.id)
ORDER BY f.id DESC LIMIT 200`, taskID, findingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	others, err := scanFindings(rows)
	if err != nil {
		return nil, err
	}
	blocked, _ := d.findingCaseBlockedPairs(taskID)
	var out []FindingCaseCandidate
	for _, f := range others {
		if blocked[pairKey(target.ID, f.ID)] {
			continue
		}
		score, reasons := scoreFindingPair(target, f)
		if score <= 0 {
			continue
		}
		out = append(out, FindingCaseCandidate{Finding: f, Score: score, Reasons: reasons})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Finding.ID > out[j].Finding.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// FindingCaseStats 统计任务下的独立缺陷数（案例 + 未归并发现）与上报记录数。
func (d *DB) FindingCaseStats(taskID int64) (*FindingCaseStats, error) {
	st := &FindingCaseStats{}
	if err := d.QueryRow(`SELECT count(*) FROM finding_cases WHERE task_id=$1 AND active`, taskID).Scan(&st.Cases); err != nil {
		return nil, err
	}
	if err := d.QueryRow(`SELECT count(*) FROM findings f WHERE f.task_id=$1
AND COALESCE(f.status,'pending') NOT IN ('ruled_out','open_proof_gap','false_positive','duplicate','retracted','ignored')
AND NOT EXISTS (SELECT 1 FROM finding_case_members m WHERE m.finding_id=f.id)`, taskID).Scan(&st.Ungrouped); err != nil {
		return nil, err
	}
	if err := d.QueryRow(`SELECT count(*) FROM findings f WHERE f.task_id=$1
AND COALESCE(f.status,'pending') NOT IN ('ruled_out','open_proof_gap','false_positive','duplicate','retracted','ignored')`, taskID).Scan(&st.Reports); err != nil {
		return nil, err
	}
	st.Distinct = st.Cases + st.Ungrouped
	return st, nil
}

// scoreFindingPair 是候选/建议共用的评分：同类型 +2、资产重叠 +2、名称相近 +1，
// 类型与资产同时命中再 +3。
func scoreFindingPair(a, b *DBFinding) (int, []string) {
	score := 0
	var reasons []string
	if a.VulnClass != "" && b.VulnClass == a.VulnClass {
		score += 2
		reasons = append(reasons, "同漏洞类型")
	}
	assets := map[int64]bool{}
	for _, id := range a.AssetIDs {
		assets[id] = true
	}
	overlap := 0
	for _, id := range b.AssetIDs {
		if assets[id] {
			overlap++
		}
	}
	if overlap > 0 {
		score += 2
		reasons = append(reasons, "资产重叠")
	}
	an := normalizeCaseText(a.Name + " " + a.Summary)
	bn := normalizeCaseText(b.Name + " " + b.Summary)
	if an != "" && bn != "" && (strings.Contains(an, bn) || strings.Contains(bn, an)) {
		score += 1
		reasons = append(reasons, "名称/摘要相近")
	}
	if score >= 4 {
		score += 3
		reasons = append(reasons, "类型与资产同时命中")
	}
	return score, reasons
}

func pairKey(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}

func (d *DB) findingCaseBlockedPairs(taskID int64) (map[[2]int64]bool, error) {
	rows, err := d.Query(`SELECT b.left_id, b.right_id FROM finding_case_blocks b
JOIN findings f ON f.id=b.left_id WHERE f.task_id=$1`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]int64]bool{}
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out[pairKey(a, b)] = true
	}
	return out, rows.Err()
}

// GenerateFindingCaseSuggestions 扫描任务内未归并的发现，按评分生成疑似分组建议。
// 同一对 finding 只建议一次（UNIQUE(task_id,finding_ids)）；人工否决过的对不再生成。
func (d *DB) GenerateFindingCaseSuggestions(taskID int64) (int, error) {
	rows, err := d.Query(`SELECT `+findingSelectCols+`
FROM findings f LEFT JOIN tasks t ON f.task_id=t.id
WHERE f.task_id=$1 AND COALESCE(f.status,'pending') NOT IN ('ruled_out','open_proof_gap','false_positive','duplicate','retracted','ignored')
AND NOT EXISTS (SELECT 1 FROM finding_case_members m WHERE m.finding_id=f.id)
ORDER BY f.id DESC LIMIT 500`, taskID)
	if err != nil {
		return 0, err
	}
	findings, err := scanFindings(rows)
	rows.Close()
	if err != nil {
		return 0, err
	}
	blocked, _ := d.findingCaseBlockedPairs(taskID)
	created := 0
	for i := 0; i < len(findings); i++ {
		for j := i + 1; j < len(findings); j++ {
			a, b := findings[i], findings[j]
			if blocked[pairKey(a.ID, b.ID)] {
				continue
			}
			score, reasons := scoreFindingPair(a, b)
			if score < 4 {
				continue
			}
			ids := []int64{a.ID, b.ID}
			if ids[0] > ids[1] {
				ids[0], ids[1] = ids[1], ids[0]
			}
			raw, _ := json.Marshal(ids)
			title := "疑似同一漏洞：" + caseLabel(a) + " / " + caseLabel(b)
			res, err := d.Exec(`INSERT INTO finding_case_suggestions(task_id, finding_ids, title, reason, score)
VALUES ($1,$2,$3,$4,$5) ON CONFLICT (task_id, finding_ids) DO NOTHING`,
				taskID, string(raw), title, strings.Join(reasons, "、"), score)
			if err != nil {
				return created, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				created++
			}
		}
	}
	return created, nil
}

// ListFindingCaseSuggestions 列建议；state 空 = pending。
func (d *DB) ListFindingCaseSuggestions(taskID int64, state string, limit int) ([]*FindingCaseSuggestion, error) {
	if limit <= 0 {
		limit = 100
	}
	if strings.TrimSpace(state) == "" {
		state = "pending"
	}
	rows, err := d.Query(`SELECT id, task_id, finding_ids, title, reason, score, state, created_at
FROM finding_case_suggestions WHERE task_id=$1 AND state=$2 ORDER BY score DESC, id DESC LIMIT $3`, taskID, state, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FindingCaseSuggestion
	for rows.Next() {
		s := &FindingCaseSuggestion{}
		var raw string
		if err := rows.Scan(&s.ID, &s.TaskID, &raw, &s.Title, &s.Reason, &s.Score, &s.State, &s.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &s.FindingIDs)
		out = append(out, s)
	}
	return out, rows.Err()
}

// AcceptFindingCaseSuggestion 采纳建议：按建议成员建案例，并标记 accepted。
func (d *DB) AcceptFindingCaseSuggestion(id int64, actor string) (*FindingCase, error) {
	var taskID int64
	var raw, title, reason string
	if err := d.QueryRow(`SELECT task_id, finding_ids, title, reason FROM finding_case_suggestions WHERE id=$1 AND state='pending'`, id).Scan(&taskID, &raw, &title, &reason); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("建议 %d 不存在或已处理", id)
		}
		return nil, err
	}
	var ids []int64
	_ = json.Unmarshal([]byte(raw), &ids)
	if len(ids) == 0 {
		return nil, fmt.Errorf("建议没有成员")
	}
	c, err := d.CreateFindingCase(taskID, taskID, title, reason, "", ids, actor)
	if err != nil {
		return nil, err
	}
	_, _ = d.Exec(`UPDATE finding_case_suggestions SET state='accepted' WHERE id=$1`, id)
	return c, nil
}

// DismissFindingCaseSuggestion 忽略建议。
func (d *DB) DismissFindingCaseSuggestion(id int64, reason string) error {
	res, err := d.Exec(`UPDATE finding_case_suggestions SET state='dismissed' WHERE id=$1 AND state='pending'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("建议 %d 不存在或已处理", id)
	}
	return nil
}

// BlockFindingCasePair 人工否决一对发现：后续候选/建议都不再提。
func (d *DB) BlockFindingCasePair(taskID, a, b int64, reason string) error {
	if a <= 0 || b <= 0 || a == b {
		return fmt.Errorf("finding ids 不合法")
	}
	if a > b {
		a, b = b, a
	}
	_, err := d.Exec(`INSERT INTO finding_case_blocks(left_id, right_id, reason) VALUES ($1,$2,$3)
ON CONFLICT (left_id,right_id) DO UPDATE SET reason=EXCLUDED.reason`, a, b, reason)
	return err
}

func caseLabel(f *DBFinding) string {
	if strings.TrimSpace(f.Name) != "" {
		return strings.TrimSpace(f.Name)
	}
	if strings.TrimSpace(f.VulnClass) != "" {
		return strings.TrimSpace(f.VulnClass)
	}
	return fmt.Sprintf("#%d", f.ID)
}

func insertFindingCaseEvent(tx *sql.Tx, caseID int64, action, actor, reason string, findingIDs []int64) error {
	raw, _ := json.Marshal(findingIDs)
	_, err := tx.Exec(`INSERT INTO finding_case_events(case_id, action, actor, reason, finding_ids) VALUES ($1,$2,$3,$4,$5)`,
		caseID, action, actor, reason, string(raw))
	return err
}

func normalizeCaseText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FindingCaseListRow 是「按漏洞」视图的一行：要么是一个案例（含匹配成员 id），
// 要么是一条尚未归并的原始发现。与 ARTEX 的 finding-case-list 行形状一致，
// 前端据此渲染案例文件夹 + 独立漏洞。
type FindingCaseListRow struct {
	Case       *FindingCase `json:"case,omitempty"`
	Finding    *DBFinding   `json:"finding,omitempty"`
	MatchedIDs []int64      `json:"matched_ids,omitempty"`
}

// ListFindingCaseRows 分页返回「案例 + 未归并发现」的混合列表。
// filter 与全局发现页共用（task/severity/status/query/assetScope/sort），
// 默认由调用方把 ExcludeCandidates 置 true，避免候选闭合混进来。
func (d *DB) ListFindingCaseRows(filter FindingFilter, page, size int) ([]FindingCaseListRow, int, error) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	where, args := filter.where()
	base := `WITH matched AS (
		SELECT f.id, f.severity, f.created_at, COALESCE(m.case_id,0) AS cid
		FROM findings f
		LEFT JOIN tasks t ON t.id=f.task_id
		LEFT JOIN finding_case_members m ON m.finding_id=f.id` + where + `
	), entities AS (
		SELECT CASE WHEN cid>0 THEN 'c:'||cid ELSE 'f:'||id END AS key,
		       MAX(cid) AS cid, MIN(id) AS fid,
		       jsonb_agg(id ORDER BY id) AS matched_ids,
		       MAX(created_at) AS created,
		       MAX(CASE severity WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 WHEN 'low' THEN 1 ELSE 0 END) AS severity_rank
		FROM matched GROUP BY key
	)`
	var total int
	if err := d.QueryRow(base+` SELECT count(*) FROM entities`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	timeOrder := "DESC"
	if filter.Asc {
		timeOrder = "ASC"
	}
	order := `e.created ` + timeOrder + `, e.fid ` + timeOrder
	if filter.Sort == "severity" {
		order = `e.severity_rank DESC, ` + order
	}
	args = append(args, size, (page-1)*size)
	rows, err := d.Query(base+fmt.Sprintf(` SELECT e.cid,e.fid,e.matched_ids FROM entities e ORDER BY %s LIMIT $%d OFFSET $%d`, order, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	type rawRow struct {
		cid, fid int64
		matched  []int64
	}
	var raw []rawRow
	for rows.Next() {
		var r rawRow
		var ids []byte
		if err := rows.Scan(&r.cid, &r.fid, &ids); err != nil {
			rows.Close()
			return nil, 0, err
		}
		_ = json.Unmarshal(ids, &r.matched)
		raw = append(raw, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	out := make([]FindingCaseListRow, 0, len(raw))
	for _, r := range raw {
		if r.cid > 0 {
			c, err := d.getFindingCaseMeta(r.cid)
			if err != nil {
				return nil, 0, err
			}
			if c == nil {
				continue
			}
			// 列表不传统一报告正文：详情页再取，避免一页 20 个案例把响应撑大。
			c.Report = ""
			out = append(out, FindingCaseListRow{Case: c, MatchedIDs: r.matched})
			continue
		}
		f, err := d.GetFinding(r.fid)
		if err != nil {
			return nil, 0, err
		}
		if f == nil {
			continue
		}
		out = append(out, FindingCaseListRow{Finding: f, MatchedIDs: r.matched})
	}
	return out, total, nil
}

// getFindingCaseMeta 读取案例元信息，不加载成员；列表页一行一个案例时避免 N×成员。
func (d *DB) getFindingCaseMeta(id int64) (*FindingCase, error) {
	c := &FindingCase{}
	err := d.QueryRow(`SELECT id, task_id, origin_task_id, title, reason, COALESCE(severity,''), COALESCE(severity_reason,''),
COALESCE(report,''), version, report_version, active, created_at, updated_at,
(SELECT count(*) FROM finding_case_members m WHERE m.case_id=finding_cases.id)
FROM finding_cases WHERE id=$1`, id).Scan(&c.ID, &c.TaskID, &c.OriginTaskID, &c.Title, &c.Reason, &c.Severity, &c.SeverityReason,
		&c.Report, &c.Version, &c.ReportVersion, &c.Active, &c.CreatedAt, &c.UpdatedAt, &c.MemberCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// scanCase reads one finding_cases row (columns in GetFindingCase order).
func scanCase(row interface{ Scan(...any) error }) (*FindingCase, error) {
	c := &FindingCase{}
	err := row.Scan(&c.ID, &c.TaskID, &c.OriginTaskID, &c.Title, &c.Reason, &c.Severity, &c.SeverityReason,
		&c.Report, &c.Version, &c.ReportVersion, &c.Active, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("案例不存在")
	}
	return c, err
}

// FindingCaseID reports the active case containing a finding (0 = ungrouped).
func (d *DB) FindingCaseID(findingID int64) (int64, error) {
	var cid int64
	err := d.QueryRow(`SELECT COALESCE(m.case_id,0) FROM finding_case_members m JOIN finding_cases c ON c.id=m.case_id AND c.active WHERE m.finding_id=$1`, findingID).Scan(&cid)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return cid, err
}

func normalizeFindingIDs(ids []int64) ([]int64, error) {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("finding id 不合法")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func findingCaseBlockedTx(tx *sql.Tx, ids []int64) error {
	var blocked int
	if err := tx.QueryRow(`SELECT count(*) FROM finding_case_blocks b WHERE (b.left_id=ANY($1) AND b.right_id=ANY($1))`, ids).Scan(&blocked); err != nil {
		return err
	}
	if blocked > 0 {
		return fmt.Errorf("所选记录包含人工否决的组合，不能归并")
	}
	return nil
}

// MergeFindingCase is the Reporter's merge: the requested findings end up in one
// case. Existing groups are unioned into the lowest case id instead of spawning a
// second folder for the same defect. Original findings, reports and evidence stay.
func (d *DB) MergeFindingCase(ctx context.Context, taskID int64, ids []int64, title, reason, actor string) (*FindingCase, error) {
	if taskID <= 0 {
		return nil, fmt.Errorf("task_id 必填")
	}
	ids, err := normalizeFindingIDs(ids)
	if err != nil {
		return nil, err
	}
	if len(ids) < 2 || len(ids) > 20 {
		return nil, fmt.Errorf("归并需要 2 至 20 条记录")
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM findings WHERE id=ANY($1) AND task_id=$2 FOR UPDATE`, ids, taskID)
	if err != nil {
		return nil, err
	}
	found := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		found[id] = true
	}
	rows.Close()
	for _, id := range ids {
		if !found[id] {
			return nil, fmt.Errorf("发现 %d 不属于任务 %d", id, taskID)
		}
	}
	if err := findingCaseBlockedTx(tx, ids); err != nil {
		return nil, err
	}
	var target int64
	if err := tx.QueryRow(`SELECT COALESCE(min(case_id),0) FROM finding_case_members WHERE finding_id=ANY($1)`, ids).Scan(&target); err != nil {
		return nil, err
	}
	if target == 0 {
		// Nothing grouped yet: reuse the ordinary creation path after releasing
		// this transaction so create and union share one implementation.
		if err := tx.Rollback(); err != nil {
			return nil, err
		}
		c, err := d.CreateFindingCase(taskID, taskID, title, reason, "", ids, actor)
		if err != nil {
			return nil, err
		}
		if _, err := d.Exec(`UPDATE finding_case_suggestions SET state='accepted' WHERE task_id=$1 AND state='pending' AND finding_ids=$2::jsonb`, taskID, jsonIDs(ids)); err != nil {
			return nil, err
		}
		return c, nil
	}
	var caseTask int64
	if err := tx.QueryRow(`SELECT COALESCE(task_id,0) FROM finding_cases WHERE id=$1 AND active FOR UPDATE`, target).Scan(&caseTask); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("案例 %d 不存在或已关闭", target)
		}
		return nil, err
	}
	if caseTask != taskID {
		return nil, fmt.Errorf("案例不属于同一个任务")
	}
	var others []int64
	rows, err = tx.Query(`SELECT id FROM finding_cases WHERE active AND id IN (SELECT case_id FROM finding_case_members WHERE finding_id=ANY($1)) AND id<>$2 FOR UPDATE`, ids, target)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		others = append(others, id)
	}
	rows.Close()
	for _, id := range others {
		// Move every member of the losing folder; no finding is duplicated because
		// finding_case_members is keyed by finding id.
		if _, err := tx.Exec(`UPDATE finding_case_members SET case_id=$1 WHERE case_id=$2`, target, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE finding_cases SET active=false, updated_at=now() WHERE id=$1`, id); err != nil {
			return nil, err
		}
		if err := insertFindingCaseEvent(tx, target, "merge_case", actor, fmt.Sprintf("并入案例 #%d", id), nil); err != nil {
			return nil, err
		}
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT INTO finding_case_members(finding_id,case_id) VALUES($1,$2) ON CONFLICT (finding_id) DO UPDATE SET case_id=EXCLUDED.case_id`, id, target); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(title) != "" {
		if _, err := tx.Exec(`UPDATE finding_cases SET title=$2 WHERE id=$1`, target, strings.TrimSpace(title)); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`UPDATE finding_cases SET version=version+1, updated_at=now() WHERE id=$1`, target); err != nil {
		return nil, err
	}
	if err := insertFindingCaseEvent(tx, target, "merge", actor, reason, ids); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE finding_case_suggestions SET state='accepted' WHERE task_id=$1 AND state='pending' AND finding_ids=$2::jsonb`, taskID, jsonIDs(ids)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetFindingCase(target)
}

func jsonIDs(ids []int64) string {
	raw, _ := json.Marshal(ids)
	return string(raw)
}

// SuggestFindingCase stores a Reporter-proposed group (2..20 same-task records)
// for a human decision. It never creates or merges a case itself.
func (d *DB) SuggestFindingCase(ctx context.Context, taskID int64, ids []int64, title, reason string) (int64, error) {
	ids, err := normalizeFindingIDs(ids)
	if err != nil {
		return 0, err
	}
	if taskID <= 0 || len(ids) < 2 || len(ids) > 20 {
		return 0, fmt.Errorf("疑似分组需要 2 至 20 条同任务记录")
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(reason) == "" {
		return 0, fmt.Errorf("title 与 reason 必填")
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM findings WHERE id=ANY($1) AND task_id=$2`, ids, taskID).Scan(&count); err != nil {
		return 0, err
	}
	if count != len(ids) {
		return 0, fmt.Errorf("候选必须全部属于同一个任务")
	}
	if err := findingCaseBlockedTx(tx, ids); err != nil {
		return 0, err
	}
	var grouped int
	if err := tx.QueryRow(`SELECT count(*) FROM finding_case_members WHERE finding_id=ANY($1)`, ids).Scan(&grouped); err != nil {
		return 0, err
	}
	if grouped > 0 {
		return 0, fmt.Errorf("所选记录已有归并案例；已有案例的成员可作为上下文，不要再提交新的疑似分组")
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO finding_case_suggestions(task_id,finding_ids,title,reason,score)
VALUES($1,$2::jsonb,$3,$4,0) ON CONFLICT (task_id,finding_ids) DO UPDATE SET title=EXCLUDED.title,reason=EXCLUDED.reason
RETURNING id`, taskID, jsonIDs(ids), strings.TrimSpace(title), strings.TrimSpace(reason)).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
