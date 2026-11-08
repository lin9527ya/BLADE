package db

import (
	"database/sql"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// TaskScope is one row of a task's test scope — the coverage denominator and the
// per-task authorization edge. Rows come either from insertAssets (source='auto',
// conservative, one per explicitly-inserted asset) or from the add_task_scope tool
// (source='agent', for company / domain / network / ICP / keyword scope).
type TaskScope struct {
	ID        int64  `json:"id"`
	TaskID    int64  `json:"task_id"`
	Kind      string `json:"kind"` // company|root_domain|subdomain|ip|cidr|icp|keyword
	CompanyID *int64 `json:"company_id,omitempty"`
	// CompanyName is resolved for kind=company so callers can label a scope row
	// without a second lookup. Empty when the row is not a company reference.
	CompanyName string `json:"company_name,omitempty"`
	Domain      string `json:"domain,omitempty"`
	Net         string `json:"net,omitempty"`
	Value       string `json:"value,omitempty"`
	Source      string `json:"source"`
	Reason      string `json:"reason,omitempty"`
}

// stripHostPort drops a trailing :port from a host:port / ip:port / [ipv6]:port
// value, returning the bare host. Bare hosts, bare IPs (v4 or v6, whose colons make
// them ambiguous), and anything not in host:port form are returned unchanged. Scope
// is host/net based, so the port from a target like "10.0.188.136:3000" or
// "api.example.com:8080" is simply discarded rather than baked into the key.
func stripHostPort(v string) string {
	v = strings.TrimSpace(v)
	if host, _, err := net.SplitHostPort(v); err == nil {
		return host
	}
	return v
}

// ipToHostCIDR turns a bare IP into its single-host CIDR (/32 or /128). "" if invalid.
func ipToHostCIDR(ip string) string {
	ip = strings.TrimSpace(ip)
	p := net.ParseIP(ip)
	if p == nil {
		return ""
	}
	if p.To4() != nil {
		return ip + "/32"
	}
	return ip + "/128"
}

// upsertTaskScope inserts one scope row idempotently (uq_task_scope). A duplicate is
// silently ignored. taskID<=0 or empty kind → no-op.
func (s *AssetStore) upsertTaskScopeResult(ts TaskScope) (bool, error) {
	if ts.TaskID <= 0 || ts.Kind == "" {
		return false, nil
	}
	var domainVal, netVal, companyVal, valueVal any
	if ts.Domain != "" {
		domainVal = ts.Domain
	}
	if ts.Net != "" {
		netVal = ts.Net
	}
	if ts.CompanyID != nil && *ts.CompanyID > 0 {
		companyVal = *ts.CompanyID
	}
	if ts.Value != "" {
		valueVal = ts.Value
	}
	src := ts.Source
	if src == "" {
		src = "auto"
	}
	query := `
INSERT INTO task_scope(task_id, kind, company_id, domain, net, value, source, reason)
VALUES ($1,$2,$3,$4,$5::cidr,$6,$7,NULLIF($8,''))
ON CONFLICT DO NOTHING`
	var (
		result sql.Result
		err    error
	)
	if s.tx != nil {
		result, err = s.tx.Exec(query, ts.TaskID, ts.Kind, companyVal, domainVal, netVal, valueVal, src, ts.Reason)
	} else {
		result, err = s.db.Exec(query, ts.TaskID, ts.Kind, companyVal, domainVal, netVal, valueVal, src, ts.Reason)
	}
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (s *AssetStore) upsertTaskScope(ts TaskScope) error {
	_, err := s.upsertTaskScopeResult(ts)
	return err
}

// AddAutoScope records the conservative task scope implied by ONE explicitly-inserted
// asset item (source='auto'). MUST be called only from insertAssets' top-level loop —
// never from a db-layer side effect (linkHostAssets), so派生资产不会盲目扩大范围。
// Rule: scope granularity follows the asset's own type. taskID<=0 → no-op.
func (s *AssetStore) AddAutoScope(taskID int64, assetType, domain, rawURL, ip string) error {
	if taskID <= 0 {
		return nil
	}
	switch assetType {
	case "root_domain":
		if d := DomainKey(domain); d != "" {
			return s.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "root_domain", Domain: d})
		}
	case "subdomain":
		if d := DomainKey(domain); d != "" {
			return s.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "subdomain", Domain: d})
		}
	case "service", "endpoint":
		host := domain
		if host == "" && rawURL != "" {
			host, _, _ = parseURL(normalizeURL(rawURL))
		}
		host = DomainKey(host)
		if host != "" && net.ParseIP(host) == nil {
			return s.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "subdomain", Domain: host})
		}
		// IP-literal host or no host → fall back to ip scope if we have one.
		if c := ipToHostCIDR(ip); c != "" {
			return s.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "ip", Net: c})
		}
	case "ip":
		if c := ipToHostCIDR(ip); c != "" {
			return s.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "ip", Net: c})
		}
	}
	return nil
}

// AddSiblingScope 把「与已入范围资产同根域」的兄弟子域纳入本任务范围（source='auto'，
// reason 写明依据）。这是用户要求的「确认属于目标的资产都可以加入测试范围」：
//
// 判定依据只有一个，且必须可审计——**同一个注册域（eTLD+1）**。它正好是"同一主体"在
// DNS 层面能自证的最强信号，也是 add_task_scope 的 root_domain 语义；不同根域一律不动
// （那要靠 company / icp 这类显式授权，绝不能因为"看起来像"就扩）。
//
// 返回真正新增的条数（已存在的不重复计）。
func (s *AssetStore) AddSiblingScope(taskID int64, domain, reason string) (int, error) {
	if taskID <= 0 {
		return 0, nil
	}
	host := DomainKey(domain)
	if host == "" || net.ParseIP(host) != nil {
		return 0, nil
	}
	root, isApex := RootDomain(host)
	if root == "" {
		return 0, nil
	}
	// 自身是裸根域时登记 root_domain（覆盖整域），否则登记精确子域。
	ts := TaskScope{TaskID: taskID, Source: "auto", Reason: reason}
	if isApex {
		ts.Kind, ts.Domain = "root_domain", root
	} else {
		ts.Kind, ts.Domain = "subdomain", host
	}
	added, err := s.upsertTaskScopeResult(ts)
	if err != nil {
		return 0, err
	}
	if added {
		return 1, nil
	}
	return 0, nil
}

