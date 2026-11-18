package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// add_intent 已不再接受 uses_loot_ids（ARTEX 对齐时移除）。提示词若继续教模型传它，
// 模型每轮都会白试一次。这里扫描**现役提示词源**（agent/planner.go 的内置正文与领域模板），
// 命中即失败。docs/prompts/planner 是历史正文备份，迁移测试依赖其原文，不参与本检查。
func TestPlannerPromptsDoNotReferenceRemovedFields(t *testing.T) {
	paths := []string{filepath.Join("..", "agent", "planner.go"), filepath.Join("..", "agent", "planner_domain_templates.go")}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for ln, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "uses_loot_ids") {
				t.Errorf("%s:%d 仍在教模型使用已移除的 uses_loot_ids；执行类工具会自动记录 uses", path, ln+1)
			}
		}
	}
}
