package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 指纹打法库（attack_playbooks）：跨任务、跨客户复用的「目标指纹 → 攻击手法」卡片。
//
// 与 company_knowledge 的分工：
//   - company_knowledge 是**某家客户**的长期结论（人工 promote，只读背景）；
//   - 本表是**全局**打法库，靠指纹匹配（技术栈 / favicon / 服务端口 / 页面标题），
//     由「打穿后自动沉淀」写入，带使用/成功/失败计数，越用越准。
//
// 指纹不是"身份"而是"相似性"：匹配是启发式打分，宁可少命中，也不要把无关目标
// 的打法硬套上去——误导一个正在动手的 worker 比不提示更危险。

// PlaybookFingerprint 是匹配用的目标指纹。所有字段都来自资产库的真实记录，
// 不由模型拍脑袋填写（record_playbook 会用 asset_ids 从库里补齐/覆盖）。
type PlaybookFingerprint struct {
	// Technologies 是 Wappalyzer 风格的识别结果，归一为小写、去重、排序。
	Technologies []string `json:"technologies,omitempty"`
	// FaviconMMH3 是 favicon 的 MMH3 哈希（最强信号：同一套应用/主题通常一致）。
	FaviconMMH3 string `json:"favicon_mmh3,omitempty"`
	// ServiceName / Port 描述服务（http/https/ssh/… 与端口）。
	ServiceName string `json:"service_name,omitempty"`
	Port        int    `json:"port,omitempty"`
	// PageTitle 是页面标题（弱信号，只做加分）。
	PageTitle string `json:"page_title,omitempty"`
}

// Normalize 归一指纹：技术小写去重排序、服务名小写、字段去空白。归一后才可哈希，
// 否则 "WordPress, PHP" 与 "php,wordpress " 会被当成两张卡。
func (fp PlaybookFingerprint) Normalize() PlaybookFingerprint {
	seen := map[string]bool{}
	techs := make([]string, 0, len(fp.Technologies))
	for _, t := range fp.Technologies {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		techs = append(techs, t)
	}
	sort.Strings(techs)
	return PlaybookFingerprint{
		Technologies: techs,
		FaviconMMH3:  strings.TrimSpace(fp.FaviconMMH3),
		ServiceName:  strings.ToLower(strings.TrimSpace(fp.ServiceName)),
		Port:         fp.Port,
		PageTitle:    strings.TrimSpace(fp.PageTitle),
	}
}

