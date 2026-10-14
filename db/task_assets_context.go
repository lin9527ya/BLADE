package db

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// directTaskContextCTE is the current task plus exactly its explicitly related
// source tasks. It is deliberately non-recursive.
const directTaskContextCTE = `
context_tasks AS (
  SELECT t.id AS task_id, t.exploration_id
  FROM tasks t
  WHERE t.id=$1 AND t.deleted_at IS NULL
  UNION ALL
  SELECT source.id, source.exploration_id
  FROM task_relations relation
  JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
  WHERE relation.task_id=$1
)`

// scopeAssetIDsCTE 是「本任务（含直接源任务）声明范围内」的资产 id 集合——**按 kind 拆成
// UNION**，而不是原来那一个大 OR 连接。用法：拼在 directTaskContextCTE 之后，再 JOIN
// scope_asset_ids。
//
// 为什么拆：那条 5 路 OR（company / root_domain / subdomain / ip|cidr / icp）会让规划器选
// 「嵌套循环 + 对 assets 逐条 scope 行全表扫描」。实测（5 万资产 × 20 条 scope）：单次 296ms、
// 扫描 100 万行、被 join filter 滤掉 95 万行——而这个查询在**每次 graph_overview**（规划器
// 每轮 + 每个 worker run）都会跑。拆开后前三支各走 idx_av2_company / idx_av2_root_domain /
// idx_av2_domain，不再逐 scope 行重扫 assets。
//
// 语义与原来逐字对齐：任一支命中等价于原 OR 中对应那一路命中，UNION 去重与 SELECT DISTINCT
// 同义。后两支（ip|cidr 的 try_inet 表达式、icp 的 regexp 归一化）本就无法走索引，但拆开后
// 它们**只扫一遍**而不是每条 scope 行扫一遍。
//
// 等价性由 TestHostsByTaskWithSourcesScopeKinds 钉住（六种 scope 各命中一个资产）。
const scopeAssetIDsCTE = `,
scope_asset_ids AS (
  SELECT a.id FROM assets a
  JOIN task_scope ts ON ts.kind='company' AND a.company_id=ts.company_id
  JOIN context_tasks ctx ON ctx.task_id=ts.task_id
  UNION
  SELECT a.id FROM assets a
  JOIN task_scope ts ON ts.kind='root_domain' AND a.root_domain=ts.domain
  JOIN context_tasks ctx ON ctx.task_id=ts.task_id
  UNION
  SELECT a.id FROM assets a
  JOIN task_scope ts ON ts.kind='subdomain' AND a.domain=ts.domain
  JOIN context_tasks ctx ON ctx.task_id=ts.task_id
  UNION
  SELECT a.id FROM assets a
  JOIN task_scope ts ON ts.kind IN ('ip','cidr') AND ts.net >>= try_inet(a.ip)
  JOIN context_tasks ctx ON ctx.task_id=ts.task_id
  UNION
  SELECT a.id FROM assets a
  JOIN task_scope ts ON ts.kind='icp' AND (
       lower(regexp_replace(COALESCE(a.icp,''), '[[:space:]]+', '', 'g')) = ts.value
       OR lower(regexp_replace(COALESCE(a.app_icp,''), '[[:space:]]+', '', 'g')) = ts.value
     )
  JOIN context_tasks ctx ON ctx.task_id=ts.task_id
)`

const contextCoverageCTE = directTaskContextCTE + scopeAssetIDsCTE + `,
target AS (
  SELECT DISTINCT a.id, a.type,
         COALESCE(a.url, a.domain, a.ip, a.app_name, a.root_domain, '') AS label
  FROM assets a
  JOIN scope_asset_ids sc ON sc.id = a.id
	UNION
	SELECT a.id, a.type,
	       COALESCE(a.url, a.domain, a.ip, a.app_name, a.root_domain, '') AS label
	FROM assets a
	JOIN exploration_anchors ea ON ea.asset_id=a.id
	JOIN exploration_nodes en ON en.id=ea.node_id
	JOIN context_tasks ctx ON ctx.exploration_id=en.exploration_id
),
tested AS (
  SELECT DISTINCT ea.asset_id
  FROM exploration_anchors ea
  JOIN exploration_nodes en ON en.id=ea.node_id AND en.kind='fact'
  JOIN context_tasks ctx ON ctx.exploration_id=en.exploration_id
)`

