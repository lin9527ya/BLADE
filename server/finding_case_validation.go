package server

import (
	"errors"
	"fmt"
	"strings"
)

// Require substantive sections and balanced fenced blocks. This checks report
// completeness, not the truth of a root-cause claim or a live exploit.
func validateUnifiedFindingReport(report string) error {
	aliases := [][]string{{"概述", "overview", "summary"}, {"影响", "impact"}, {"前置", "prerequisite", "precondition"}, {"复现", "reproduction", "reproduce"}, {"证据", "evidence"}, {"poc", "完整脚本", "验证脚本", "proof of concept"}, {"根因", "root cause"}, {"修复", "remediation", "recommendation", "fix"}}
	names := []string{"概述", "影响", "前置条件", "复现步骤", "证据", "PoC", "根因分析", "修复建议"}
	found := make([]bool, len(aliases))
	current := -1
	hasContent := false
	fence := ""
	finish := func() {
		if current >= 0 && hasContent {
			found[current] = true
		}
	}
	for _, line := range strings.Split(report, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			marker := t[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence != "" {
			if t != "" {
				hasContent = true
			}
			continue
		}
		if strings.HasPrefix(t, "#") {
			heading := strings.TrimLeft(t, "#")
			if len(heading) == len(t) || !strings.HasPrefix(heading, " ") {
				continue
			}
			// Subheadings belong to the current section unless they start another
			// required section.
			next := -1
			lower := strings.ToLower(heading)
			for i, words := range aliases {
				for _, word := range words {
					if strings.Contains(lower, word) {
						next = i
						break
					}
				}
				if next >= 0 {
					break
				}
			}
			if next >= 0 {
				finish()
				current = next
				hasContent = false
			}
			continue
		}
		if t != "" && t != "---" && t != "***" && t != ">" {
			hasContent = true
		}
	}
	finish()
	if fence != "" {
		return errors.New("统一报告代码块未闭合，可能截断；请补全后再保存")
	}
	missing := []string{}
	for i, ok := range found {
		if !ok {
			missing = append(missing, names[i])
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("统一报告缺少有内容的章节：%s；请补全后保存", strings.Join(missing, "、"))
	}
	return nil
}
