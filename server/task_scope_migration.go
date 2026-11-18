package server

import (
	"log"
	"net"

	"github.com/lin9527ya/blade/db"
)

// migrateAssetScopeTarget 是一次性迁移：把资产范围策略引入前创建的 exact 任务
// 升级为 target（目标所属全资产）。它只处理“旧任务”，一次性 flag 保证用户后来
// 手动切回 exact 的任务不会被重启重新放宽。
//
// 扩面依据只取可审计来源：任务描述/目标里的显式主机、已有 task_scope 的
// root_domain/subdomain 行、任务显式关联企业，以及目标主机/ IP 的公司归因。
// 不猜测名称相似的第三方根域。
func (s *Server) migrateAssetScopeTarget() {
	pg := s.m.PG()
	if pg == nil {
		return
	}
	const flag = "asset_scope_target_migration_v1"
	if v, _, _ := pg.GetSetting(flag); v == "true" {
		return
	}
	tasks, err := pg.ListTasks()
	if err != nil {
		log.Printf("[scope-migrate] 读取任务失败，下次启动重试: %v", err)
		return
	}
	as := s.m.Assets()
	if as == nil {
		return
	}
	changed, failed := 0, 0
	for _, t := range tasks {
		if t.AssetScopePolicy != db.AssetScopePolicyExact {
			continue
		}
		taskID := t.ID
		if taskID <= 0 {
			continue
		}
		roots := make([]string, 0, 4)
		companies := append([]int64(nil), t.CompanyIDs...)
		if _, host, _, ok := parseTarget(t.Description + " " + t.Goal); ok && host != "" {
			if ip := net.ParseIP(host); ip != nil {
				if cid, found, err := as.ResolveCompanyForHost(host); err == nil && found {
					companies = append(companies, cid)
				}
			} else {
				roots = append(roots, host)
			}
		}
		// 已有 root/subdomain 范围行只在**显式来源**（手工/agent/policy）下才提升
		// 为 policy 根行；source=auto 可能是 worker 撞到的第三方根域，不能借迁移
		// 把它整域授权。seed 目标由上面的 parseTarget 覆盖，不依赖 auto 行。
		if scopes, err := as.ListTaskScope(taskID); err == nil {
			for _, sc := range scopes {
				if sc.Source != "manual" && sc.Source != "agent" && sc.Source != "policy" {
					continue
				}
				if sc.Kind == "root_domain" || sc.Kind == "subdomain" {
					if sc.Domain != "" {
						roots = append(roots, sc.Domain)
					}
				}
			}
		}
		for _, root := range roots {
			if cid, found, err := as.ResolveCompanyForHost(root); err == nil && found {
				companies = append(companies, cid)
			}
		}
		if _, err := as.SetTaskAssetScopePolicy(taskID, db.AssetScopePolicyTarget, roots, companies); err != nil {
			log.Printf("[scope-migrate] 任务 %d 升级 target 失败（下次启动重试）: %v", t.ID, err)
			failed++
			continue
		}
		if s.m.interceptor != nil {
			s.m.interceptor.InvalidateTaskScope(taskID)
		}
		changed++
	}
	if failed > 0 {
		log.Printf("[scope-migrate] %d 个任务升级失败，保留迁移标记待下次启动重试", failed)
		return
	}
	if err := pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[scope-migrate] 记录迁移标记失败，下次启动会重跑（幂等）: %v", err)
		return
	}
	if changed > 0 {
		log.Printf("[scope-migrate] 已将 %d 个旧 exact 任务升级为 target（目标所属全资产）", changed)
	}

}
