package agent

import (
	"log"

	"github.com/lin9527ya/blade/db"
)

// noteLootUse 记录“当前意图实际动用了哪条战果”（intent --uses--> loot）。
//
// uses 边原先由 add_intent.uses_loot_ids 声明；ARTEX 重构移除了该字段，导致攻击链
// 与报告里的“战果动用”长期为空。现在改由执行类工具在**成功提交后**自动写：
// 只认 worker/mainagent 当前持有的意图，失败不阻断动作，只留日志。
// uses 只被链路/报告/统计读取，不参与任何完成门或调度。
func (t *ToolSet) noteLootUse(lootID int64) {
	if lootID <= 0 || t.ts == nil || t.ownerNode <= 0 || !t.ownerIsIntent {
		return
	}
	if err := t.ts.Link(t.ownerNode, db.RelUses, lootID); err != nil {
		log.Printf("[loot] 记录 uses 边失败 intent=%d loot=%d: %v", t.ownerNode, lootID, err)
	}
}