// scopeTargetCTE selects every asset that BELONGS to the current task's (and its
// direct source tasks') declared scope — membership, not literal value: a
// root_domain scope pulls in every subdomain / service / endpoint whose own
// root_domain column equals it; an ip/cidr scope pulls in assets whose ip OR
// IP-literal host falls inside the net. $1 is the task id. Unlike contextCoverageCTE
// it carries neither the fact-anchor union nor the tested set — it is pure "in
// declared scope", independent of what has already been touched. Used by the
// agent's list_assets so a query returns the task's relevant assets, not the
// whole shared库.
const scopeTargetCTE = `
context_tasks AS (
  SELECT t.id AS task_id
  FROM tasks t
  WHERE t.id=$1 AND t.deleted_at IS NULL
  UNION ALL
  SELECT source.id
  FROM task_relations relation
  JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
  WHERE relation.task_id=$1
),
target AS (
  SELECT DISTINCT a.id
  FROM assets a
  JOIN task_scope ts ON (
       (ts.kind='company'     AND a.company_id = ts.company_id)
    OR (ts.kind='root_domain' AND a.root_domain = ts.domain)
    OR (ts.kind='subdomain'   AND a.domain = ts.domain)
    OR (ts.kind IN ('ip','cidr') AND (ts.net >>= try_inet(a.ip) OR ts.net >>= try_inet(a.domain)))
    OR (ts.kind='icp' AND (
         lower(regexp_replace(COALESCE(a.icp,''), '[[:space:]]+', '', 'g')) = ts.value
         OR lower(regexp_replace(COALESCE(a.app_icp,''), '[[:space:]]+', '', 'g')) = ts.value
       ))
  )
  JOIN context_tasks ctx ON ctx.task_id=ts.task_id
)`

