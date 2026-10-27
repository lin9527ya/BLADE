package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CVE 库（全局）：人工维护的漏洞知识 + 指纹/产品/版本匹配 + 可选利用脚本。
//
// 与 attack_playbooks 的区别：打法库是"打穿后沉淀"的实战卡片；CVE 库是操作者录入的
// 漏洞知识。匹配只是**线索**，脚本执行也必须走现有授权/审批门——这里不自动产生
// finding / foothold，也不把 CVE 命中当漏洞证据。

type CVE struct {
	ID                int64               `json:"id"`
	CVEID             string              `json:"cve_id"`
	Title             string              `json:"title"`
	Severity          string              `json:"severity,omitempty"`
	CVSSScore         float64             `json:"cvss_score"`
	Vendor            string              `json:"vendor,omitempty"`
	Description       string              `json:"description,omitempty"`
	AffectedProducts  []string            `json:"affected_products"`
	VersionConstraint string              `json:"version_constraint"`
	Fingerprint       PlaybookFingerprint `json:"fingerprint"`
	ReconSyntax       string              `json:"recon_syntax,omitempty"` // FOFA/Hunter/Shodan 等测绘语法，一行一条
	ProbeGuide        string              `json:"probe_guide,omitempty"`
	Refs              []string            `json:"refs,omitempty"`
	ExploitScript     string              `json:"exploit_script,omitempty"`
	ExploitLanguage   string              `json:"exploit_language,omitempty"`
	ExploitFilename   string              `json:"exploit_filename,omitempty"`
	ExploitUsage      string              `json:"exploit_usage,omitempty"`
	ExploitSHA256     string              `json:"exploit_sha256,omitempty"`
	ExploitBytes      int                 `json:"exploit_bytes"`
	HasExploit        bool                `json:"has_exploit"`
	Status            string              `json:"status"`
	DeprecatedReason  string              `json:"deprecated_reason,omitempty"`
	Revision          int                 `json:"revision"`
	Uses              int                 `json:"uses"`
	Executions        int                 `json:"executions"`
	LastUsedAt        *time.Time          `json:"last_used_at,omitempty"`
	LastExecutedAt    *time.Time          `json:"last_executed_at,omitempty"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
}

type CVEMatch struct {
	CVE
	Score          int      `json:"score"`
	MatchReasons   []string `json:"match_reasons"`
	VersionMatch   string   `json:"version_match"` // compatible|unconstrained|version_unknown|product_unknown|mismatch
	AssetVersion   string   `json:"asset_version,omitempty"`
	MatchedProduct string   `json:"matched_product,omitempty"`
	AssetID        int64    `json:"asset_id,omitempty"`
	AssetLabel     string   `json:"asset_label,omitempty"`
}

type CVEFilter struct {
	Q          string
	Status     string // ""=active, all=全部
	Severity   string
	Language   string
	HasExploit *bool
	Limit      int
	Offset     int
}

// CVEAssetSignals 是从一台资产/一组手填指纹解析出的匹配信号。Products 是
// "产品 → 版本（未知为空）"；Technologies 保留原始 tech 串用于展示/回退。
type CVEAssetSignals struct {
	AssetID      int64
	AssetLabel   string
	Products     map[string]string
	Technologies []string
	FaviconMMH3  string
	ServiceName  string
	Port         int
	PageTitle    string
}

const (
	CVEStatusActive     = "active"
	CVEStatusDeprecated = "deprecated"
)

var (
	cveIDRe     = regexp.MustCompile(`^(?:CVE-\d{4}-\d{4,7}|(?i:blade-\d{4}-\d{4,7}))$`)
	cpeRe       = regexp.MustCompile(`(?i)cpe:2\.3:[aho]:([^:]+):([^:]+):([^:]+)`)
	techVerRe   = regexp.MustCompile(`^(.*?)[\s:/_@-]+v?([0-9][0-9A-Za-z.*+_-]*)$`)
	cvePerms    = map[string]bool{"python3": true, "python": true, "bash": true, "sh": true, "ruby": true, "perl": true, "php": true, "node": true, "nodejs": true, "pwsh": true, "powershell": true}
	cveLangExt  = map[string]string{"python3": ".py", "python": ".py", "bash": ".sh", "sh": ".sh", "ruby": ".rb", "perl": ".pl", "php": ".php", "node": ".js", "nodejs": ".js", "pwsh": ".ps1", "powershell": ".ps1"}
	cveVersionR = map[string]int{"compatible": 4, "unconstrained": 3, "version_unknown": 2, "product_unknown": 1}
)

// NormalizeCVEID canonicalizes an identifier: real CVE IDs uppercase, our own
// blade-YYYY-NNNN internal IDs lowercase (matches what the importer displays).
// Whitespace is always trimmed.
func NormalizeCVEID(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(s), "blade-") {
		return strings.ToLower(s)
	}
	return strings.ToUpper(s)
}

func normalizeCVEStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case CVEStatusActive:
		return CVEStatusActive
	case CVEStatusDeprecated:
		return CVEStatusDeprecated
	}
	return ""
}

// NormalizeExploitLanguage maps aliases onto a small canonical interpreter set.
func NormalizeExploitLanguage(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "python", "py":
		return "python3"
	case "nodejs", "node.js":
		return "node"
	case "powershell":
		return "pwsh"
	}
	if cvePerms[s] {
		return s
	}
	return ""
}

func ExploitScriptExt(lang string) string {
	lang = NormalizeExploitLanguage(lang)
	if ext := cveLangExt[lang]; ext != "" {
		return ext
	}
	return ".txt"
}

func normalizeCVEProducts(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = normalizeProductName(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func normalizeCVEStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func sanitizeCVEFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
	if name == "" || name == "." {
		return ""
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	return name
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func validateCVE(c *CVE) error {
	c.CVEID = NormalizeCVEID(c.CVEID)
	if !cveIDRe.MatchString(c.CVEID) {
		return fmt.Errorf("编号格式应为 CVE-YYYY-NNNN 或 blade-YYYY-NNNN")
	}
	c.Title = strings.TrimSpace(c.Title)
	if c.Title == "" || len([]rune(c.Title)) > 300 {
		return fmt.Errorf("标题不能为空且不超过 300 字符")
	}
	c.Severity = strings.ToLower(strings.TrimSpace(c.Severity))
	if c.Severity == "" {
		c.Severity = SeverityMedium
	}
	if !ValidSeverity(c.Severity) {
		return fmt.Errorf("severity 必须是 critical|high|medium|low")
	}
	if c.CVSSScore < 0 || c.CVSSScore > 10 {
		return fmt.Errorf("CVSS 必须在 0–10")
	}
	c.Vendor = strings.TrimSpace(c.Vendor)
	c.Description = strings.TrimSpace(c.Description)
	c.ProbeGuide = strings.TrimSpace(c.ProbeGuide)
	c.ReconSyntax = strings.TrimSpace(c.ReconSyntax)
	if len([]rune(c.Description)) > 64*1024 || len([]rune(c.ProbeGuide)) > 64*1024 {
		return fmt.Errorf("描述/探测指引不能超过 64K 字符")
	}
	if len([]rune(c.ReconSyntax)) > 16*1024 {
		return fmt.Errorf("测绘语法不能超过 16K 字符")
	}
	c.AffectedProducts = normalizeCVEProducts(c.AffectedProducts)
	if len(c.AffectedProducts) > 20 {
		return fmt.Errorf("受影响产品最多 20 个")
	}
	c.VersionConstraint = strings.TrimSpace(c.VersionConstraint)
	if c.VersionConstraint != "" {
		if _, err := ParseVersionConstraint(c.VersionConstraint); err != nil {
			return err
		}
		if len(c.AffectedProducts) == 0 {
			return fmt.Errorf("设置版本约束时必须至少有一个受影响产品")
		}
	}
	c.Fingerprint = c.Fingerprint.Normalize()
	if len(c.Fingerprint.Technologies) > 50 {
		return fmt.Errorf("指纹技术栈最多 50 个")
	}
	if c.Fingerprint.Port < 0 || c.Fingerprint.Port > 65535 {
		return fmt.Errorf("端口必须在 1–65535")
	}
	if len(c.AffectedProducts) == 0 && c.Fingerprint.FaviconMMH3 == "" &&
		len(c.Fingerprint.Technologies) == 0 && c.Fingerprint.ServiceName == "" {
		return fmt.Errorf("至少提供一个匹配信号（产品，或 favicon/技术栈/服务）")
	}
	c.Refs = normalizeCVEStrings(c.Refs)
	if len(c.Refs) > 30 {
		return fmt.Errorf("参考链接最多 30 条")
	}
	for _, ref := range c.Refs {
		if len([]rune(ref)) > 2048 {
			return fmt.Errorf("参考链接过长")
		}
	}
	c.ExploitLanguage = NormalizeExploitLanguage(c.ExploitLanguage)
	c.ExploitScript = strings.TrimSpace(c.ExploitScript)
	c.ExploitUsage = strings.TrimSpace(c.ExploitUsage)
	if len([]rune(c.ExploitUsage)) > 8*1024 {
		return fmt.Errorf("脚本使用说明不能超过 8K 字符")
	}
	if c.ExploitScript != "" {
		if c.ExploitLanguage == "" {
			return fmt.Errorf("脚本语言只支持 python3|bash|sh|ruby|perl|php|node|pwsh")
		}
		if len(c.ExploitScript) > 1<<20 {
			return fmt.Errorf("脚本不能超过 1 MiB")
		}
		if !utf8.ValidString(c.ExploitScript) || strings.IndexByte(c.ExploitScript, 0) >= 0 {
			return fmt.Errorf("脚本必须是有效 UTF-8 且不含 NUL")
		}
		c.ExploitSHA256 = sha256Hex(c.ExploitScript)
		c.ExploitFilename = sanitizeCVEFilename(c.ExploitFilename)
		if c.ExploitFilename == "" {
			c.ExploitFilename = "cve" + ExploitScriptExt(c.ExploitLanguage)
		}
	} else {
		c.ExploitLanguage = ""
		c.ExploitFilename = ""
		c.ExploitSHA256 = ""
	}
	c.Status = normalizeCVEStatus(c.Status)
	if c.Status == "" {
		c.Status = CVEStatusActive
	}
	return nil
}

// NormalizeCVE validates and normalizes an import draft without writing it.
func NormalizeCVE(c CVE) (CVE, error) {
	if err := validateCVE(&c); err != nil {
		return c, err
	}
	return c, nil
}

const cveFullCols = `id, cve_id, title, COALESCE(severity,''), COALESCE(vendor,''), COALESCE(description,''),
affected_products, COALESCE(version_constraint,''), fingerprint, COALESCE(probe_guide,''), refs,
exploit_script, COALESCE(exploit_language,''), COALESCE(exploit_filename,''), COALESCE(exploit_usage,''),
COALESCE(exploit_sha256,''), octet_length(exploit_script), status, COALESCE(deprecated_reason,''),
revision, uses, executions, last_used_at, last_executed_at, created_at, updated_at, COALESCE(cvss_score,0),
COALESCE(recon_syntax,'')`

// 列表不把脚本正文读回来：1 MiB/条的脚本 × 50 条会把列表页拖垮。
const cveListCols = `id, cve_id, title, COALESCE(severity,''), COALESCE(vendor,''), left(COALESCE(description,''),300),
affected_products, COALESCE(version_constraint,''), fingerprint, ''::text AS probe_guide, '[]'::jsonb AS refs,
''::text AS exploit_script, COALESCE(exploit_language,''), COALESCE(exploit_filename,''), COALESCE(exploit_usage,''),
COALESCE(exploit_sha256,''), octet_length(exploit_script), status, COALESCE(deprecated_reason,''),
revision, uses, executions, last_used_at, last_executed_at, created_at, updated_at, COALESCE(cvss_score,0),
COALESCE(recon_syntax,'')`

func scanCVE(sc interface{ Scan(...any) error }) (*CVE, error) {
	var c CVE
	var productsRaw, fpRaw, refsRaw []byte
	var lastUsed, lastExec sql.NullTime
	if err := sc.Scan(&c.ID, &c.CVEID, &c.Title, &c.Severity, &c.Vendor, &c.Description,
		&productsRaw, &c.VersionConstraint, &fpRaw, &c.ProbeGuide, &refsRaw,
		&c.ExploitScript, &c.ExploitLanguage, &c.ExploitFilename, &c.ExploitUsage,
		&c.ExploitSHA256, &c.ExploitBytes, &c.Status, &c.DeprecatedReason,
		&c.Revision, &c.Uses, &c.Executions, &lastUsed, &lastExec, &c.CreatedAt, &c.UpdatedAt, &c.CVSSScore, &c.ReconSyntax); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(productsRaw, &c.AffectedProducts)
	_ = json.Unmarshal(fpRaw, &c.Fingerprint)
	_ = json.Unmarshal(refsRaw, &c.Refs)
	if c.AffectedProducts == nil {
		c.AffectedProducts = []string{}
	}
	if c.Refs == nil {
		c.Refs = []string{}
	}
	if lastUsed.Valid {
		c.LastUsedAt = &lastUsed.Time
	}
	if lastExec.Valid {
		c.LastExecutedAt = &lastExec.Time
	}
	c.HasExploit = c.ExploitSHA256 != "" || c.ExploitScript != ""
	return &c, nil
}

func cveJSON(c CVE) (products, fp, refs string) {
	pb, _ := json.Marshal(normalizeCVEProducts(c.AffectedProducts))
	fb, _ := json.Marshal(c.Fingerprint.Normalize())
	rb, _ := json.Marshal(normalizeCVEStrings(c.Refs))
	return string(pb), string(fb), string(rb)
}

func (s *AssetStore) CreateCVE(c CVE) (*CVE, error) {
	if err := validateCVE(&c); err != nil {
		return nil, err
	}
	if c.Revision <= 0 {
		c.Revision = 1
	}
	products, fp, refs := cveJSON(c)
	row := s.db.QueryRow(`INSERT INTO cve_library
(cve_id, title, severity, vendor, description, affected_products, version_constraint, fingerprint,
 probe_guide, refs, exploit_script, exploit_language, exploit_filename, exploit_usage, exploit_sha256,
 status, deprecated_reason, revision, uses, executions, cvss_score, recon_syntax)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,0,0,$19,$20)
