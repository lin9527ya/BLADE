package db

import (
	"database/sql"
	"fmt"
	"net"
	"strings"
)

// 资产范围策略读写：exact=只测明确登记主机；target=目标所属全资产
// （同 eTLD+1 + 同公司跨根域 + 本任务发现/锚定资产）。

// TaskAssetScopePolicy 返回任务策略；不存在/非法一律按 exact（DB 安全默认）。
func (s *AssetStore) TaskAssetScopePolicy(taskID int64) string {
	if taskID <= 0 {
		return AssetScopePolicyExact
	}
	var policy string
	err := s.db.QueryRow(`SELECT COALESCE(asset_scope_policy,'exact') FROM tasks
WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&policy)
	if err != nil {
		return AssetScopePolicyExact
	}
	return NormalizeAssetScopePolicy(policy)
}

// ResolveCompanyForHost 用现有公司范围规则（域名 root/ip/icp）判断主机归属。
// 不猜测：没有配置命中时返回 found=false。
func (s *AssetStore) ResolveCompanyForHost(host string) (int64, bool, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return 0, false, nil
	}
	var cid *int64
	var err error
	if net.ParseIP(host) != nil {
		cid, err = s.resolveCompanyWithICP("", host, "")
	} else {
		root, _ := RootDomain(host)
		cid, err = s.resolveCompanyWithICP(root, "", "")
	}
	if err != nil {
		return 0, false, err
	}
	if cid == nil || *cid <= 0 {
		return 0, false, nil
	}
	return *cid, true, nil
}

// SetTaskAssetScopePolicy 原子切换策略并维护 source='policy' 的范围行。
// targetRoots / companyIDs 由调用方给出（目标根域与显式/归因公司）。
// 返回新增/删除的策略行数。
func (s *AssetStore) SetTaskAssetScopePolicy(taskID int64, policy string, targetRoots []string, companyIDs []int64) (int, error) {
	if taskID <= 0 {
		return 0, fmt.Errorf("需要 task_id")
	}
	policy = NormalizeAssetScopePolicy(policy)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	var current string
	if err := tx.QueryRow(`SELECT COALESCE(asset_scope_policy,'exact') FROM tasks
WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, taskID).Scan(&current); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("任务不存在")
		}
		return 0, err
	}
	if NormalizeAssetScopePolicy(current) != policy {
		if _, err := tx.Exec(`UPDATE tasks SET asset_scope_policy=$2 WHERE id=$1`, taskID, policy); err != nil {
			return 0, err
		}
	}
	changed := 0
	if policy == AssetScopePolicyTarget {
		// 只提升调用方给出的目标根域，不把 worker 自动发现的第三方根域整域授权。
		seen := map[string]bool{}
		for _, raw := range targetRoots {
			host := DomainKey(raw)
			if host == "" || net.ParseIP(host) != nil {
				continue
			}
			root, _ := RootDomain(host)
			if root == "" || seen[root] {
				continue
			}
			seen[root] = true
			res, err := tx.Exec(`INSERT INTO task_scope(task_id,kind,domain,source,reason)
VALUES($1,'root_domain',$2,'policy','范围策略 target：目标归约到 eTLD+1')
ON CONFLICT DO NOTHING`, taskID, root)
			if err != nil {
				return 0, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				changed += int(n)
			}
		}
		for _, cid := range companyIDs {
			if cid <= 0 {
				continue
			}
			res, err := tx.Exec(`INSERT INTO task_scope(task_id,kind,company_id,source,reason)
VALUES($1,'company',$2,'policy','范围策略 target：同公司资产（可跨根域）')
ON CONFLICT DO NOTHING`, taskID, cid)
			if err != nil {
				return 0, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				changed += int(n)
			}
		}
	} else {
		res, err := tx.Exec(`DELETE FROM task_scope
WHERE task_id=$1 AND source='policy' AND kind IN ('root_domain','company')`, taskID)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		changed = int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}

// HostInDeclaredScope 判断一个主机是否属于任务的可测资产集合。
// 复用 scopeTargetCTE：包含 task_scope 成员、target 策略下的本任务 task_ids 与
// 探索锚定资产。只用于 RoE 的成员判定，不扩大任何写入。
func (d *DB) HostInDeclaredScope(taskID int64, host string) (bool, error) {
	host = strings.ToLower(stripHostPort(strings.TrimSpace(host)))
	if taskID <= 0 || host == "" {
		return false, nil
	}
	var ok bool
	err := d.QueryRow(`WITH `+scopeTargetCTE+`
SELECT EXISTS(
  SELECT 1 FROM target t JOIN assets a ON a.id=t.id
  WHERE lower(COALESCE(a.domain,''))=$2
     OR lower(COALESCE(a.root_domain,''))=$2
     OR lower(COALESCE(a.ip,''))=$2
     OR lower(split_part(split_part(COALESCE(a.url,''),'://',2),'/',1))=$2
     OR lower(split_part(split_part(split_part(COALESCE(a.url,''),'://',2),'/',1),':',1))=$2
)
OR EXISTS(
  -- 公司范围要能授权“尚未入资产图”的同公司主机：company_scope 的 domain
  -- 覆盖该根域及其子域，ip/cidr 覆盖该地址；否则新主机在 insert_assets 之前
  -- 会被 RoE 误判为越界。
  SELECT 1
  FROM task_scope ts
  JOIN context_tasks ctx ON ctx.task_id=ts.task_id
  JOIN company_scope cs ON cs.company_id=ts.company_id
  WHERE ts.kind='company' AND (
    (cs.kind='domain' AND (lower($2)=lower(cs.domain) OR lower($2) LIKE '%.' || lower(cs.domain)))
    OR (cs.kind IN ('ip','cidr') AND cs.net >>= try_inet($2))
  )
)`, taskID, host).Scan(&ok)
	return ok, err
}

// AssetBlockedOverride 返回主机是否被资产闸门拦截（block 优先，task allow 可覆盖）。
// 与 insert_assets 的完整闸门不同：这里只回答“RoE 要不要因 .gov/.edu 等规则转人工”。
func (s *AssetStore) AssetBlockedOverride(taskID int64, host string) (string, bool, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", false, nil
	}
	var ips []string
	if net.ParseIP(host) != nil {
		ips = []string{host}
	}
	return s.EvaluateInterceptGate(taskID, host, ips, nil)
}

// EvaluateInterceptGate checks enabled global asset_intercept_rules merged with
// per-task task_intercept_rules (block & allow) via EvaluateAssetGate.
func (s *AssetStore) EvaluateInterceptGate(taskID int64, host string, ips, urls []string) (string, bool, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", false, nil
	}
	blockRules, err := s.db.ListAssetInterceptRules()
	if err != nil {
		return "", false, err
	}
	var allowRules []AssetInterceptRule
	if taskID > 0 {
		tb, ta, err := s.TaskInterceptRulesSplit(taskID)
		if err != nil {
			return "", false, err
		}
		blockRules = append(blockRules, tb...)
		allowRules = ta
	}
	if len(blockRules) == 0 && !hasEnabledRule(allowRules) {
		return "", false, nil
	}
	dec := EvaluateAssetGate(blockRules, allowRules, []string{host}, ips, urls)
	if dec.Allowed {
		return "", false, nil
	}
	return dec.Reason, true, nil
}
