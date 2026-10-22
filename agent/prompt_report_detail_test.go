package agent

import (
	"strings"
	"testing"
)

// TestReporterPromptRequiresExploitationDetail 钉住报告模板里那几条"最容易漏、漏了
// 报告就没法用"的要求：详细利用过程、绝对路径与访问 URL、执行过的命令与回显、
// 清理与遗留物。改提示词时把任何一条删掉，这里会红。
func TestReporterPromptRequiresExploitationDetail(t *testing.T) {
	for _, want := range []string{
		"## 利用过程（详细）",
		"绝对路径",
		"访问 URL",
		"## 关键位置与产物",
		"## 执行过的命令与回显",
		"## 清理与遗留物",
		"绝不臆造",
		"确认它确实存在", // 未确认不写报告：写之前先过这道门
		"未确认不写报告",
	} {
		if !strings.Contains(ReporterDefaultPrompt, want) {
			t.Errorf("reporter 提示词缺少「%s」：报告会退回「只写结论」的旧样子", want)
		}
	}
	// worker 侧要有把原始材料交给 reporter 的要求，否则报告 agent 无从还原。
	for _, want := range []string{"落地文件的绝对路径", "关键回显", "url"} {
		if !strings.Contains(workerDefaultTmpl, want) {
			t.Errorf("worker 提示词缺少「%s」：上报证据里不会有路径/URL/回显", want)
		}
	}
}
