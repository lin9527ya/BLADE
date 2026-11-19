package server

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// plannerPromptV1SHA256 是重写前 planner 内置正文的 sha256（见
// docs/prompts/planner/default.body.md）。启动时只迁移"仍等于这份旧内置正文"的库：
// 用户自己编辑过的正文 hash 不同，一律不覆盖。
// plannerBuiltinPromptSHA256 是**历史内置正文**的 sha256 集合（按 TrimSpace 后计算）。
// 单凭最后一个版本不够：老库的 planner 正文是首次安装那一版播种后就不再更新（seedPrompts
// 首插即止），所以 v0.7/v0.8 的库会停在当时的内置正文上。用户自己编辑过的正文 hash 不在
// 集合里，一律不覆盖。
var plannerBuiltinPromptSHA256 = map[string]bool{
	"76bfe3acd0d53701f00e64966f7d3f0efaeb959c4e29cb1dcf61b7cd9720970c": true, // v0.7.0–v0.7.3
	"e11d4585404488bd7123426bb6577a3ade660d89c9c337200ba4e9daf93bd7d0": true, // v0.7.4–v0.8.0
	"572992a24cbc479fcd22b167a815c60ad72aea1b5ce0fd5c7d5317ccb82a75b7": true, // v0.8.1–v0.8.6
	"00602dbb6b504b9a611c9311c22742756c06bf00720f3430b8c3e31f1c414384": true, // v0.8.7–v0.8.9
	"5ca5b8b2147c6aa75ee0b02436b4aa4238e9bc07b03230a0946ee5f41c457f84": true, // v0.9.0–v0.9.2（重写前）
	"7e178641779bc359ba32198ee15d4bfa0fda2d73814f322c85d00576af7dc24d": true, // 重写 v2（首版瘦身，缺态势语义）
	"bf8ad2e280317c2110fcfbd50b342c28bae83e77cc053c2ce2dea50298b84109": true, // v0.9.19–20
	"0ffa97062f5a4d92a2f117150fee63a85acf20ef620ffb81e003afd9a1643eea": true, // v0.9.21
	"3ee238ddd5bf7088e89db620cc46f6e2178ab46d66ab7061077947cf439eb1d6": true, // v0.9.22
	"6a832e1146013f13d6fceaca1d89be85bddc2cf7e5515b9b525d8da8bd4b8430": true, // ARTEX parity v3（扩面/瓶颈提示前）
}

const settingPlannerPromptV3 = "planner_prompt_v3"

// 自定义正文与默认正文的区别只能精确识别，不能靠“含某个旧特征词”。
func plannerPromptIsBuiltin(cur string) bool {
	cur = strings.TrimSpace(cur)
	if cur == "" || cur == strings.TrimSpace(agent.PlannerPromptBody()) {
		return true
	}
	sum := sha256.Sum256([]byte(cur))
	return plannerBuiltinPromptSHA256[hex.EncodeToString(sum[:])]
}

// migratePlannerPrompt 把旧版内置 planner 正文升级到 v3（经典瘦身 + 态势语义）。
// 只执行一次（settings 旗标）；领域模板不在这里处理——planner_templates 的内置行
// 每次启动都会被 SeedPlannerTemplateIfEmpty 刷新正文，用户自建模板不受影响。
func migratePlannerPrompt(pg *db.DB) {
	if pg == nil {
		return
	}
	if v, ok, err := pg.GetSetting(settingPlannerPromptV3); err != nil || (ok && v == "true") {
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
	sum := sha256.Sum256([]byte(strings.TrimSpace(cur)))
	switch {
	case cur != "" && plannerBuiltinPromptSHA256[hex.EncodeToString(sum[:])]:
		appendText, err := pg.CurrentPromptAppend(a.ID)
		if err != nil {
			log.Printf("[prompts] 读取 planner 追加段失败，未迁移: %v", err)
			return
		}
		if _, err := pg.SavePromptWithAppend(a.ID, agent.PlannerPromptBody(), appendText, "重写：经典模式瘦身（v3）", "system"); err != nil {
			log.Printf("[prompts] planner 正文 v3 迁移失败: %v", err)
			return
		}
		log.Printf("[prompts] planner 正文已迁移到 v3（旧正文备份见 docs/prompts/planner）")
	case cur != "":
		log.Printf("[prompts] planner 正文已被编辑，保留现状（旧正文备份见 docs/prompts/planner）")
	}
	_ = pg.SetSetting(settingPlannerPromptV3, "true")
}

// Refresh only byte-identical historical builtins, on every startup rather than
// a once-only flag. A past migration flag does not pin a system default forever.
// User bodies and append text survive; old versions remain in prompt history.
var workerBuiltinPromptSHA256 = map[string]bool{
	"7ba5df1b5c45fd3bfe0f638efc99a441c499a1d767a21961de0b13e1d16b812c": true,
	"dc3878b69229870a0cd8a0cd96cb7c6f2e9007cdf5c3d3d6abc616c6a20e2c3c": true,
}

func migrateExplorationPrompts(pg *db.DB) {
	if pg == nil {
		return
	}
	migrateBuiltinPrompt(pg, "planner", plannerBuiltinPromptSHA256)
	migrateBuiltinPrompt(pg, "worker", workerBuiltinPromptSHA256)
	for _, seed := range agent.BuiltinToolSeeds() {
		if seed.Key != "record_fact" {
			continue
		}
		cur, err := pg.GetTool(seed.Key)
		if err != nil || cur == nil || !cur.System || cur.Description == seed.Desc {
			continue
		}
		sum := sha256.Sum256([]byte(cur.Description))
		if hex.EncodeToString(sum[:]) == "8b9446c8c11a2cd45f6f5c45bb541a732c399415a718b44b69479e4619078593" {
			if err := pg.UpdateToolDescriptionIfUnchanged(seed.Key, cur.Description, seed.Desc); err != nil {
				log.Printf("[prompts] 更新 record_fact 默认说明失败: %v", err)
			}
		}
	}
}

func migrateBuiltinPrompt(pg *db.DB, key string, hashes map[string]bool) {
	a, err := pg.GetAgentByKey(key)
	if err != nil || a == nil {
		return
	}
	cur, err := pg.CurrentPrompt(a.ID)
	if err != nil {
		return
	}
	target := agent.BuiltinPromptSeeds()[key]
	if target == "" || strings.TrimSpace(cur) == strings.TrimSpace(target) {
		return
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(cur)))
	if !hashes[hex.EncodeToString(sum[:])] {
		return
	}
	extra, err := pg.CurrentPromptAppend(a.ID)
	if err != nil {
		return
	}
	if _, err := pg.SavePromptWithAppend(a.ID, target, extra, "探索闭环：统一内置协议", "system"); err != nil {
		log.Printf("[prompts] 更新 %s 内置正文失败: %v", key, err)
	}
}
