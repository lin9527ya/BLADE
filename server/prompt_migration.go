package server

import (
	"crypto/sha256"
	"encoding/hex"
	"log"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// plannerPromptV1SHA256 是重写前 planner 内置正文的 sha256（见
// docs/prompts/planner/default.body.md）。启动时只迁移"仍等于这份旧内置正文"的库：
// 用户自己编辑过的正文 hash 不同，一律不覆盖。
const (
	plannerPromptV1SHA256  = "5ca5b8b2147c6aa75ee0b02436b4aa4238e9bc07b03230a0946ee5f41c457f84"
	settingPlannerPromptV2 = "planner_prompt_v2"
)

// migratePlannerPrompt 把旧版内置 planner 正文升级到 v2（经典模式瘦身版）。
// 只执行一次（settings 旗标）；领域模板不在这里处理——planner_templates 的内置行
// 每次启动都会被 SeedPlannerTemplateIfEmpty 刷新正文，用户自建模板不受影响。
func migratePlannerPrompt(pg *db.DB) {
	if pg == nil {
		return
	}
	if v, ok, err := pg.GetSetting(settingPlannerPromptV2); err != nil || (ok && v == "true") {
		return
	}
	a, err := pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		return
	}
	cur, err := pg.CurrentPrompt(a.ID)
	if err != nil {
		log.Printf("[prompts] 读取 planner 正文失败，跳过 v2 迁移: %v", err)
		return
	}
	sum := sha256.Sum256([]byte(cur))
	switch {
	case cur != "" && hex.EncodeToString(sum[:]) == plannerPromptV1SHA256:
		if _, err := pg.SavePrompt(a.ID, agent.PlannerPromptBody(), "重写：经典模式瘦身（v2）", "system"); err != nil {
			log.Printf("[prompts] planner 正文 v2 迁移失败: %v", err)
			return
		}
		log.Printf("[prompts] planner 正文已迁移到 v2（旧正文备份见 docs/prompts/planner）")
	case cur != "":
		log.Printf("[prompts] planner 正文已被编辑，保留现状（旧正文备份见 docs/prompts/planner）")
	}
	_ = pg.SetSetting(settingPlannerPromptV2, "true")
}