// scopeRootDomains 是 ScopeRootDomains 的 store 侧实现：深度口径要用它，
// 而 ExplorationStore 没有 AssetStore 的依赖。SQL 与判定保持同一份语义。
func (s *ExplorationStore) scopeRootDomains(taskID int64) ([]string, error) {
	rows, err := s.db.Query(`
SELECT kind, COALESCE(domain,'') FROM task_scope
WHERE task_id=$1 AND kind IN ('root_domain','subdomain')`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []string{}
	for rows.Next() {
		var kind, domain string
		if err := rows.Scan(&kind, &domain); err != nil {
			return nil, err
		}
		if kind == "root_domain" {
			if domain != "" && !seen[domain] {
				seen[domain] = true
				out = append(out, domain)
			}
			continue
		}
		if root, _ := RootDomain(domain); root != "" && !seen[root] {
			seen[root] = true
			out = append(out, root)
		}
	}
	sort.Strings(out)
	return out, rows.Err()
}

// ScopeRootDomains 返回本任务范围里出现过的注册域（root_domain 行 + subdomain 行的 eTLD+1）。
// 用于「同根兄弟资产入范围」的判定与 planner 态势展示。
func (s *AssetStore) ScopeRootDomains(taskID int64) ([]string, error) {
	rows, err := s.ListTaskScope(taskID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, r := range rows {
		switch r.Kind {
		case "root_domain":
			if r.Domain != "" && !seen[r.Domain] {
				seen[r.Domain] = true
				out = append(out, r.Domain)
			}
		case "subdomain":
			if root, _ := RootDomain(r.Domain); root != "" && !seen[root] {
				seen[root] = true
				out = append(out, root)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// CountSiblingsInGraph 返回这些注册域在**资产图里已登记的主机数**。
//
// 按**主机**去重，不按资产行数：同一个主机下的 endpoint 有几十条，它们不说明"还有别的
// 站点"。判断"同根子域发现做过没有"要的是"知道几个不同的主机"，所以只数
// subdomain / ip / app（各自的自然键），endpoint 与 service 是挂在主机下的属性。
//
// 这个数不依赖本任务范围——资产图是全局共享的，别的任务登记过的兄弟主机同样算
// "已经知道它存在"。
func (s *AssetStore) CountSiblingsInGraph(roots []string) (int, error) {
	if len(roots) == 0 {
		return 0, nil
	}
	var n int
	err := s.db.QueryRow(`
SELECT count(DISTINCT COALESCE(NULLIF(domain,''), NULLIF(ip,''), NULLIF(app_name,''), NULLIF(url,'')))
FROM assets
WHERE type IN ('subdomain','app','ip') AND root_domain = ANY($1::text[])`, roots).Scan(&n)
	return n, err
}

// UntouchedSiblings 返回指定注册域下、**没有被本任务任何意图引用过**的具体资产。
//
// 「没被引用过」比「没被事实覆盖过」更严格，也更贴近这里的用途：一条意图只要派出去过
// （哪怕最后 blocked / no_output），就说明这个面已经试过了；只有连意图都没派过的资产，
// 才是「还没试过、值得在正面受阻时转过去」的候选。
//
// 排除 root_domain（聚合，不是可测点）与 service（host:port 属性，覆盖由宿主承担）。
// limit<=0 → 12。返回窗口 + 真实总数。
func (s *AssetStore) UntouchedSiblings(taskID int64, roots []string, limit int) ([]CoverageAsset, int, error) {
	if taskID <= 0 {
		return nil, 0, nil
	}
	if limit <= 0 {
		limit = 12
	}
	// 没有注册域范围时 roots 为空是合法的：任务可能只按 ip/cidr 授权（$2::text[] 空数组
	// 让 `= ANY` 恒为 false，下面的 ip/cidr 分支照常生效）。
	if roots == nil {
		roots = []string{}
	}
	// 范围内资产有两个来源，缺一不可：同注册域（$2），以及按 ip/cidr 纳入的范围行
	// （IP 资产的 root_domain 为空，只按 root_domain 匹配会把它们整片漏掉）。
	const filter = `
  AND a.type IN ('subdomain','app','endpoint','ip')
  AND NOT EXISTS (
    SELECT 1 FROM exploration_anchors ea
    JOIN exploration_nodes en ON en.id = ea.node_id
    JOIN tasks t ON t.exploration_id = en.exploration_id
    WHERE t.id = $1 AND en.kind = 'intent' AND ea.asset_id = a.id)`
	const inScope = `
  (a.root_domain = ANY($2::text[])
   OR EXISTS (SELECT 1 FROM task_scope ts
              WHERE ts.task_id = $1 AND ts.kind IN ('ip','cidr')
                AND ts.net >>= try_inet(a.ip)))`
	label := `COALESCE(NULLIF(a.url,''), NULLIF(a.domain,''), NULLIF(a.ip,''), NULLIF(a.app_name,''), a.type)`
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM assets a WHERE `+inScope+filter,
		taskID, roots).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT a.id, a.type, `+label+`
FROM assets a WHERE `+inScope+filter+`
ORDER BY a.id LIMIT $3`, taskID, roots, limit)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	out := []CoverageAsset{}
	for rows.Next() {
		var a CoverageAsset
		if err := rows.Scan(&a.ID, &a.Type, &a.Label); err != nil {
			return nil, total, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// SweepSiblingScope 把「已知属于同一注册域」的资产批量纳入任务范围。
//
// 与 AddSiblingScope 的分工：那个用于「刚发现一个主机 → 立刻把同根兄弟收进来」；
// 这个用于**回扫**——任务范围建立之后，资产图里又出现了同根域的主机（别的任务登记过、
// 或 enrichment 补出的），一次性补进来。两者判定同一件事：同 eTLD+1。
//
// 只认 root_domain 字段完全相等的资产（数据库已按 eTLD+1 归一），不做模糊匹配。
// 返回新增条数。
func (s *AssetStore) SweepSiblingScope(taskID int64, reason string) (int, error) {
	if taskID <= 0 {
		return 0, nil
	}
	roots, err := s.ScopeRootDomains(taskID)
	if err != nil || len(roots) == 0 {
		return 0, err
	}
	rows, err := s.db.Query(`
SELECT DISTINCT COALESCE(domain, '')
FROM assets
WHERE type = 'subdomain' AND root_domain = ANY($1) AND COALESCE(domain,'') <> ''`,
		roots)
	if err != nil {
		return 0, err
	}
	var hosts []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return 0, err
		}
		hosts = append(hosts, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	added := 0
	for _, h := range hosts {
		n, err := s.AddSiblingScope(taskID, h, reason)
		if err != nil {
			return added, err
		}
		added += n
	}
	return added, nil
}

// AddAgentScope parses a (kind,value) pair and records it as task scope.
// source is "agent" when called from an LLM tool, "manual" from the UI.
// company: value = company name or id (must already exist). root_domain/subdomain:
// value = a domain. ip/cidr: value = an IP or CIDR (bare IP → /32,/128).
func (s *AssetStore) AddAgentScope(taskID int64, kind, value, reason, source string) (TaskScope, error) {
	if source == "" {
		source = "agent"
	}
	ts := TaskScope{TaskID: taskID, Kind: kind, Source: source, Reason: reason}
	if taskID <= 0 {
		return ts, fmt.Errorf("需要 task_id")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return ts, fmt.Errorf("value 不能为空")
	}
	switch kind {
	case "company":
		if s.company == nil {
			return ts, fmt.Errorf("company store 未启用")
		}
		var comp *Company
		var err error
		if id, e := strconv.ParseInt(value, 10, 64); e == nil {
			comp, err = s.company.GetCompany(id)
		} else {
			comp, err = s.company.GetCompanyByName(value)
		}
		if err != nil {
			return ts, err
		}
		if comp == nil {
			return ts, fmt.Errorf("company 不存在: %s（先用 list_companies 确认，或建好企业）", value)
		}
		ts.CompanyID = &comp.ID
	case "root_domain":
		d := DomainKey(stripHostPort(value))
		root, _ := RootDomain(d)
		if root == "" {
			root = d
		}
		if root == "" {
			return ts, fmt.Errorf("无效根域: %s", value)
		}
		ts.Domain = root
	case "subdomain":
		d := DomainKey(stripHostPort(value))
		if d == "" {
			return ts, fmt.Errorf("无效子域: %s", value)
		}
		ts.Domain = d
	case "ip", "cidr":
		v := value
		if !strings.Contains(v, "/") {
			// 明确选了「网段」却不给掩码时，不能静默当成 /32——那正是
			// "选了网段也只能加 192.168.1.1/32" 的来源。让调用方写清 /24。
			if kind == "cidr" {
				return ts, fmt.Errorf("网段需要掩码长度，如 192.168.1.0/24（单个 IP 请选 ip）")
			}
			v = ipToHostCIDR(stripHostPort(v))
			ts.Kind = "ip"
		} else {
			ts.Kind = "cidr"
		}
		if v == "" {
			return ts, fmt.Errorf("无效 ip/cidr: %s", value)
		}
		_, ipnet, err := net.ParseCIDR(v)
		if err != nil {
			return ts, fmt.Errorf("无效 ip/cidr: %s", value)
		}
		// 必须存**规范化后的网络地址**：PostgreSQL 的 cidr 类型拒绝带主机位的值，
		// 而用户常把 C 段写成 192.168.1.1/24。原样保存会被 PG 拒绝（表现为
		// "网段加不进去，只能加 /32"）；ipnet.String() 会归一到 192.168.1.0/24。
		ts.Net = ipnet.String()
	case "icp", "keyword":
		// keyword 范围目前没有对应的资产匹配谓词（covTargetCTE 只认 company/root_domain/
		// subdomain/ip/cidr/icp）：接受它会让一条永远匹配不到资产的 scope 混进范围账，
		// 看起来"已纳入"，实际覆盖度里一条都不算。宁可当场拒绝并给出替代做法。
		if kind == "keyword" {
			return ts, fmt.Errorf("keyword 范围暂不支持覆盖度匹配：请改用 company（整个公司）/root_domain（整域）/subdomain 或 ip/cidr；关键词检索请用 list_assets 的 DSL")
		}
		parsed, err := ParseScopeInput(ScopeInput{Kind: kind, Value: value})
		if err != nil {
			return ts, err
		}
		ts.Value = parsed.Value
	default:
		return ts, fmt.Errorf("不支持的 kind: %s（company/root_domain/subdomain/ip/cidr/icp/keyword）", kind)
	}
	if err := s.upsertTaskScope(ts); err != nil {
		return ts, err
	}
	return ts, nil
}

// DeleteTaskScope removes a single scope row by id, scoped to the given task.
// Returns whether a row was actually deleted.
func (s *AssetStore) DeleteTaskScope(taskID, scopeID int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM task_scope WHERE id=$1 AND task_id=$2`, scopeID, taskID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListTaskScope returns all scope rows for a task.
func (s *AssetStore) ListTaskScope(taskID int64) ([]TaskScope, error) {
	rows, err := s.db.Query(`
SELECT ts.id, ts.kind, COALESCE(ts.company_id,0), COALESCE(c.name,''), COALESCE(ts.domain,''),
       COALESCE(ts.net::text,''), COALESCE(ts.value,''), ts.source, COALESCE(ts.reason,'')
FROM task_scope ts
LEFT JOIN companies c ON c.id=ts.company_id
WHERE ts.task_id=$1 ORDER BY ts.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskScope{}
	for rows.Next() {
		var t TaskScope
		var cid int64
		if err := rows.Scan(&t.ID, &t.Kind, &cid, &t.CompanyName, &t.Domain, &t.Net, &t.Value, &t.Source, &t.Reason); err != nil {
			return nil, err
		}
		t.TaskID = taskID
		if cid > 0 {
			t.CompanyID = &cid
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CoverageAsset is one in-scope asset (used for the untested backlog sample).
type CoverageAsset struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

// CoverageByType is per-asset-type coverage: total in scope vs tested.
type CoverageByType struct {
	Type   string `json:"type"`
	Total  int    `json:"total"`
	Tested int    `json:"tested"`
}

// Coverage is a task's rough asset test coverage — a reference figure for the agent,
// NOT a precise metric. Denominator = assets matching any active task_scope row;
// Tested = those anchored to at least one fact node in the exploration.
type Coverage struct {
	Enabled     bool             `json:"enabled"`     // 资产覆盖度功能是否开启；false 时其余字段为零值
	ScopeRows   int              `json:"scope_rows"`  // 0 → 范围未锚定
	Denominator int              `json:"denominator"` // 范围内资产数
	Tested      int              `json:"tested"`      // 已测(约)
	Pct         *float64         `json:"pct"`         // 覆盖度；分母 0 时 null
	ByType      []CoverageByType `json:"by_type"`     // 按资产类型的 总数/已测
}

// CoverageEnabled reports whether a task has the asset-coverage feature turned on
// (tasks.coverage_enabled). Missing row / error → true (fail open to the default),
// so unknown/legacy tasks keep the historical behavior. taskID<=0 → true.
func (s *AssetStore) CoverageEnabled(taskID int64) bool {
	if taskID <= 0 {
		return true
	}
	var enabled bool
	if err := s.db.QueryRow(`SELECT COALESCE(coverage_enabled,true) FROM tasks WHERE id=$1`, taskID).Scan(&enabled); err != nil {
		return true
	}
	return enabled
}

// task_scope→assets match predicate, reused by the count / by-type / untested queries. $1=taskID.
const covTargetCTE = `
target AS (
  SELECT DISTINCT a.id, a.type,
         COALESCE(a.url, a.domain, a.ip, a.app_name, a.root_domain, '') AS label
  FROM assets a
  JOIN task_scope ts ON ts.task_id = $1 AND (
       (ts.kind='company'     AND a.company_id = ts.company_id)
    OR (ts.kind='root_domain' AND a.root_domain = ts.domain)
    OR (ts.kind='subdomain'   AND a.domain = ts.domain)
    OR (ts.kind IN ('ip','cidr') AND ts.net >>= try_inet(a.ip))
    OR (ts.kind='icp' AND (
         lower(regexp_replace(COALESCE(a.icp,''), '[[:space:]]+', '', 'g')) = ts.value
         OR lower(regexp_replace(COALESCE(a.app_icp,''), '[[:space:]]+', '', 'g')) = ts.value
       ))
  )
),
tested AS (
  SELECT DISTINCT ea.asset_id
  FROM exploration_anchors ea
  JOIN exploration_nodes en ON en.id = ea.node_id
  WHERE en.exploration_id = $2 AND en.kind = 'fact'
)`

// CoverageMatrixCell is one (in-scope asset × risk area) cell of the coverage matrix.
type CoverageMatrixCell struct {
	RiskArea   string   `json:"risk_area"`
	Tested     int      `json:"tested"`       // 终态 done/negative
	Failed     int      `json:"failed"`       // blocked/exhausted —— 未测成
	InFlight   int      `json:"in_flight"`    // open/running/paused
	OutOfScope int      `json:"out_of_scope"` // out-of-scope —— 越界结论，不算已测也不算未测成
	Intents    []string `json:"intents"`      // "#<id> <state>"，供报告点名
}

// CoverageMatrixAsset is one in-scope asset row of the matrix (cells keyed by risk area).
type CoverageMatrixAsset struct {
	ID    int64                `json:"id"`
	Type  string               `json:"type"`
	Label string               `json:"label"`
	Facts int                  `json:"facts"` // 锚定于该资产的事实数（证据）
	Cells []CoverageMatrixCell `json:"cells"`
}

// CoverageMatrix derives the per-asset risk-area coverage matrix of THIS task:
// in-scope assets (task_scope × assets) × the risk areas recorded on their
// anchored intents, with outcome counts (已测/未测成/进行中/范围外) and fact
// evidence. Areas with no recorded intent are absent — callers compare against
// db.RiskAreas for 未触及.
func (s *ExplorationStore) CoverageMatrix(taskID int64) ([]CoverageMatrixAsset, error) {
	rows, err := s.db.Query(`WITH `+covTargetCTE+`
SELECT t.id, t.type, t.label FROM target t ORDER BY t.type, t.label`, taskID, s.expID)
	if err != nil {
		return nil, err
	}
	assets := []CoverageMatrixAsset{}
	index := map[int64]int{}
	for rows.Next() {
		var a CoverageMatrixAsset
		if err := rows.Scan(&a.ID, &a.Type, &a.Label); err != nil {
			rows.Close()
			return nil, err
		}
		a.Cells = []CoverageMatrixCell{}
		index[a.ID] = len(assets)
		assets = append(assets, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 意图引用一律用**全局 id**。此前这里渲染成「#序号（id 全局id）」两个数，
	// 模型分不清该用哪个去调工具，拿着序号找错节点（VPS 实测）。
	intentRef := func(id int64) string { return fmt.Sprintf("#%d", id) }

	type cellKey struct {
		asset int64
		area  string
	}
	cellIdx := map[cellKey]int{}
	prows, err := s.db.Query(`WITH `+covTargetCTE+`
SELECT ea.asset_id, COALESCE(NULLIF(en.payload->>'risk_area',''),'other') AS area, en.state, en.id,
       -- verify 意图只给它**实际验证过**的资产记覆盖：validated_asset_ids 缺失时
       -- 不再退化为“锚定的都算测过”——只挂了资产、没记录验证结论的意图，覆盖不可证，
       -- 该格保持未触及（债），而不是凭一个锚点就变绿。discovery 意图不受此限。
       COALESCE(en.state IN ('done','negative')
        AND en.payload->>'intent_kind' = 'verify'
        AND NOT COALESCE(
              jsonb_typeof(en.payload->'validated_asset_ids') = 'array'
              AND (en.payload->'validated_asset_ids') @> to_jsonb(ea.asset_id), false), false) AS validates_other_asset
FROM target t
JOIN exploration_anchors ea ON ea.asset_id = t.id
JOIN exploration_nodes en ON en.id = ea.node_id AND en.exploration_id = $2 AND en.kind = 'intent'
ORDER BY ea.asset_id, area, en.id`, taskID, s.expID)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var assetID, nodeID int64
		var area, state string
		var validatesOther bool
		if err := prows.Scan(&assetID, &area, &state, &nodeID, &validatesOther); err != nil {
			prows.Close()
			return nil, err
		}
		// verify 意图终态但没验证这个资产：这条意图对该资产不算已测。
		// 跳过整行（而不是记 failed）——「没验证」不等于「测失败」，该格应显示未触及。
		if validatesOther {
			continue
		}
		// 已删除（drop_intent/UI 取消）与已停止（kill/stop）的意图既不是"进行中"、也不是"测过"：
		// 落进 default 会让那一格永远显示"在跑"，既误导人也不反映真实覆盖。直接不参与矩阵。
		if state == "deleted" || state == "stopped" {
			continue
		}
		ai, ok := index[assetID]
		if !ok {
			continue
		}
		key := cellKey{assetID, area}
		ci, ok := cellIdx[key]
		if !ok {
			assets[ai].Cells = append(assets[ai].Cells, CoverageMatrixCell{RiskArea: area, Intents: []string{}})
			ci = len(assets[ai].Cells) - 1
			cellIdx[key] = ci
		}
		cell := &assets[ai].Cells[ci]
		switch state {
		case "done", "negative":
			cell.Tested++
		case "blocked", "exhausted", "no_output":
			// no_output = 跑完了却什么都没写回：按"未测成"计，绝不进 Tested。
			cell.Failed++
		case "out-of-scope":
			cell.OutOfScope++
		default:
			cell.InFlight++
		}
		cell.Intents = append(cell.Intents, intentRef(nodeID)+" "+state)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, err
	}

	frows, err := s.db.Query(`WITH `+covTargetCTE+`
SELECT ea.asset_id, count(DISTINCT en.id)
FROM target t
JOIN exploration_anchors ea ON ea.asset_id = t.id
JOIN exploration_nodes en ON en.id = ea.node_id AND en.exploration_id = $2 AND en.kind = 'fact'
GROUP BY ea.asset_id`, taskID, s.expID)
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	for frows.Next() {
		var assetID int64
		var n int
		if err := frows.Scan(&assetID, &n); err != nil {
			return nil, err
		}
		if ai, ok := index[assetID]; ok {
			assets[ai].Facts = n
		}
	}
	return assets, frows.Err()
}

// TaskCoverage computes rough per-type coverage for a task. taskID indexes
// task_scope + assets; expID indexes the fact anchors. Reference figure only.
func (s *AssetStore) TaskCoverage(taskID, expID int64) (*Coverage, error) {
	cov := &Coverage{ByType: []CoverageByType{}}
	_ = s.db.QueryRow(`SELECT count(*) FROM task_scope WHERE task_id=$1`, taskID).Scan(&cov.ScopeRows)
	rows, err := s.db.Query(`WITH `+covTargetCTE+`
SELECT t.type, count(*) AS total,
       count(*) FILTER (WHERE t.id IN (SELECT asset_id FROM tested)) AS tested
FROM target t GROUP BY t.type ORDER BY t.type`, taskID, expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bt CoverageByType
		if err := rows.Scan(&bt.Type, &bt.Total, &bt.Tested); err != nil {
			return nil, err
		}
		cov.ByType = append(cov.ByType, bt)
		cov.Denominator += bt.Total
		cov.Tested += bt.Tested
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if cov.Denominator > 0 {
		p := float64(cov.Tested) / float64(cov.Denominator)
		cov.Pct = &p
	}
	return cov, nil
}

// ---------------------------------------------------------------------------
// Coverage graph — a force-directed view of a task's in-scope assets.
// ---------------------------------------------------------------------------

// CoverageGraphNode is one node of the asset coverage graph. Node identity is a
// string key: an asset row is "a:<id>", a company is "c:<id>", and a root domain
// with no asset row of its own is the synthetic "r:<domain>". Out-of-scope
// connector nodes (roots pulled in only to link subdomains, and the companies
// above them) carry InScope=false and are rendered gray.
type CoverageGraphNode struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"` // company|root_domain|subdomain|ip|service|app|endpoint
	Label       string `json:"label"`
	Tested      bool   `json:"tested"`
	InScope     bool   `json:"in_scope"`
	AssetID     int64  `json:"asset_id,omitempty"` // 0 for company / synthetic root
	CompanyID   int64  `json:"company_id,omitempty"`
	Domain      string `json:"domain,omitempty"`
	RootDomain  string `json:"root_domain,omitempty"`
	IP          string `json:"ip,omitempty"`
	URL         string `json:"url,omitempty"`
	Port        int    `json:"port,omitempty"`
	ServiceType string `json:"service_type,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	PageTitle   string `json:"page_title,omitempty"`
	StatusCode  int    `json:"status_code,omitempty"`
	// Zone 是保守的网络层级（internet/boundary/internal），由地址证据计算；
	// 前端「网络拓扑」视图按它分区布局。
	Zone       string `json:"zone,omitempty"`
	ZoneReason string `json:"zone_reason,omitempty"`
}

// coverageZone 给覆盖图节点一个保守的网络层级：公网域名/公网 IP 是互联网区，
// 有公开主机证据的服务/应用/端点是边界区，私有/回环/链路本地地址是内网区。
// 只看 kind 不够——公网 endpoint 不能因为类型是 endpoint 就被放进内网。
func coverageZone(n *CoverageGraphNode) {
	if n == nil {
		return
	}
	host := strings.TrimSpace(n.IP)
	if host == "" {
		host = strings.TrimSpace(n.Domain)
	}
	if host == "" && n.URL != "" {
		parsed, _, _ := parseURL(normalizeURL(n.URL))
		host = parsed
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			n.Zone, n.ZoneReason = "internal", "私有、回环或链路本地地址"
			return
		}
	}
	switch n.Kind {
	case "company", "root_domain", "subdomain":
		n.Zone, n.ZoneReason = "internet", "公网域名或外部入口"
	case "service", "app", "endpoint":
		if host != "" {
			n.Zone, n.ZoneReason = "boundary", "存在可访问的域名、IP或URL暴露面"
		} else {
			n.Zone, n.ZoneReason = "internal", "未发现公网主机证据，按业务资产处理"
		}
	case "ip":
		n.Zone, n.ZoneReason = "internet", "未识别为私有地址的IP入口"
	default:
		n.Zone, n.ZoneReason = "internal", "按业务资产默认归类"
	}
}

// CoverageGraphEdge is a child→parent containment link (endpoint→service→
// subdomain/ip→root_domain→company, app→company).
type CoverageGraphEdge struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// CoverageGraphData is the whole graph for one task.
type CoverageGraphData struct {
	Nodes []CoverageGraphNode `json:"nodes"`
	Edges []CoverageGraphEdge `json:"edges"`
}

func assetKey(id int64) string   { return "a:" + strconv.FormatInt(id, 10) }
func companyKey(id int64) string { return "c:" + strconv.FormatInt(id, 10) }

// hostPortOf returns the (host, port) a service / endpoint node hangs off — the
// domain if set, otherwise the URL host, otherwise the IP.
func hostPortOf(n *CoverageGraphNode) (string, int) {
	host, port := n.Domain, n.Port
	if host == "" && n.URL != "" {
		h, p, _ := parseURL(normalizeURL(n.URL))
		host = h
		if port == 0 {
			port = p
		}
	}
	if host == "" {
		host = n.IP
	}
	return host, port
}

// BuildCoverageGraph assembles the full coverage graph for a task and its direct
// read-only sources: every in-scope asset plus connector root domains/companies,
// with current-or-source fact anchors reflected in Tested. The legacy expID
// argument is retained for API compatibility; the task registry is authoritative.
func (s *AssetStore) BuildCoverageGraph(taskID, _ int64) (*CoverageGraphData, error) {
	g := &CoverageGraphData{Nodes: []CoverageGraphNode{}, Edges: []CoverageGraphEdge{}}
	if taskID <= 0 {
		return g, nil
	}
	rows, err := s.db.Query(`WITH `+contextCoverageCTE+`
SELECT a.id, a.type, COALESCE(a.company_id,0),
       COALESCE(a.domain,''), COALESCE(a.root_domain,''), COALESCE(a.ip,''),
       COALESCE(a.url,''), COALESCE(a.port,0), COALESCE(a.service_type,''),
       COALESCE(a.app_name,''), COALESCE(a.page_title,''), COALESCE(a.status_code,0),
       (a.id IN (SELECT asset_id FROM tested)) AS tested
FROM assets a JOIN target t ON t.id = a.id
ORDER BY a.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byKey := map[string]*CoverageGraphNode{}
	rootByDomain := map[string]string{} // root domain → node key
	subByDomain := map[string]string{}  // subdomain    → node key
	ipByAddr := map[string]string{}     // ip literal   → node key
	svcByHostPort := map[string]string{}
	svcByHost := map[string]string{}
	companyIDs := map[int64]bool{} // referenced company ids (need a node)

	add := func(n CoverageGraphNode) *CoverageGraphNode {
		if n.Zone == "" {
			coverageZone(&n)
		}
		if _, ok := byKey[n.Key]; ok {
			return byKey[n.Key]
		}
		g.Nodes = append(g.Nodes, n)
		p := &g.Nodes[len(g.Nodes)-1]
		byKey[n.Key] = p
		return p
	}

	for rows.Next() {
		var n CoverageGraphNode
		var companyID int64
		if err := rows.Scan(&n.AssetID, &n.Kind, &companyID,
			&n.Domain, &n.RootDomain, &n.IP, &n.URL, &n.Port, &n.ServiceType,
			&n.AppName, &n.PageTitle, &n.StatusCode, &n.Tested); err != nil {
			return nil, err
		}
		n.Key = assetKey(n.AssetID)
		n.CompanyID = companyID
		n.InScope = true
		n.Label = coverageNodeLabel(&n)
		p := add(n)
		switch n.Kind {
		case "root_domain":
			if n.Domain != "" {
				rootByDomain[n.Domain] = p.Key
			}
		case "subdomain":
			if n.Domain != "" {
				subByDomain[n.Domain] = p.Key
			}
		case "ip":
			if n.IP != "" {
				ipByAddr[n.IP] = p.Key
			}
		case "service":
			host, port := hostPortOf(p)
			if host != "" {
				svcByHost[host] = p.Key
				svcByHostPort[host+"|"+strconv.Itoa(port)] = p.Key
			}
		}
		if companyID > 0 {
			companyIDs[companyID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Connector root domains: any subdomain whose root domain is not itself an
	// in-scope node. Pull the real asset row if one exists (so the drawer shows
	// real detail), else synthesize a bare "r:<domain>" placeholder. Both gray.
	missingRoots := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Kind == "subdomain" && n.RootDomain != "" {
			if _, ok := rootByDomain[n.RootDomain]; !ok {
				missingRoots[n.RootDomain] = true
			}
		}
	}
	for root := range missingRoots {
		var id, companyID int64
		err := s.db.QueryRow(`SELECT id, COALESCE(company_id,0) FROM assets
WHERE type='root_domain' AND domain=$1 LIMIT 1`, root).Scan(&id, &companyID)
		var node CoverageGraphNode
		if err == nil && id > 0 {
			node = CoverageGraphNode{Key: assetKey(id), Kind: "root_domain", AssetID: id,
				CompanyID: companyID, Domain: root, Label: root}
			if companyID > 0 {
				companyIDs[companyID] = true
			}
		} else {
			node = CoverageGraphNode{Key: "r:" + root, Kind: "root_domain", Domain: root, Label: root}
		}
		add(node)
		rootByDomain[root] = node.Key
	}

	// Company nodes for every referenced company id — always gray context.
	for id := range companyIDs {
		key := companyKey(id)
		if _, ok := byKey[key]; ok {
			continue
		}
		var name string
		if err := s.db.QueryRow(`SELECT name FROM companies WHERE id=$1`, id).Scan(&name); err != nil {
			continue
		}
		add(CoverageGraphNode{Key: key, Kind: "company", CompanyID: id,
			Label: name, AssetID: 0})
	}

	// Derived containment edges (only when the parent node exists).
	link := func(childKey, parentKey string) {
		if parentKey == "" || parentKey == childKey {
			return
		}
		if _, ok := byKey[parentKey]; !ok {
			return
		}
		g.Edges = append(g.Edges, CoverageGraphEdge{Src: childKey, Dst: parentKey})
	}
	firstOf := func(keys ...string) string {
		for _, k := range keys {
			if k != "" {
				if _, ok := byKey[k]; ok {
					return k
				}
			}
		}
		return ""
	}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		switch n.Kind {
		case "subdomain":
			link(n.Key, rootByDomain[n.RootDomain])
		case "root_domain", "app", "ip":
			if n.CompanyID > 0 {
				link(n.Key, companyKey(n.CompanyID))
			}
		case "service":
			link(n.Key, firstOf(subByDomain[n.Domain], ipByAddr[n.IP], rootByDomain[n.RootDomain]))
		case "endpoint":
			host, port := hostPortOf(n)
			parent := firstOf(
				svcByHostPort[host+"|"+strconv.Itoa(port)], svcByHost[host],
				subByDomain[host], subByDomain[n.Domain], ipByAddr[host], ipByAddr[n.IP],
				rootByDomain[n.RootDomain])
			link(n.Key, parent)
		}
	}
	return g, nil
}

// coverageNodeLabel picks the human label for a coverage-graph node.
func coverageNodeLabel(n *CoverageGraphNode) string {
	switch n.Kind {
	case "endpoint", "service":
		if n.URL != "" {
			return n.URL
		}
	case "app":
		if n.AppName != "" {
			return n.AppName
		}
	}
	for _, v := range []string{n.URL, n.Domain, n.IP, n.AppName, n.RootDomain} {
		if v != "" {
			return v
		}
	}
	return n.Key
}

// ListUntestedAssets returns a task's in-scope, not-yet-tested assets, optionally
// filtered by asset type, paginated. Returns the page + the total count. limit<=0 → 10.
func (s *AssetStore) ListUntestedAssets(taskID, expID int64, typ string, limit, offset int) ([]CoverageAsset, int, error) {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	typeFilter := ""
	args := []any{taskID, expID}
	if typ != "" {
		typeFilter = " AND t.type = $3"
		args = append(args, typ)
	}
	var total int
	_ = s.db.QueryRow(`WITH `+covTargetCTE+`
SELECT count(*) FROM target t WHERE t.id NOT IN (SELECT asset_id FROM tested)`+typeFilter, args...).Scan(&total)
	pageArgs := append(append([]any{}, args...), limit, offset)
	limPos := strconv.Itoa(len(args) + 1)
	offPos := strconv.Itoa(len(args) + 2)
	rows, err := s.db.Query(`WITH `+covTargetCTE+`
SELECT t.id, t.type, t.label FROM target t
WHERE t.id NOT IN (SELECT asset_id FROM tested)`+typeFilter+`
ORDER BY t.id LIMIT $`+limPos+` OFFSET $`+offPos, pageArgs...)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	out := []CoverageAsset{}
	for rows.Next() {
		var a CoverageAsset
		if err := rows.Scan(&a.ID, &a.Type, &a.Label); err != nil {
			return nil, total, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// --- 广度账本（asset-level breadth）---

// BreadthAssetGap is one concrete in-scope asset that still needs breadth work.
// State is derived from the coverage matrix: untouched (no intent at all),
// failed (blocked/exhausted/no_output only), in_flight (open/running/paused),
// tested (at least one done/negative), or out_of_scope (explicitly ruled out).
type BreadthAssetGap struct {
	AssetID int64  `json:"asset_id"`
	Type    string `json:"type"`
	Label   string `json:"label"`
	State   string `json:"state"`
	Facts   int    `json:"facts"`
	Partial bool   `json:"partial,omitempty"` // tested, but some risk areas untouched
	// UntouchedCount 是未碰风险类的真实数量；UntouchedAreas 只展示最贴合的两类，
	// 避免态势被「8 资产 × 13 类」的矩阵淹没（完整矩阵走 coverage_matrix）。
	UntouchedCount int      `json:"untouched_count,omitempty"`
	UntouchedAreas []string `json:"untouched_risk_areas,omitempty"`
	FailedAreas    []string `json:"failed_risk_areas,omitempty"`
	InFlightAreas  []string `json:"in_flight_risk_areas,omitempty"`
}

// BreadthSummary is the concrete-asset terminal coverage ledger injected into the
// planner's situational view. It deliberately excludes root_domain (an aggregate,
// not a testable point). Debt = assets with no terminal intent and no in-flight
// intent; those are the assets a breadth intent must pick up.
type BreadthSummary struct {
	AssetsTotal         int               `json:"assets_total"`
	AssetsTested        int               `json:"assets_tested"`
	AssetsUntouched     int               `json:"assets_untouched"`
	AssetsFailed        int               `json:"assets_failed"`
	AssetsInFlight      int               `json:"assets_in_flight"`
	AssetsOutOfScope    int               `json:"assets_out_of_scope"`
	AssetsTerminal      int               `json:"assets_terminal"`
	AssetsPartial       int               `json:"assets_partial"`
	RootDomainsExcluded int               `json:"root_domains_excluded"`
	ServicesExcluded    int               `json:"services_excluded"` // service 是属性(host:port)，不单独计债
	Debt                int               `json:"debt"`
	ScopeRows           int               `json:"scope_rows"`
	NeedAssetDiscovery  bool              `json:"need_asset_discovery"`
	Pct                 *float64          `json:"pct"`
	RiskAreas           []string          `json:"risk_areas"`
	TopGaps             []BreadthAssetGap `json:"top_gaps"`
	DebtIDs             map[int64]bool    `json:"-"`
}

const breadthTopGapLimit = 8

var breadthStateRank = map[string]int{
	"untouched": 0, "failed": 1, "in_flight": 2, "tested": 3, "out_of_scope": 4,
}

var breadthTypeRank = map[string]int{
	"endpoint": 0, "service": 1, "app": 2, "subdomain": 3, "ip": 4,
}

// BreadthSummary builds the ledger for this task from the existing coverage
// matrix. It does not change the matrix's Tested/Failed/InFlight semantics; it
// only groups cells per concrete asset and separates terminal coverage from debt.
func (s *ExplorationStore) BreadthSummary(taskID int64) (*BreadthSummary, error) {
	cov, err := s.CoverageMatrix(taskID)
	if err != nil {
		return nil, err
	}
	var scopeRows int
	_ = s.db.QueryRow(`SELECT count(*) FROM task_scope WHERE task_id=$1`, taskID).Scan(&scopeRows)
	return BuildBreadthSummary(cov, scopeRows), nil
}

// serviceHostLabel 从 service 的 label 里取 host（label 可能是 URL、host:port 或裸 IP）。
func serviceHostLabel(label string) string {
	s := strings.TrimSpace(label)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	return strings.ToLower(strings.TrimSuffix(s, "."))
}

// AssetDepthGap 是一个「已测过、但还有大量风险类没碰」的资产。
//
// 为什么需要它：breadth 的 debt 只数「一个终态意图都没有」的资产，一旦某个资产被测过
// 任意一类（比如只做了 auth），它就从 debt 里消失、pct 变成 100%。于是"广度达标"与
// "测得很浅"可以同时成立——任务 22 实测：22 条意图集中在 3 个资产上，其余资产各测
// 1 类就再没人碰，planner 随即宣告穷尽。
//
// 这里按「每个资产已终结的风险类数」补一层深度口径：少于 minAreas 的资产列出来，
// 让「穷尽」这件事必须面对它们。
type AssetDepthGap struct {
	AssetID   int64    `json:"asset_id"`
	Type      string   `json:"type"`
	Label     string   `json:"label"`
	DoneAreas []string `json:"done_risk_areas"`
	// Missing 是**尚未终结**的风险类（没派过、或只有 blocked/no_output）。
	Missing []string `json:"missing_risk_areas"`
}

// DepthGaps 返回本任务范围内「已测但深度不足」的资产：终结风险类数 < minAreas。
// 只统计具体资产（subdomain/app/endpoint/ip；root_domain 与 service 不计，理由同
// breadth）。minAreas<=0 时用默认值 DefaultDepthAreas。
func (s *ExplorationStore) DepthGaps(taskID int64, minAreas, limit int) ([]AssetDepthGap, int, error) {
	return s.depthGaps(taskID, minAreas, limit, nil)
}

// DepthGapsWithWaivers 对审计应用精确豁免，豁免不冒充测试结果（DoneAreas 仍只含实测/越界结论）。
func (s *ExplorationStore) DepthGapsWithWaivers(taskID int64, minAreas, limit int, waivers []CoverageWaiver) ([]AssetDepthGap, int, error) {
	return s.depthGaps(taskID, minAreas, limit, waivers)
}

func (s *ExplorationStore) depthGaps(taskID int64, minAreas, limit int, waivers []CoverageWaiver) ([]AssetDepthGap, int, error) {
	if minAreas <= 0 {
		minAreas = DefaultDepthAreas
	}
	if limit <= 0 {
		limit = 8
	}
	// 在范围口径必须与 UntouchedSiblings 一致：同注册域 + 任务 ip/cidr 范围行。
	// 用 covTargetCTE 会漏掉「只登记了精确子域、同根兄弟靠自动纳入」的那一类——
	// 而恰恰是这些兄弟最容易"碰一下就没人管"，正是本查询要抓的对象。
	roots, err := s.scopeRootDomains(taskID)
	if err != nil {
		return nil, 0, err
	}
	if roots == nil {
		roots = []string{}
	}
	rows, err := s.db.Query(`
SELECT a.id, a.type,
       COALESCE(NULLIF(a.url,''), NULLIF(a.domain,''), NULLIF(a.ip,''), NULLIF(a.app_name,''), a.type) AS label,
       -- out-of-scope 也算「已处理」：那是显式判定（越界/不适用），不是没碰过。
       -- 否则被整体判出去的资产会永远显示为"深度不足"，把收官永久堵死。
       COALESCE(array_agg(DISTINCT en.payload->>'risk_area')
         FILTER (WHERE en.kind='intent' AND en.state IN ('done','negative','out-of-scope')), '{}')::text[] AS done_areas
FROM assets a
LEFT JOIN exploration_anchors ea ON ea.asset_id = a.id
LEFT JOIN exploration_nodes en ON en.id = ea.node_id AND en.exploration_id = $2
WHERE a.type IN ('subdomain','app','endpoint','ip')
  AND (a.root_domain = ANY($1::text[])
       OR EXISTS (SELECT 1 FROM task_scope ts
                  WHERE ts.task_id = $3 AND ts.kind IN ('ip','cidr')
                    AND ts.net >>= try_inet(a.ip)))
GROUP BY a.id, a.type, a.url, a.domain, a.ip, a.app_name`, roots, s.expID, taskID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AssetDepthGap
	for rows.Next() {
		var g AssetDepthGap
		var doneRaw string
		if err := rows.Scan(&g.AssetID, &g.Type, &g.Label, &doneRaw); err != nil {
			return nil, 0, err
		}
		done := parsePgTextArray(doneRaw)
		seen := map[string]bool{}
		for _, a := range done {
			a = NormalizeRiskArea(a)
			if a == "" {
				continue
			}
			seen[a] = true
			g.DoneAreas = append(g.DoneAreas, a)
		}
		handled := len(seen)
		for _, area := range RiskAreas {
			if seen[area] {
				continue
			}
			if _, ok := matchCoverageWaiver(waivers, g.AssetID, area); ok {
				handled++
				continue
			}
			g.Missing = append(g.Missing, area)
		}
		if handled >= minAreas {
			continue
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// 缺得最多的排前面：它们最可能是"看起来测过、其实很浅"的那一批。
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].DoneAreas) != len(out[j].DoneAreas) {
			return len(out[i].DoneAreas) < len(out[j].DoneAreas)
		}
		return out[i].AssetID < out[j].AssetID
	})
	total := len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

// parsePgTextArray 解析 PostgreSQL 的 text[] 字面量（{a,b,"c d"}）。
// 驱动在这里返回的是字符串而不是 []string，所以自己拆；元素只会是风险类名
// （字母/数字/下划线），不需要处理转义与 NULL。
func parsePgTextArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '{' {
		return nil
	}
	body := strings.TrimSuffix(raw[1:], "}")
	if strings.TrimSpace(body) == "" {
		return nil
	}
	out := make([]string, 0, strings.Count(body, ",")+1)
	for _, part := range strings.Split(body, ",") {
		part = strings.Trim(strings.TrimSpace(part), `"`)
		if part != "" && part != "NULL" {
			out = append(out, part)
		}
	}
	return out
}

// DefaultDepthAreas 是「一个资产算测过」的最低风险类数。
//
// 取 3 的依据：任务 19 的深度资产（www.dampflager.de 主站）终结了 10 类，而同任务里
// 的兄弟子域各只终结 1 类就再没人碰；任务 22 的两个资产分别终结 3 类与 0 类。3 类
// 是"真的按面测过"与"只打了个照面"之间的分界，又不至于要求每个子域都跑满 14 类。
const DefaultDepthAreas = 3

// BuildBreadthSummary is the pure aggregation used by BreadthSummary and tests.
func BuildBreadthSummary(cov []CoverageMatrixAsset, scopeRows int) *BreadthSummary {
	out := &BreadthSummary{
		ScopeRows: scopeRows,
		RiskAreas: append([]string(nil), RiskAreas...),
		TopGaps:   []BreadthAssetGap{},
		DebtIDs:   map[int64]bool{},
	}
	// service 是「host:port」属性，不是独立的测试面：同一个站点会同时出现 service
	// 和它的多个 endpoint，分别计债会让分母翻倍、覆盖率被稀释。把它合并到同 host 的
	// 具体资产（subdomain/ip）上；找不到宿主时才退回“service 自己算一个资产”——
	// 否则只有 service 的任务会因为分母为空而永远无法推进覆盖度。
	byLabel := map[string]int{}
	for i, a := range cov {
		if a.Type == "subdomain" || a.Type == "ip" {
			byLabel[strings.ToLower(strings.TrimSuffix(a.Label, "."))] = i
		}
	}
	mergeCells := map[int][]CoverageMatrixCell{}
	mergeFacts := map[int]int{}
	skipAsset := map[int64]bool{}
	for _, a := range cov {
		if a.Type != "service" {
			continue
		}
		host := serviceHostLabel(a.Label)
		if j, ok := byLabel[host]; ok && host != "" {
			mergeCells[j] = append(mergeCells[j], a.Cells...)
			mergeFacts[j] += a.Facts
			skipAsset[a.ID] = true
			out.ServicesExcluded++
		}
	}
	for i, a := range cov {
		switch a.Type {
		case "root_domain":
			out.RootDomainsExcluded++
			continue
		}
		if skipAsset[a.ID] {
			continue
		}
		a.Facts += mergeFacts[i]
		if extra := mergeCells[i]; len(extra) > 0 {
			a.Cells = append(append([]CoverageMatrixCell{}, a.Cells...), extra...)
		}
		out.AssetsTotal++
		seen := make(map[string]bool, len(a.Cells))
		var testedArea, failedArea, inFlightArea bool
		var outOfScopeArea bool
		gap := BreadthAssetGap{AssetID: a.ID, Type: a.Type, Label: a.Label, Facts: a.Facts}
		for _, c := range a.Cells {
			seen[c.RiskArea] = true
			if c.Tested > 0 {
				testedArea = true
			}
			if c.OutOfScope > 0 {
				outOfScopeArea = true
			}
			if c.InFlight > 0 {
				inFlightArea = true
				gap.InFlightAreas = append(gap.InFlightAreas, c.RiskArea)
			}
			if c.Failed > 0 && c.Tested == 0 {
				failedArea = true
				gap.FailedAreas = append(gap.FailedAreas, c.RiskArea)
			}
		}
		for _, area := range RiskAreas {
			if !seen[area] {
				gap.UntouchedAreas = append(gap.UntouchedAreas, area)
			}
		}
		gap.UntouchedCount = len(gap.UntouchedAreas)
		switch {
		case testedArea:
			gap.State = "tested"
			out.AssetsTested++
			out.AssetsTerminal++
			if len(gap.UntouchedAreas) > 0 {
				gap.Partial = true
				out.AssetsPartial++
			}
		case outOfScopeArea:
			gap.State = "out_of_scope"
			out.AssetsOutOfScope++
			out.AssetsTerminal++
		case inFlightArea:
			gap.State = "in_flight"
			out.AssetsInFlight++
		case failedArea:
			gap.State = "failed"
			out.AssetsFailed++
			out.Debt++
			out.DebtIDs[a.ID] = true
		default:
			gap.State = "untouched"
			out.AssetsUntouched++
			out.Debt++
			out.DebtIDs[a.ID] = true
		}
		if gap.State != "out_of_scope" && (gap.State != "tested" || gap.Partial) {
			out.TopGaps = append(out.TopGaps, gap)
		}
	}
	denom := out.AssetsTotal - out.AssetsOutOfScope
	if denom > 0 {
		p := float64(out.AssetsTested) / float64(denom)
		out.Pct = &p
	}
	sort.SliceStable(out.TopGaps, func(i, j int) bool {
		gi, gj := out.TopGaps[i], out.TopGaps[j]
		if ri, rj := breadthStateRank[gi.State], breadthStateRank[gj.State]; ri != rj {
			return ri < rj
		}
		if ti, tj := breadthTypeRank[gi.Type], breadthTypeRank[gj.Type]; ti != tj {
			return ti < tj
		}
		mi := len(gi.UntouchedAreas) + len(gi.FailedAreas)
		mj := len(gj.UntouchedAreas) + len(gj.FailedAreas)
		if mi != mj {
			return mi > mj
		}
		return gi.AssetID < gj.AssetID
	})
	// 排序用完整列表（缺口多的优先），输出前再裁剪：每资产只留最贴合的两类。
	for i := range out.TopGaps {
		if len(out.TopGaps[i].UntouchedAreas) > 2 {
			out.TopGaps[i].UntouchedAreas = out.TopGaps[i].UntouchedAreas[:2]
		}
		if len(out.TopGaps[i].FailedAreas) > 2 {
			out.TopGaps[i].FailedAreas = out.TopGaps[i].FailedAreas[:2]
		}
		if len(out.TopGaps[i].InFlightAreas) > 2 {
			out.TopGaps[i].InFlightAreas = out.TopGaps[i].InFlightAreas[:2]
		}
	}
	if len(out.TopGaps) > breadthTopGapLimit {
		out.TopGaps = out.TopGaps[:breadthTopGapLimit]
	}
	out.NeedAssetDiscovery = scopeRows > 0 && out.AssetsTotal == 0
	return out
}
