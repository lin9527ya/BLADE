package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 技能文档只允许引用真实存在的工具。历史上 Pojun 导入的技能引用了
// listener_start/search_vulns/save_evidence 等平台上不存在的工具，模型照做会反复失败。
// 这里把「不存在」变成构建期错误，而不是等 agent 在任务里撞墙。
var retiredSkillTools = []string{
	"listener_start", "listener_sessions",
	"search_vulns", "get_vuln", "get_poc", "scan_with_nuclei",
	"save_evidence",
}

func TestSkillsDoNotReferenceMissingTools(t *testing.T) {
	root := filepath.Join("..", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Skip("skills/ 不存在（打包环境）")
	}
	patterns := make([]*regexp.Regexp, len(retiredSkillTools))
	for i, name := range retiredSkillTools {
		patterns[i] = regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `([^A-Za-z0-9_]|$)`)
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".md") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for ln, line := range strings.Split(string(b), "\n") {
			for i, re := range patterns {
				if re.MatchString(line) {
					t.Errorf("%s:%d 引用了不存在的工具 %q；请改用现有工具并更新说明", path, ln+1, retiredSkillTools[i])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
