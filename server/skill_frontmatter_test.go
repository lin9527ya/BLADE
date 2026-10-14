package server

import (
	"strings"
	"testing"
)

// TestValidateSkillFrontmatter 钉住"能不能安全改写 frontmatter"的判据。
// 通过侧全部取自仓库里真实的四份 SKILL.md 形态（含 playwright-cli 的
// `allowed-tools: Bash(npx:*)` 这种"值里带冒号"），拒绝侧是真实事故形态与
// 常见的畸形写法。
func TestValidateSkillFrontmatter(t *testing.T) {
	// 事故原文片段：漏了收口的 ---，正文（标题/列表/JSON 块）被卷进前导块。
	// 一次「关联 MCP」的界面操作把它按 key: value 重排，`http://` 被拆成 `http: //`。
	incident := "---\n" +
		"## name: scopesentry-mcp\n" +
		"description: 通过 ScopeSentry MCP 管理安全扫描平台。\n" +
		"# ScopeSentry MCP 使用指南\n" +
		"面向**已部署 ScopeSentry 实例**的用户。\n" +
		"- 默认 Web 界面：`http: //<主机>`\n" +
		"```json\n" +
		"\"mcpServers\": {\n" +
		"  \"scopesentry\": {\n" +
		"---\n" +
		"# ScopeSentry MCP 使用指南\n"

	blockList := "---\nname: x\nmcps:\n  - browser\n  - filesystem\n---\n正文\n"
	noClose := "---\nname: x\ndescription: y\n# 标题\n正文\n"
	noOpen := "name: x\ndescription: y\n---\n正文\n"

	longFM := "---\n"
	for i := 0; i < skillFrontmatterMaxLines+2; i++ {
		longFM += "k" + string(rune('a'+i%26)) + ": v\n"
	}
	longFM += "---\n正文\n"

	cases := []struct {
		name    string
		md      string
		wantErr string // "" = 应通过；否则期望错误里出现的关键词
	}{
		{"最小规范", "---\nname: x\ndescription: y\n---\n正文\n", ""},
		{"真实：playwright-cli 的 allowed-tools", "---\nname: playwright-cli\ndescription: Automate browser interactions.\nallowed-tools: Bash(playwright-cli:*) Bash(npx:*) Bash(npm:*)\n---\n正文\n", ""},
		{"真实：api-recon（中文描述 + 空行收尾）", "---\nname: api-recon\ndescription: 收集网站API接口时调用该skill。\n---\n\n正文\n", ""},
		{"注释行允许", "---\n# 说明\nname: x\ndescription: y\n---\n正文\n", ""},
		{"空值键允许（由写入器补值）", "---\nname: x\nmcps:\n---\n正文\n", ""},
		{"事故原文：正文被卷进 frontmatter", incident, "第 2 行"}, // `## name:` 是漏进来的标题
		{"只有标题没有正文时也要拦住", "---\n## name: x\n---\n正文\n", "Markdown 标题"},
		{"块状列表（写入器处理不了，拒绝并给等价写法）", blockList, "不是 `key: value`"},
		{"没有收口的 ---", noClose, "没有收口"},
		{"没有开头的 ---", noOpen, "第 1 行"},
		{"围栏代码块", "---\nname: x\n```json\n{}\n```\n---\n正文\n", "代码围栏"},
		{"前导块超长", longFM, "上限"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateSkillFrontmatter([]byte(c.md))
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("应通过，实际被拒：%v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("应当被拒，实际通过")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("错误文案应含 %q，实际：%v", c.wantErr, err)
			}
			// 拒绝文案必须告诉用户"文件没动 + 怎么修"，否则用户只会看到失败。
			if !strings.Contains(err.Error(), "没有改动文件") {
				t.Fatalf("拒绝文案应说明文件未改动：%v", err)
			}
		})
	}
}

// TestSkillManifestIssues 钉住清单上的两级提示：硬（前导块不合法）/ 软（缺字段）。
func TestSkillManifestIssues(t *testing.T) {
	cases := []struct {
		name     string
		md       string
		wantHard string
		wantSoft string
	}{
		{"健康", "---\nname: x\ndescription: y\n---\n正文\n", "", ""},
		{"缺 name", "---\ndescription: y\n---\n正文\n", "", "缺 name"},
		{"缺 description", "---\nname: x\n---\n正文\n", "", "缺 description"},
		{"两个都缺", "---\nlicense: MIT\n---\n正文\n", "", "缺 name"},
		{"硬错误时不叠软警告", "---\n## name: x\n---\n正文\n", "Markdown 标题", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hard, soft := skillManifestIssues([]byte(c.md))
			if c.wantHard == "" && hard != "" {
				t.Fatalf("不该有硬错误：%s", hard)
			}
			if c.wantHard != "" && !strings.Contains(hard, c.wantHard) {
				t.Fatalf("硬错误应含 %q，实际 %q", c.wantHard, hard)
			}
			if c.wantSoft == "" && soft != "" {
				t.Fatalf("不该有软警告：%s", soft)
			}
			if c.wantSoft != "" && !strings.Contains(soft, c.wantSoft) {
				t.Fatalf("软警告应含 %q，实际 %q", c.wantSoft, soft)
			}
		})
	}
}