// ListTaskScopeWithSources returns the current task's scope followed by the
// scopes of its direct source tasks. TaskScope.TaskID preserves provenance.
func (s *AssetStore) ListTaskScopeWithSources(taskID int64) ([]TaskScope, error) {
	rows, err := s.db.Query(`WITH `+directTaskContextCTE+`
SELECT ts.id, ts.task_id, ts.kind, COALESCE(ts.company_id,0), COALESCE(c.name,''), COALESCE(ts.domain,''),
       COALESCE(ts.net::text,''), COALESCE(ts.value,''), ts.source, COALESCE(ts.reason,'')
FROM task_scope ts
JOIN context_tasks ctx ON ctx.task_id=ts.task_id
LEFT JOIN companies c ON c.id=ts.company_id
ORDER BY CASE WHEN ts.task_id=$1 THEN 0 ELSE 1 END, ts.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskScope{}
	for rows.Next() {
		var scope TaskScope
		var companyID int64
		if err := rows.Scan(&scope.ID, &scope.TaskID, &scope.Kind, &companyID, &scope.CompanyName, &scope.Domain, &scope.Net, &scope.Value, &scope.Source, &scope.Reason); err != nil {
			return nil, err
		}
		if companyID > 0 {
			scope.CompanyID = &companyID
		}
		out = append(out, scope)
	}
	return out, rows.Err()
}

// TaskCoverageWithSources computes one coverage view over the union of the
// current task and its direct sources: source scopes and anchored assets extend
// the denominator, while fact anchors count as tested. No row is copied.
func (s *AssetStore) TaskCoverageWithSources(taskID int64) (*Coverage, error) {
	cov := &Coverage{ByType: []CoverageByType{}}
	_ = s.db.QueryRow(`WITH `+directTaskContextCTE+`
SELECT count(*) FROM task_scope ts JOIN context_tasks ctx ON ctx.task_id=ts.task_id`, taskID).Scan(&cov.ScopeRows)
	rows, err := s.db.Query(`WITH `+contextCoverageCTE+`
SELECT target.type, count(*) AS total,
       count(*) FILTER (WHERE target.id IN (SELECT asset_id FROM tested)) AS tested
FROM target GROUP BY target.type ORDER BY target.type`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var byType CoverageByType
		if err := rows.Scan(&byType.Type, &byType.Total, &byType.Tested); err != nil {
			return nil, err
		}
		cov.ByType = append(cov.ByType, byType)
		cov.Denominator += byType.Total
		cov.Tested += byType.Tested
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if cov.Denominator > 0 {
		pct := float64(cov.Tested) / float64(cov.Denominator)
		cov.Pct = &pct
	}
	return cov, nil
}

// ListUntestedAssetsWithSources is the direct-source-aware backlog query used
// by inherited tasks. Source fact anchors remove assets from the backlog.
func (s *AssetStore) ListUntestedAssetsWithSources(taskID int64, typ string, limit, offset int) ([]CoverageAsset, int, error) {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	typeFilter := ""
	args := []any{taskID}
	if typ != "" {
		typeFilter = " AND target.type = $2"
		args = append(args, typ)
	}
	var total int
	if err := s.db.QueryRow(`WITH `+contextCoverageCTE+`
SELECT count(*) FROM target WHERE target.id NOT IN (SELECT asset_id FROM tested)`+typeFilter, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	limitPosition := strconv.Itoa(len(args) + 1)
	offsetPosition := strconv.Itoa(len(args) + 2)
	rows, err := s.db.Query(`WITH `+contextCoverageCTE+`
SELECT target.id, target.type, target.label FROM target
WHERE target.id NOT IN (SELECT asset_id FROM tested)`+typeFilter+`
ORDER BY target.id LIMIT $`+limitPosition+` OFFSET $`+offsetPosition, pageArgs...)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	out := []CoverageAsset{}
	for rows.Next() {
		var asset CoverageAsset
		if err := rows.Scan(&asset.ID, &asset.Type, &asset.Label); err != nil {
			return nil, total, err
		}
		out = append(out, asset)
	}
	return out, total, rows.Err()
}

// HostsByTaskWithSources resolves exact HTTP host candidates from assets that
// are attached to, anchored by, or in scope for the current task or a direct
// source. Traffic remains global and is not copied. This read helper must not be
// used for destructive task cleanup; HostsByTask intentionally retains that
// narrower, task-owned behavior.
func (s *AssetStore) HostsByTaskWithSources(taskID int64) ([]string, error) {
	rows, err := s.db.Query(`WITH `+directTaskContextCTE+scopeAssetIDsCTE+`,
context_assets AS (
  SELECT DISTINCT a.id
  FROM assets a
  WHERE EXISTS (SELECT 1 FROM context_tasks ctx WHERE ctx.task_id=ANY(a.task_ids))
  UNION
  SELECT ea.asset_id
  FROM exploration_anchors ea
  JOIN exploration_nodes en ON en.id=ea.node_id
  JOIN context_tasks ctx ON ctx.exploration_id=en.exploration_id
  UNION
  SELECT id FROM scope_asset_ids
)
SELECT COALESCE(a.domain,''), COALESCE(a.ip,''), COALESCE(a.url,'')
FROM assets a JOIN context_assets ctx ON ctx.id=a.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hosts := map[string]struct{}{}
	add := func(host string) {
		host = strings.TrimSpace(strings.ToLower(host))
		if host != "" {
			hosts[host] = struct{}{}
		}
	}
	for rows.Next() {
		var domain, ip, rawURL string
		if err := rows.Scan(&domain, &ip, &rawURL); err != nil {
			return nil, err
		}
		add(domain)
		add(ip)
		if parsed, err := url.Parse(rawURL); err == nil {
			add(parsed.Hostname())
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(hosts))
	for host := range hosts {
		out = append(out, host)
	}
	sort.Strings(out)
	return out, nil
}