RETURNING `+cveFullCols,
		c.CVEID, c.Title, c.Severity, c.Vendor, c.Description, products, c.VersionConstraint, fp,
		c.ProbeGuide, refs, c.ExploitScript, c.ExploitLanguage, c.ExploitFilename, c.ExploitUsage, c.ExploitSHA256,
		c.Status, c.DeprecatedReason, c.Revision, c.CVSSScore, c.ReconSyntax)
	out, err := scanCVE(row)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *AssetStore) UpdateCVE(id int64, c CVE) (*CVE, error) {
	if err := validateCVE(&c); err != nil {
		return nil, err
	}
	products, fp, refs := cveJSON(c)
	row := s.db.QueryRow(`UPDATE cve_library SET
 cve_id=$2, title=$3, severity=$4, vendor=$5, description=$6, affected_products=$7,
 version_constraint=$8, fingerprint=$9, probe_guide=$10, refs=$11, exploit_script=$12,
 exploit_language=$13, exploit_filename=$14, exploit_usage=$15, exploit_sha256=$16,
 revision=revision+1, cvss_score=$17, recon_syntax=$18
WHERE id=$1 RETURNING `+cveFullCols,
		id, c.CVEID, c.Title, c.Severity, c.Vendor, c.Description, products,
		c.VersionConstraint, fp, c.ProbeGuide, refs, c.ExploitScript,
		c.ExploitLanguage, c.ExploitFilename, c.ExploitUsage, c.ExploitSHA256, c.CVSSScore, c.ReconSyntax)
	out, err := scanCVE(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return out, err
}

func (s *AssetStore) GetCVE(id int64) (*CVE, error) {
	row := s.db.QueryRow(`SELECT `+cveFullCols+` FROM cve_library WHERE id=$1`, id)
	c, err := scanCVE(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func (s *AssetStore) GetCVEByCVEID(cveID string) (*CVE, error) {
	row := s.db.QueryRow(`SELECT `+cveFullCols+` FROM cve_library WHERE cve_id=$1`, NormalizeCVEID(cveID))
	c, err := scanCVE(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// NextInternalCVESeq 返回 blade-<year>-NNNN 的下一个可用序号。
// 只按同年前缀取最大数字后缀，不依赖数据库序列：内部编号要和人工录入/导入的
// 既有编号共存，且 cve_id 的唯一约束仍由数据库兜底（并发预览撞号时创建会 409）。
func (s *AssetStore) NextInternalCVESeq(year int) (int, error) {
	prefix := fmt.Sprintf("blade-%d-", year)
	rows, err := s.db.Query(`SELECT cve_id FROM cve_library WHERE lower(cve_id) LIKE $1`, prefix+"%")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	next := 1
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		suffix := strings.TrimPrefix(strings.ToLower(id), prefix)
		n, err := strconv.Atoi(suffix)
		if err == nil && n >= next {
			next = n + 1
		}
	}
	return next, rows.Err()
}

func (s *AssetStore) ListCVEs(f CVEFilter) ([]*CVE, int, error) {
	where := []string{"1=1"}
	args := []any{}
	status := strings.ToLower(strings.TrimSpace(f.Status))
	if status == "" {
		status = CVEStatusActive
	}
	if status != "all" {
		st := normalizeCVEStatus(status)
		if st == "" {
			return nil, 0, fmt.Errorf("status 必须是 active|deprecated|all")
		}
		args = append(args, st)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	if sev := strings.ToLower(strings.TrimSpace(f.Severity)); sev != "" {
		if !ValidSeverity(sev) {
			return nil, 0, fmt.Errorf("severity 必须是 critical|high|medium|low")
		}
		args = append(args, sev)
		where = append(where, fmt.Sprintf("severity=$%d", len(args)))
	}
	if lang := NormalizeExploitLanguage(f.Language); lang != "" {
		args = append(args, lang)
		where = append(where, fmt.Sprintf("exploit_language=$%d", len(args)))
	}
	if f.HasExploit != nil {
		if *f.HasExploit {
			where = append(where, "exploit_sha256 <> ''")
		} else {
			where = append(where, "exploit_sha256 = ''")
		}
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+q+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(cve_id ILIKE $%d OR title ILIKE $%d OR description ILIKE $%d OR affected_products::text ILIKE $%d OR fingerprint::text ILIKE $%d)", n, n, n, n, n))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM cve_library WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	// 默认按编号从大到小：自研 blade-YYYY-NNNN 在前，真实 CVE 在后；两类各自按
	// 年份、序号数值降序（不按字符串比较，避免 CVE-2025-9999 > CVE-2025-10000
	// 这种位数不同的字典序错误）。启用中的记录仍排在已废弃之前。
	rows, err := s.db.Query(`SELECT `+cveListCols+` FROM cve_library WHERE `+whereSQL+
		fmt.Sprintf(` ORDER BY
  CASE WHEN status='active' THEN 0 ELSE 1 END,
  CASE WHEN lower(cve_id) LIKE 'blade-%%' THEN 0 ELSE 1 END,
  COALESCE(NULLIF(substring(lower(cve_id) from '^(?:blade|cve)-([0-9]{4})-'), '')::int, 0) DESC,
  COALESCE(NULLIF(substring(lower(cve_id) from '^(?:blade|cve)-[0-9]{4}-([0-9]+)$'), '')::bigint, 0) DESC,
  lower(cve_id) DESC, id DESC
 LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*CVE{}
	for rows.Next() {
		c, err := scanCVE(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

func (s *AssetStore) SetCVEStatus(id int64, status, reason string) (int64, error) {
	st := normalizeCVEStatus(status)
	if st == "" {
		return 0, fmt.Errorf("status 必须是 active|deprecated")
	}
	reason = strings.TrimSpace(reason)
	if st == CVEStatusDeprecated && reason == "" {
		return 0, fmt.Errorf("废弃必须给出理由")
	}
	if st == CVEStatusActive {
		reason = ""
	}
	res, err := s.db.Exec(`UPDATE cve_library SET status=$2, deprecated_reason=$3 WHERE id=$1`, id, st, reason)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *AssetStore) DeleteCVE(id int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM cve_library WHERE id=$1`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *AssetStore) UpdateCVEScript(id int64, script []byte, language, filename string) (*CVE, error) {
	c, err := s.GetCVE(id)
	if err != nil || c == nil {
		return c, err
	}
	c.ExploitScript = string(script)
	c.ExploitLanguage = language
	c.ExploitFilename = filename
	if err := validateCVE(c); err != nil {
		return nil, err
	}
	products, fp, refs := cveJSON(*c)
	row := s.db.QueryRow(`UPDATE cve_library SET exploit_script=$2, exploit_language=$3,
 exploit_filename=$4, exploit_usage=$5, exploit_sha256=$6, affected_products=$7,
 version_constraint=$8, fingerprint=$9, probe_guide=$10, refs=$11, revision=revision+1
 WHERE id=$1 RETURNING `+cveFullCols,
		id, c.ExploitScript, c.ExploitLanguage, c.ExploitFilename, c.ExploitUsage, c.ExploitSHA256,
		products, c.VersionConstraint, fp, c.ProbeGuide, refs)
	out, err := scanCVE(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return out, err
}

func (s *AssetStore) ClearCVEScript(id int64) (*CVE, error) {
	row := s.db.QueryRow(`UPDATE cve_library SET exploit_script='', exploit_language='',
 exploit_filename='', exploit_usage='', exploit_sha256='', revision=revision+1
 WHERE id=$1 RETURNING `+cveFullCols, id)
	out, err := scanCVE(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return out, err
}

func (s *AssetStore) TouchCVEUses(ids []int64) {
	if len(ids) == 0 {
		return
	}
	_, _ = s.db.Exec(`UPDATE cve_library SET uses=uses+1, last_used_at=now() WHERE id = ANY($1)`, ids)
}

func (s *AssetStore) RecordCVEExecution(id int64) {
	_, _ = s.db.Exec(`UPDATE cve_library SET executions=executions+1, last_executed_at=now() WHERE id=$1`, id)
}

// ---------- 指纹/产品/版本匹配 ----------

func normalizeProductName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("_", " ", "/", " ", "\\", " ", "@", " ", ":", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func splitProductVersion(tech string) (product, version string) {
	tech = strings.TrimSpace(tech)
	if tech == "" {
		return "", ""
	}
	// 去掉尾部括号（如 "Apache/2.4.49 (Unix)"），避免把平台信息带进版本。
	if i := strings.Index(tech, "("); i > 0 {
		tech = strings.TrimSpace(tech[:i])
	}
	if m := techVerRe.FindStringSubmatch(tech); m != nil {
		product = normalizeProductName(m[1])
		version = strings.TrimSpace(m[2])
		if product != "" {
			return product, version
		}
	}
	return normalizeProductName(tech), ""
}

func productMatches(cveProduct, assetProduct string) bool {
	cveProduct = normalizeProductName(cveProduct)
	assetProduct = normalizeProductName(assetProduct)
	if cveProduct == "" || assetProduct == "" {
		return false
	}
	if cveProduct == assetProduct {
		return true
	}
	if len([]rune(cveProduct)) >= 4 && strings.HasPrefix(assetProduct, cveProduct) {
		return true
	}
	if len([]rune(assetProduct)) >= 4 && strings.Contains(" "+cveProduct+" ", " "+assetProduct+" ") {
		return true
	}
	// 多词产品：允许短词精确出现在另一侧（"apache tomcat" ↔ "tomcat"）。
	for _, tok := range strings.Fields(assetProduct) {
		if len([]rune(tok)) >= 4 && strings.Contains(" "+cveProduct+" ", " "+tok+" ") {
			return true
		}
	}
	return false
}

func addProductVersion(products map[string]string, tech, version string) {
	tech = normalizeProductName(tech)
	if tech == "" {
		return
	}
	if old, ok := products[tech]; !ok || (old == "" && version != "") {
		products[tech] = version
	}
}

func collectExtraStrings(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		*out = append(*out, x)
	case []any:
		for _, item := range x {
			collectExtraStrings(item, out)
		}
	case map[string]any:
		for _, item := range x {
			collectExtraStrings(item, out)
		}
	}
}

func SignalsFromAsset(a *Asset) CVEAssetSignals {
	sig := CVEAssetSignals{Products: map[string]string{}, AssetID: a.ID}
	if a.URL != "" {
		sig.AssetLabel = a.URL
	} else if a.Domain != "" {
		sig.AssetLabel = a.Domain
	} else {
		sig.AssetLabel = a.IP
	}
	if sig.AssetLabel == "" {
		sig.AssetLabel = fmt.Sprintf("asset#%d", a.ID)
	}
	sig.FaviconMMH3 = strings.TrimSpace(a.FaviconMMH3)
	sig.ServiceName = strings.ToLower(strings.TrimSpace(a.ServiceName))
	if a.Port != nil {
		sig.Port = *a.Port
	}
	sig.PageTitle = strings.TrimSpace(a.PageTitle)
	for _, t := range a.Technologies {
		p, v := splitProductVersion(t)
		addProductVersion(sig.Products, p, v)
		sig.Technologies = append(sig.Technologies, t)
	}
	// 结构化 extra 常见形态：{"product":"log4j","version":"2.14.1"}。
	if len(a.Extra) > 0 {
		firstString := func(keys ...string) string {
			for _, k := range keys {
				if v, ok := a.Extra[k].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
			return ""
		}
		product := firstString("product", "name", "server", "x-powered-by", "powered-by", "banner")
		version := firstString("version", "product_version", "server_version")
		if product != "" {
			p, v := splitProductVersion(product)
			if v == "" {
				v = version
			}
			addProductVersion(sig.Products, p, v)
		}
	}
	var extraStrings []string
	collectExtraStrings(a.Extra, &extraStrings)
	for _, s := range extraStrings {
		if m := cpeRe.FindStringSubmatch(s); m != nil {
			addProductVersion(sig.Products, m[2], m[3])
			continue
		}
		if p, v := splitProductVersion(s); p != "" && v != "" {
			addProductVersion(sig.Products, p, v)
		}
	}
	for _, op := range a.OpenPorts {
		product, _ := op["product"].(string)
		service, _ := op["service"].(string)
		version, _ := op["version"].(string)
		if product == "" {
			product = service
		}
		addProductVersion(sig.Products, product, version)
	}
	return sig
}

func SignalsFromFingerprint(fp PlaybookFingerprint, product, version string) CVEAssetSignals {
	fp = fp.Normalize()
	sig := CVEAssetSignals{
		Products: map[string]string{}, Technologies: append([]string{}, fp.Technologies...),
		FaviconMMH3: fp.FaviconMMH3, ServiceName: fp.ServiceName, Port: fp.Port, PageTitle: fp.PageTitle,
	}
	for _, t := range fp.Technologies {
		p, v := splitProductVersion(t)
		addProductVersion(sig.Products, p, v)
	}
	if strings.TrimSpace(product) != "" {
		addProductVersion(sig.Products, product, strings.TrimSpace(version))
	}
	return sig
}

func cveVersionStatus(c *CVE, sig CVEAssetSignals) (match, assetVersion, matchedProduct string) {
	match = "product_unknown"
	products := normalizeCVEProducts(c.AffectedProducts)
	if len(products) == 0 {
		products = normalizeCVEProducts(c.Fingerprint.Technologies)
	}
	for _, p := range products {
		for ap, av := range sig.Products {
			if productMatches(p, ap) {
				matchedProduct, assetVersion = ap, av
				break
			}
		}
		if matchedProduct != "" {
			break
		}
	}
	if c.VersionConstraint == "" {
		if matchedProduct != "" {
			return "unconstrained", assetVersion, matchedProduct
		}
		return "product_unknown", "", ""
	}
	vc, err := ParseVersionConstraint(c.VersionConstraint)
	if err != nil {
		return "version_unknown", assetVersion, matchedProduct
	}
	if matchedProduct == "" {
		return "product_unknown", "", ""
	}
	ok, comparable := vc.SatisfiedBy(assetVersion)
	if !comparable {
		return "version_unknown", assetVersion, matchedProduct
	}
	if !ok {
		return "mismatch", assetVersion, matchedProduct
	}
	return "compatible", assetVersion, matchedProduct
}

func scoreCVE(c *CVE, sig CVEAssetSignals) (int, []string, string, string, string) {
	score := 0
	strong := false
	var reasons []string
	versionMatch, assetVersion, matchedProduct := cveVersionStatus(c, sig)

	products := normalizeCVEProducts(c.AffectedProducts)
	if len(products) > 0 {
		for _, p := range products {
			for ap := range sig.Products {
				if !productMatches(p, ap) {
					continue
				}
				if matchedProduct == "" {
					matchedProduct = ap
				}
				if genericTechnologies[ap] {
					score += 10
				} else {
					score += 40
					strong = true
				}
				reasons = append(reasons, "产品 "+ap+" 命中")
				break
			}
			if matchedProduct != "" {
				break
			}
		}
	} else {
		// 没填产品时退回指纹技术栈；同样要避免泛化词单独命中。
		specific, generic := 0, 0
		for _, t := range c.Fingerprint.Technologies {
			p, _ := splitProductVersion(t)
			for ap := range sig.Products {
				if !productMatches(p, ap) {
					continue
				}
				if genericTechnologies[ap] {
					generic++
				} else {
					specific++
					strong = true
					reasons = append(reasons, "技术栈 "+ap+" 命中")
				}
				break
			}
		}
		if specific > 3 {
			specific = 3
		}
		if generic > 2 {
			generic = 2
		}
		score += specific*40 + generic*10
	}
	if c.Fingerprint.FaviconMMH3 != "" && c.Fingerprint.FaviconMMH3 == sig.FaviconMMH3 {
		score += 100
		strong = true
		reasons = append(reasons, "favicon 命中")
	}
	if c.Fingerprint.ServiceName != "" && c.Fingerprint.ServiceName == sig.ServiceName {
		if c.Fingerprint.Port > 0 && c.Fingerprint.Port == sig.Port {
			score += 15
			reasons = append(reasons, fmt.Sprintf("服务 %s:%d 命中", sig.ServiceName, sig.Port))
		} else {
			score += 5
			reasons = append(reasons, "服务 "+sig.ServiceName+" 命中")
		}
	}
	if c.Fingerprint.PageTitle != "" && c.Fingerprint.PageTitle == sig.PageTitle {
		score += 10
		reasons = append(reasons, "页面标题命中")
	}
	if versionMatch == "mismatch" {
		reasons = append(reasons, fmt.Sprintf("资产版本 %s 不满足约束 %s", assetVersion, c.VersionConstraint))
		return 0, reasons, versionMatch, assetVersion, matchedProduct
	}
	if versionMatch == "compatible" {
		score += 40
		strong = true
		reasons = append(reasons, fmt.Sprintf("版本 %s 满足 %s", assetVersion, c.VersionConstraint))
	} else if versionMatch == "version_unknown" && matchedProduct != "" {
		reasons = append(reasons, "版本未知（按产品/指纹列为候选）")
	}
	if score < 40 || !strong {
		return 0, reasons, versionMatch, assetVersion, matchedProduct
	}
	return score, reasons, versionMatch, assetVersion, matchedProduct
}

func rankCVEMatches(out []CVEMatch) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		ri, rj := cveVersionR[out[i].VersionMatch], cveVersionR[out[j].VersionMatch]
		if ri != rj {
			return ri > rj
		}
		if out[i].Uses != out[j].Uses {
			return out[i].Uses > out[j].Uses
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
}

func (s *AssetStore) loadActiveCVEs(limit int) ([]*CVE, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.Query(`SELECT `+cveListCols+` FROM cve_library WHERE status='active'
ORDER BY executions DESC, updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CVE{}
	for rows.Next() {
		c, err := scanCVE(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *AssetStore) MatchCVEs(sig CVEAssetSignals, limit int) ([]CVEMatch, error) {
	if limit <= 0 {
		limit = 3
	}
	cards, err := s.loadActiveCVEs(1000)
	if err != nil {
		return nil, err
	}
	out := make([]CVEMatch, 0, len(cards))
	for _, c := range cards {
		score, reasons, versionMatch, assetVersion, matchedProduct := scoreCVE(c, sig)
		if score <= 0 {
			continue
		}
		out = append(out, CVEMatch{
			CVE: *c, Score: score, MatchReasons: reasons, VersionMatch: versionMatch,
			AssetVersion: assetVersion, MatchedProduct: matchedProduct,
			AssetID: sig.AssetID, AssetLabel: sig.AssetLabel,
		})
	}
	rankCVEMatches(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *AssetStore) MatchCVEsForAsset(taskID, assetID int64, limit int) ([]CVEMatch, error) {
	assets, err := s.GetByIDsInScope(taskID, []int64{assetID})
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return nil, fmt.Errorf("资产 %d 不在本任务范围内或不存在", assetID)
	}
	return s.MatchCVEs(SignalsFromAsset(assets[0]), limit)
}

// MatchCVEForAsset 返回一条 CVE 对该资产的评估；score=0 表示不匹配（仍带原因，
// 供工具返回可读拒绝）。
func (s *AssetStore) MatchCVEForAsset(taskID int64, cveID string, assetID int64) (*CVEMatch, error) {
	c, err := s.GetCVEByCVEID(cveID)
	if err != nil || c == nil {
		return nil, err
	}
	assets, err := s.GetByIDsInScope(taskID, []int64{assetID})
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return nil, fmt.Errorf("资产 %d 不在本任务范围内或不存在", assetID)
	}
	sig := SignalsFromAsset(assets[0])
	score, reasons, versionMatch, assetVersion, matchedProduct := scoreCVE(c, sig)
	return &CVEMatch{
		CVE: *c, Score: score, MatchReasons: reasons, VersionMatch: versionMatch,
		AssetVersion: assetVersion, MatchedProduct: matchedProduct,
		AssetID: assets[0].ID, AssetLabel: sig.AssetLabel,
	}, nil
}

// MatchCVEsForTask 用本任务范围内 service 资产的指纹逐台匹配，同一 CVE 只保留最佳
// 资产。匹配用资产级信号（而不是聚合后的 TaskFingerprint），才能保留产品/版本。
func (s *AssetStore) MatchCVEsForTask(taskID int64, limit int) ([]CVEMatch, error) {
	if taskID <= 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 3
	}
	rows, err := s.db.Query(`WITH `+scopeTargetCTE+`
SELECT a.id, COALESCE(a.url,''), COALESCE(a.domain,''), COALESCE(a.ip,''), COALESCE(a.port,0),
       COALESCE(a.service_name,''), COALESCE(a.favicon_mmh3,''), COALESCE(a.page_title,''),
       COALESCE(array_to_json(a.technologies)::text,'[]'), COALESCE(a.extra::text,'{}')
FROM assets a WHERE a.type='service' AND a.id IN (SELECT id FROM target) LIMIT 2000`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var signals []CVEAssetSignals
	for rows.Next() {
		var id int64
		var url, domain, ip, service, favicon, title, techRaw, extraRaw string
		var port int
		if err := rows.Scan(&id, &url, &domain, &ip, &port, &service, &favicon, &title, &techRaw, &extraRaw); err != nil {
			return nil, err
		}
		var techs []string
		_ = json.Unmarshal([]byte(techRaw), &techs)
		var extra map[string]any
		_ = json.Unmarshal([]byte(extraRaw), &extra)
		a := &Asset{ID: id, Type: "service", URL: url, Domain: domain, IP: ip, ServiceName: service,
			FaviconMMH3: favicon, PageTitle: title, Technologies: techs, Extra: extra}
		if port > 0 {
			a.Port = &port
		}
		signals = append(signals, SignalsFromAsset(a))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cards, err := s.loadActiveCVEs(1000)
	if err != nil {
		return nil, err
	}
	best := map[int64]CVEMatch{}
	for _, c := range cards {
		for _, sig := range signals {
			score, reasons, versionMatch, assetVersion, matchedProduct := scoreCVE(c, sig)
			if score <= 0 {
				continue
			}
			m := CVEMatch{
				CVE: *c, Score: score, MatchReasons: reasons, VersionMatch: versionMatch,
				AssetVersion: assetVersion, MatchedProduct: matchedProduct,
				AssetID: sig.AssetID, AssetLabel: sig.AssetLabel,
			}
			if old, ok := best[c.ID]; !ok || m.Score > old.Score ||
				(m.Score == old.Score && cveVersionR[m.VersionMatch] > cveVersionR[old.VersionMatch]) {
				best[c.ID] = m
			}
		}
	}
	out := make([]CVEMatch, 0, len(best))
	for _, m := range best {
		out = append(out, m)
	}
	rankCVEMatches(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