// Key 返回归一化指纹的 sha256（固定字段顺序由结构体保证）。
func (fp PlaybookFingerprint) Key() string {
	n := fp.Normalize()
	raw, _ := json.Marshal(n)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Playbook 是一张打法卡片。
type Playbook struct {
	ID               int64               `json:"id"`
	Fingerprint      PlaybookFingerprint `json:"fingerprint"`
	FingerprintKey   string              `json:"fingerprint_key"`
	TechniqueKey     string              `json:"technique_key"`
	Title            string              `json:"title"`
	Summary          string              `json:"summary"`
	Steps            string              `json:"steps"`    // Markdown：完整打法
	Evidence         string              `json:"evidence"` // 原始命令/请求/回显
	Confidence       string              `json:"confidence"`
	SourceTaskID     string              `json:"source_task_id,omitempty"`
	SourceNodeID     int64               `json:"source_node_id,omitempty"`
	SourceCompany    string              `json:"source_company,omitempty"`
	Status           string              `json:"status"`
	DeprecatedReason string              `json:"deprecated_reason,omitempty"`
	Revision         int                 `json:"revision"`
	Uses             int                 `json:"uses"`
	Successes        int                 `json:"successes"`
	Failures         int                 `json:"failures"`
	LastOutcome      string              `json:"last_outcome,omitempty"`
	LastNote         string              `json:"last_note,omitempty"`
	LastUsedAt       *time.Time          `json:"last_used_at,omitempty"`
	LastVerifiedAt   *time.Time          `json:"last_verified_at,omitempty"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

// PlaybookMatch 是一条带匹配分的卡片。
type PlaybookMatch struct {
	Playbook
	Score int `json:"score"`
}

const (
	PlaybookStatusActive     = "active"
	PlaybookStatusDeprecated = "deprecated"
)

// PlaybookOutcomes 是 feedback 的封闭词表。
var PlaybookOutcomes = []string{"worked", "failed", "inconclusive"}

func normalizePlaybookStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case PlaybookStatusActive:
		return PlaybookStatusActive
	case PlaybookStatusDeprecated:
		return PlaybookStatusDeprecated
	}
	return ""
}

// NormalizeTechniqueKey 把手法名归一成稳定 slug：小写、非字母数字/中文→'-'、压缩连字符。
// 留空时调用方用 title 兜底。
func NormalizeTechniqueKey(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r >= 0x4e00 && r <= 0x9fff:
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.':
			if !lastDash {
				b.WriteRune(r)
				lastDash = true
			}
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-_.")
	if len([]rune(out)) > 80 {
		out = string([]rune(out)[:80])
	}
	return out
}

const playbookCols = `id, fingerprint, fingerprint_key, technique_key, title, summary,
COALESCE(steps,''), COALESCE(evidence,''), confidence, COALESCE(source_task_id,''), source_node_id,
COALESCE(source_company,''), status, COALESCE(deprecated_reason,''), revision, uses, successes,
failures, COALESCE(last_outcome,''), COALESCE(last_note,''), last_used_at, last_verified_at,
created_at, updated_at`

func scanPlaybook(sc interface{ Scan(...any) error }) (*Playbook, error) {
	var p Playbook
	var raw []byte
	var lastUsed, lastVerified sql.NullTime
	if err := sc.Scan(&p.ID, &raw, &p.FingerprintKey, &p.TechniqueKey, &p.Title, &p.Summary,
		&p.Steps, &p.Evidence, &p.Confidence, &p.SourceTaskID, &p.SourceNodeID, &p.SourceCompany,
		&p.Status, &p.DeprecatedReason, &p.Revision, &p.Uses, &p.Successes, &p.Failures,
		&p.LastOutcome, &p.LastNote, &lastUsed, &lastVerified, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(raw, &p.Fingerprint)
	if lastUsed.Valid {
		p.LastUsedAt = &lastUsed.Time
	}
	if lastVerified.Valid {
		p.LastVerifiedAt = &lastVerified.Time
	}
	return &p, nil
}

// UpsertPlaybook 写入一张卡片：按 (fingerprint_key, technique_key) 去重。
// 命中时更新内容并 revision+1，**保留** uses/successes/failures（统计不能因更新丢失）。
// 返回 inserted=true 表示新建。
func (s *AssetStore) UpsertPlaybook(p Playbook) (*Playbook, bool, error) {
	fp := p.Fingerprint.Normalize()
	key := fp.Key()
	tech := NormalizeTechniqueKey(p.TechniqueKey)
	if tech == "" {
		tech = NormalizeTechniqueKey(p.Title)
	}
	if tech == "" {
		return nil, false, fmt.Errorf("technique_key 或 title 至少给一个（用于去重）")
	}
	title := strings.TrimSpace(p.Title)
	summary := strings.TrimSpace(p.Summary)
	steps := strings.TrimSpace(p.Steps)
	if title == "" || steps == "" {
		return nil, false, fmt.Errorf("title 与 steps 必填：卡片要能被人照着打")
	}
	confidence := NormalizeConfidence(p.Confidence)
	if confidence == "" {
		confidence = "high"
	}
	raw, _ := json.Marshal(fp)
	row := s.db.QueryRow(`
INSERT INTO attack_playbooks
  (fingerprint, fingerprint_key, technique_key, title, summary, steps, evidence, confidence,
   source_task_id, source_node_id, source_company)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (fingerprint_key, technique_key) DO UPDATE SET
  fingerprint      = EXCLUDED.fingerprint,
  title            = EXCLUDED.title,
  summary          = EXCLUDED.summary,
  steps            = EXCLUDED.steps,
  evidence         = CASE WHEN EXCLUDED.evidence <> '' THEN EXCLUDED.evidence ELSE attack_playbooks.evidence END,
  confidence       = EXCLUDED.confidence,
  source_task_id   = CASE WHEN EXCLUDED.source_task_id <> '' THEN EXCLUDED.source_task_id ELSE attack_playbooks.source_task_id END,
  source_node_id   = CASE WHEN EXCLUDED.source_node_id <> 0 THEN EXCLUDED.source_node_id ELSE attack_playbooks.source_node_id END,
  source_company   = CASE WHEN EXCLUDED.source_company <> '' THEN EXCLUDED.source_company ELSE attack_playbooks.source_company END,
  revision         = attack_playbooks.revision + 1,
  last_verified_at = now(),
  updated_at       = now()
RETURNING `+playbookCols+`, (xmax = 0) AS inserted`,
		string(raw), key, tech, title, summary, steps, strings.TrimSpace(p.Evidence), confidence,
		strings.TrimSpace(p.SourceTaskID), p.SourceNodeID, strings.TrimSpace(p.SourceCompany))

	var p2 Playbook
	var rawFP []byte
	var lastUsed, lastVerified sql.NullTime
	var inserted bool
	if err := row.Scan(&p2.ID, &rawFP, &p2.FingerprintKey, &p2.TechniqueKey, &p2.Title, &p2.Summary,
		&p2.Steps, &p2.Evidence, &p2.Confidence, &p2.SourceTaskID, &p2.SourceNodeID, &p2.SourceCompany,
		&p2.Status, &p2.DeprecatedReason, &p2.Revision, &p2.Uses, &p2.Successes, &p2.Failures,
		&p2.LastOutcome, &p2.LastNote, &lastUsed, &lastVerified, &p2.CreatedAt, &p2.UpdatedAt, &inserted); err != nil {
		return nil, false, err
	}
	_ = json.Unmarshal(rawFP, &p2.Fingerprint)
	if lastUsed.Valid {
		p2.LastUsedAt = &lastUsed.Time
	}
	if lastVerified.Valid {
		p2.LastVerifiedAt = &lastVerified.Time
	}
	return &p2, inserted, nil
}

// GetPlaybook 按 id 取一条。
func (s *AssetStore) GetPlaybook(id int64) (*Playbook, error) {
	row := s.db.QueryRow(`SELECT `+playbookCols+` FROM attack_playbooks WHERE id=$1`, id)
	p, err := scanPlaybook(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// PlaybookFilter 是列表查询条件（打法库页面）。
type PlaybookFilter struct {
	Q      string
	Status string
	Limit  int
	Offset int
}

// ListPlaybooks 返回卡片列表与总数。
func (s *AssetStore) ListPlaybooks(f PlaybookFilter) ([]*Playbook, int, error) {
	where := []string{"1=1"}
	args := []any{}
	if st := normalizePlaybookStatus(f.Status); st != "" {
		args = append(args, st)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+q+"%")
		where = append(where, fmt.Sprintf("(title ILIKE $%d OR summary ILIKE $%d OR steps ILIKE $%d OR fingerprint::text ILIKE $%d)",
			len(args), len(args), len(args), len(args)))
	}
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM attack_playbooks WHERE `+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.Query(`SELECT `+playbookCols+` FROM attack_playbooks WHERE `+
		strings.Join(where, " AND ")+
		fmt.Sprintf(" ORDER BY (status='active') DESC, successes DESC, updated_at DESC, id DESC LIMIT $%d OFFSET $%d",
			len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]*Playbook, 0, f.Limit)
	for rows.Next() {
		p, err := scanPlaybook(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// SetPlaybookStatus 启用/废弃一张卡片；废弃必须给理由（与公司知识同款纪律）。
func (s *AssetStore) SetPlaybookStatus(id int64, status, reason string) (int64, error) {
	st := normalizePlaybookStatus(status)
	if st == "" {
		return 0, fmt.Errorf("status 必须是 active | deprecated")
	}
	reason = strings.TrimSpace(reason)
	if st == PlaybookStatusDeprecated && reason == "" {
		return 0, fmt.Errorf("废弃必须给理由：后来人才知道为什么不能用")
	}
	res, err := s.db.Exec(`UPDATE attack_playbooks SET status=$1, deprecated_reason=$2, updated_at=now() WHERE id=$3`,
		st, reason, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TouchPlaybookUses 给被 lookup 命中的卡片累加使用次数。
func (s *AssetStore) TouchPlaybookUses(ids []int64) {
	if len(ids) == 0 {
		return
	}
	_, _ = s.db.Exec(`UPDATE attack_playbooks SET uses=uses+1, last_used_at=now() WHERE id = ANY($1)`, ids)
}

// RecordPlaybookFeedback 记录一次实战反馈。outcome 只认 worked/failed/inconclusive：
//   - worked：成功率 +1，并刷新 last_verified_at（"最近验证"排序靠它）；
//   - failed：失败 +1（打法可能过期/不适用）；
//   - inconclusive：只留痕，不计入成功率（没试成不等于打法错）。
func (s *AssetStore) RecordPlaybookFeedback(id int64, outcome, note string) (*Playbook, error) {
	outcome = strings.ToLower(strings.TrimSpace(outcome))
	valid := false
	for _, o := range PlaybookOutcomes {
		if outcome == o {
			valid = true
			break
		}
	}
	if !valid {
		return nil, fmt.Errorf("outcome 必须是 worked | failed | inconclusive")
	}
	var err error
	switch outcome {
	case "worked":
		_, err = s.db.Exec(`UPDATE attack_playbooks SET successes=successes+1, last_outcome=$2,
			last_note=$3, last_used_at=now(), last_verified_at=now() WHERE id=$1`, id, outcome, strings.TrimSpace(note))
	case "failed":
		_, err = s.db.Exec(`UPDATE attack_playbooks SET failures=failures+1, last_outcome=$2,
			last_note=$3, last_used_at=now() WHERE id=$1`, id, outcome, strings.TrimSpace(note))
	default:
		_, err = s.db.Exec(`UPDATE attack_playbooks SET last_outcome=$2, last_note=$3,
			last_used_at=now() WHERE id=$1`, id, outcome, strings.TrimSpace(note))
	}
	if err != nil {
		return nil, err
	}
	return s.GetPlaybook(id)
}

// TaskFingerprint 聚合本任务范围内 service 资产的指纹信号。
// taskID<=0（无任务上下文）返回空——它只用于"这个任务可能撞上哪类打法"的提示。
func (s *AssetStore) TaskFingerprint(taskID int64) TaskFingerprint {
	if taskID <= 0 {
		return TaskFingerprint{}
	}
	rows, err := s.db.Query(`WITH `+scopeTargetCTE+`
SELECT COALESCE(array_to_json(technologies)::text,'[]'), COALESCE(favicon_mmh3,''), COALESCE(service_name,''),
       COALESCE(port,0), COALESCE(page_title,'')
FROM assets WHERE type='service' AND id IN (SELECT id FROM target) LIMIT 2000`, taskID)
	if err != nil {
		return TaskFingerprint{}
	}
	defer rows.Close()
	techCount := map[string]int{}
	favicons := map[string]bool{}
	services := map[string]bool{}
	servicePorts := map[string]bool{}
	titles := map[string]bool{}
	for rows.Next() {
		var techRaw, favicon, service, title string
		var port int
		if err := rows.Scan(&techRaw, &favicon, &service, &port, &title); err != nil {
			continue
		}
		var techs []string
		_ = json.Unmarshal([]byte(techRaw), &techs)
		for _, t := range techs {
			t = strings.ToLower(strings.TrimSpace(t))
			if t != "" {
				techCount[t]++
			}
		}
		if favicon = strings.TrimSpace(favicon); favicon != "" {
			favicons[favicon] = true
		}
		service = strings.ToLower(strings.TrimSpace(service))
		if service != "" {
			services[service] = true
			if port > 0 {
				servicePorts[fmt.Sprintf("%s:%d", service, port)] = true
			}
		}
		if title = strings.TrimSpace(title); title != "" && len(titles) < 10 {
			titles[title] = true
		}
	}
	type tc struct {
		t string
		n int
	}
	ordered := make([]tc, 0, len(techCount))
	for t, n := range techCount {
		ordered = append(ordered, tc{t, n})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].n != ordered[j].n {
			return ordered[i].n > ordered[j].n
		}
		return ordered[i].t < ordered[j].t
	})
	tf := TaskFingerprint{}
	for _, x := range ordered {
		if len(tf.Technologies) >= 30 {
			break
		}
		tf.Technologies = append(tf.Technologies, x.t)
	}
	tf.Favicons = capSorted(favicons, 20)
	tf.Services = capSorted(services, 10)
	tf.ServicePorts = capSorted(servicePorts, 20)
	tf.Titles = capSorted(titles, 10)
	return tf
}

// TaskFingerprint 是一个任务范围内观察到的指纹信号集合（用于 task 级匹配）。
type TaskFingerprint struct {
	Technologies []string `json:"technologies,omitempty"`
	Favicons     []string `json:"favicons,omitempty"`
	Services     []string `json:"services,omitempty"`
	ServicePorts []string `json:"service_ports,omitempty"`
	Titles       []string `json:"titles,omitempty"`
}

func capSorted(set map[string]bool, max int) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// genericTechnologies 是"到处都是"的技术标签：它们只做弱加分，**不能单独构成匹配**。
// 否则每个 nginx+php 的站点都会命中所有 nginx+php 的打法——那等于没有匹配。
var genericTechnologies = map[string]bool{
	"nginx": true, "apache": true, "httpd": true, "iis": true, "tomcat": true,
	"php": true, "mysql": true, "mariadb": true, "postgresql": true, "redis": true,
	"http": true, "https": true, "openssl": true, "openssh": true, "cloudflare": true,
	"jquery": true, "bootstrap": true, "react": true, "vue": true, "angular": true,
	"node.js": true, "express": true, "hsts": true, "google-analytics": true,
}

type playbookSignals struct {
	techs        map[string]bool
	favicons     map[string]bool
	services     map[string]bool
	servicePorts map[string]bool
	titles       map[string]bool
}

func signalsFromFingerprint(fp PlaybookFingerprint) playbookSignals {
	fp = fp.Normalize()
	s := playbookSignals{
		techs: map[string]bool{}, favicons: map[string]bool{}, services: map[string]bool{},
		servicePorts: map[string]bool{}, titles: map[string]bool{},
	}
	for _, t := range fp.Technologies {
		s.techs[t] = true
	}
	if fp.FaviconMMH3 != "" {
		s.favicons[fp.FaviconMMH3] = true
	}
	if fp.ServiceName != "" {
		s.services[fp.ServiceName] = true
		if fp.Port > 0 {
			s.servicePorts[fmt.Sprintf("%s:%d", fp.ServiceName, fp.Port)] = true
		}
	}
	if fp.PageTitle != "" {
		s.titles[fp.PageTitle] = true
	}
	return s
}

func signalsFromTask(tf TaskFingerprint) playbookSignals {
	s := playbookSignals{
		techs: map[string]bool{}, favicons: map[string]bool{}, services: map[string]bool{},
		servicePorts: map[string]bool{}, titles: map[string]bool{},
	}
	for _, t := range tf.Technologies {
		s.techs[strings.ToLower(t)] = true
	}
	for _, f := range tf.Favicons {
		s.favicons[f] = true
	}
	for _, v := range tf.Services {
		s.services[v] = true
	}
	for _, v := range tf.ServicePorts {
		s.servicePorts[v] = true
	}
	for _, v := range tf.Titles {
		s.titles[v] = true
	}
	return s
}

// scorePlaybook 返回匹配分；0 = 不匹配。规则见包注释与计划：
// favicon 命中 100（强信号）；非通用技术每项 30（上限 90）、通用技术每项 10（上限 20）；
// service+port 15 / 仅 service 5；标题完全相同 10。门槛 40 且必须有一个强信号。
func scorePlaybook(p *Playbook, s playbookSignals) int {
	fp := p.Fingerprint.Normalize()
	score := 0
	strong := false
	if fp.FaviconMMH3 != "" && s.favicons[fp.FaviconMMH3] {
		score += 100
		strong = true
	}
	specific, generic := 0, 0
	for _, t := range fp.Technologies {
		if !s.techs[t] {
			continue
		}
		if genericTechnologies[t] {
			generic++
		} else {
			specific++
			strong = true
		}
	}
	if specific > 3 {
		specific = 3
	}
	if generic > 2 {
		generic = 2
	}
	score += specific*30 + generic*10
	if fp.ServiceName != "" {
		if fp.Port > 0 && s.servicePorts[fmt.Sprintf("%s:%d", fp.ServiceName, fp.Port)] {
			score += 15
		} else if s.services[fp.ServiceName] {
			score += 5
		}
	}
	if fp.PageTitle != "" && s.titles[fp.PageTitle] {
		score += 10
	}
	if score < 40 || !strong {
		return 0
	}
	return score
}

func playbookSuccessRate(p *Playbook) float64 {
	total := p.Successes + p.Failures
	if total == 0 {
		return 0
	}
	return float64(p.Successes) / float64(total)
}

func rankMatches(out []PlaybookMatch) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		ri, rj := playbookSuccessRate(&out[i].Playbook), playbookSuccessRate(&out[j].Playbook)
		if ri != rj {
			return ri > rj
		}
		vi, vj := out[i].LastVerifiedAt, out[j].LastVerifiedAt
		switch {
		case vi != nil && vj != nil && !vi.Equal(*vj):
			return vi.After(*vj)
		case vi != nil && vj == nil:
			return true
		case vi == nil && vj != nil:
			return false
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
}

// loadActivePlaybooks 取活跃卡片用于内存打分。表是人工规模（千级以内），
// 打分放在 Go 里是为了让"为什么命中"可读、可测，而不是一条难调的 SQL。
func (s *AssetStore) loadActivePlaybooks(limit int) ([]*Playbook, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT `+playbookCols+` FROM attack_playbooks
WHERE status='active' ORDER BY successes DESC, updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Playbook, 0, limit)
	for rows.Next() {
		p, err := scanPlaybook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MatchPlaybooks 按给定指纹匹配活跃卡片（lookup_playbooks 用）。
func (s *AssetStore) MatchPlaybooks(fp PlaybookFingerprint, limit int) ([]PlaybookMatch, error) {
	if limit <= 0 {
		limit = 3
	}
	cards, err := s.loadActivePlaybooks(500)
	if err != nil {
		return nil, err
	}
	sig := signalsFromFingerprint(fp)
	out := make([]PlaybookMatch, 0, 3)
	for _, p := range cards {
		if score := scorePlaybook(p, sig); score > 0 {
			out = append(out, PlaybookMatch{Playbook: *p, Score: score})
		}
	}
	rankMatches(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MatchPlaybooksForTask 用本任务范围内的指纹信号匹配（态势注入用）。
func (s *AssetStore) MatchPlaybooksForTask(taskID int64, limit int) ([]PlaybookMatch, error) {
	tf := s.TaskFingerprint(taskID)
	if len(tf.Technologies) == 0 && len(tf.Favicons) == 0 && len(tf.Services) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 3
	}
	cards, err := s.loadActivePlaybooks(500)
	if err != nil {
		return nil, err
	}
	sig := signalsFromTask(tf)
	out := make([]PlaybookMatch, 0, 3)
	for _, p := range cards {
		if score := scorePlaybook(p, sig); score > 0 {
			out = append(out, PlaybookMatch{Playbook: *p, Score: score})
		}
	}
	rankMatches(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// TaskCompanyName 取任务关联的第一个企业名（打法卡片的来源客户，审计用）。
func (s *AssetStore) TaskCompanyName(taskID int64) string {
	if taskID <= 0 {
		return ""
	}
	var name string
	_ = s.db.QueryRow(`SELECT COALESCE(c.name,'') FROM task_scope ts
JOIN companies c ON c.id = ts.company_id
WHERE ts.task_id=$1 AND ts.kind='company' ORDER BY ts.id LIMIT 1`, taskID).Scan(&name)
	return name
}
