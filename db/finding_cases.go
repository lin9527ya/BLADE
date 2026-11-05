package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// FindingCase 是「漏洞案例」：把同一根因/同一资产的重复上报归并成一个独立缺陷。
// 原始 findings 保留（成员），案例持有统一报告与版本，报告编辑用 report_version 做 CAS。
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

// UpdateFindingCaseReport 用 report_version 做 CAS 更新统一报告；expectedVersion=0 表示不校验。
func (d *DB) UpdateFindingCaseReport(id int64, report string, expectedVersion int64) (*FindingCase, error) {
	res, err := d.Exec(`UPDATE finding_cases SET report=$2, report_version=report_version+1, version=version+1, updated_at=now()
WHERE id=$1 AND ($3=0 OR report_version=$3)`, id, report, expectedVersion)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("报告版本冲突：请刷新后重试")
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
WHERE f.task_id=$1 AND f.id<>$2 AND COALESCE(f.status,'pending') NOT IN ('duplicate','retracted')
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
AND COALESCE(f.status,'pending') NOT IN ('duplicate','retracted')
AND NOT EXISTS (SELECT 1 FROM finding_case_members m WHERE m.finding_id=f.id)`, taskID).Scan(&st.Ungrouped); err != nil {
		return nil, err
	}
	if err := d.QueryRow(`SELECT count(*) FROM findings f WHERE f.task_id=$1
AND COALESCE(f.status,'pending') NOT IN ('duplicate','retracted')`, taskID).Scan(&st.Reports); err != nil {
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
WHERE f.task_id=$1 AND COALESCE(f.status,'pending') NOT IN ('duplicate','retracted')
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
