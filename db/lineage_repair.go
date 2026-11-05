package db

import (
	"context"
	"database/sql"
	"fmt"
)

// repairGoalIntentLineage 把旧的「目标 --spawns--> 意图」边迁回 ARTEX 的起点派生模型：
// 意图只能锚在事实/漏洞上（顶层意图兜底连 origin fact），目标只保留 origin --spawns-->
// 目标这一条初始链；目标达成由 prove_goal 写 evidence --proves--> goal。
//
// 这是幂等数据修复：新库/已修复库上是空操作；老库在启动时执行一次。
func repairGoalIntentLineage(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `
DELETE FROM exploration_edges e
USING exploration_nodes g, exploration_nodes i
WHERE e.src_id = g.id AND e.dst_id = i.id
  AND e.rel = 'spawns' AND g.kind = 'goal' AND i.kind = 'intent'`); err != nil {
		return fmt.Errorf("remove goal→intent spawns: %w", err)
	}
	// 目标只保留「起点 --spawns--> 目标」这一条初始链；老图里若目标完全孤立，补回。
	if _, err := conn.ExecContext(ctx, `
INSERT INTO exploration_edges(exploration_id, src_id, rel, dst_id)
SELECT g.exploration_id, o.id, 'spawns', g.id
FROM exploration_nodes g
JOIN exploration_nodes o
  ON o.exploration_id = g.exploration_id AND o.kind = 'fact' AND o.state = 'origin'
WHERE g.kind = 'goal'
  AND NOT EXISTS (
    SELECT 1 FROM exploration_edges d
    WHERE d.exploration_id = g.exploration_id AND d.src_id = o.id
      AND d.rel = 'spawns' AND d.dst_id = g.id
  )
ON CONFLICT DO NOTHING`); err != nil {
		return fmt.Errorf("link goals to origin: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO exploration_edges(exploration_id, src_id, rel, dst_id)
SELECT i.exploration_id, o.id, 'derived_from', i.id
FROM exploration_nodes i
JOIN exploration_nodes o
  ON o.exploration_id = i.exploration_id AND o.kind = 'fact' AND o.state = 'origin'
WHERE i.kind = 'intent'
  AND NOT EXISTS (
    SELECT 1 FROM exploration_edges d
    WHERE d.exploration_id = i.exploration_id AND d.dst_id = i.id AND d.rel = 'derived_from'
  )
  AND NOT EXISTS (
    SELECT 1 FROM exploration_edges d
    WHERE d.exploration_id = i.exploration_id AND d.src_id = i.id AND d.rel = 'depends_on'
  )
ON CONFLICT DO NOTHING`); err != nil {
		return fmt.Errorf("link intents to origin: %w", err)
	}
	return nil
}
