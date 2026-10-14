package server

import (
	"fmt"
	"regexp"
	"strings"
)

// SKILL.md 的前导 frontmatter 安全护栏。
//
// 背景：仓库里三处消费者都按「第一个 --- 到下一个 ---」划界（norma 的加载器、
// 本文件的 rewriteSkillFrontmatter、上传校验的 skillNameFromFrontmatter）。一旦文件
// 漏了收口的 ---，划出来的区间会把正文卷进来——历史上真的发生过：一次正常的
// 「关联 MCP」界面操作把 30 行正文按 key: value 重排，空行、JSON 缩进、URL 里的
// `://` 全被改坏。所以**改写前**先确认"这段确实形如 frontmatter"，确认不了就拒绝。
const skillFrontmatterMaxLines = 40

// skillFrontmatterKeyRe 合法键：普通标识符（agentskills.io 风格；playwright-cli 用
// 的 `allowed-tools` 必须通过）。值里允许冒号（`allowed-tools: Bash(npx:*)`），
// 因为切分只认第一个冒号。
var skillFrontmatterKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)

// frontmatterFixHint 拒绝时统一附上的出路说明——拒绝不等于死路。
const frontmatterFixHint = "。这次没有改动文件；可以直接在技能文件编辑器里改 SKILL.md" +
	"（那是逐字节保存），或把 frontmatter 收成 `---` … `---` 之间若干行 `key: value`"

// skillFrontmatterSpan 定位前导块：start = 首行 --- 的下一行，end = 收口 --- 的行号
// （均为 0 基）。找不到就报错。
func skillFrontmatterSpan(md []byte) (start, end int, err error) {
	lines := strings.Split(string(md), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return 0, 0, fmt.Errorf("SKILL.md 第 1 行不是 `---`，没有 frontmatter 起始标记%s", frontmatterFixHint)
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return 1, i, nil
		}
	}
	return 0, 0, fmt.Errorf("SKILL.md 的 frontmatter 没有收口的 `---`（从第 2 行起都会被当成 frontmatter）%s", frontmatterFixHint)
}

// validateSkillFrontmatter 硬校验：前导块必须「形如 frontmatter」——每个非空行要么是
// 注释（#…），要么是 `key: value`；代码围栏、正文句子、块状列表项都判不合法。
// 这是"能不能安全改写"的判据，不是 manifest 合法性判据（缺 name/description 由
// skillManifestIssues 软提示）。错误文案面向用户，直接进 400 响应体。
func validateSkillFrontmatter(md []byte) error {
	start, end, err := skillFrontmatterSpan(md)
	if err != nil {
		return err
	}
	if end-start > skillFrontmatterMaxLines {
		return fmt.Errorf("SKILL.md 的 frontmatter 有 %d 行（上限 %d 行），多半是漏了收口的 `---` 把正文卷了进来%s",
			end-start, skillFrontmatterMaxLines, frontmatterFixHint)
	}
	lines := strings.Split(string(md), "\n")
	for i := start; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") {
			// 单 # 是 YAML 注释，原样保留；`## …` 是 Markdown 标题的样子——
			// frontmatter 里出现它，基本就是标题漏进来了（真实事故的第一行就是 `## name:`）。
			if strings.HasPrefix(t, "##") {
				return fmt.Errorf("SKILL.md 第 %d 行 `%s` 是 Markdown 标题的样子，漏进了 frontmatter%s",
					i+1, clipRunes(t, 40), frontmatterFixHint)
			}
			continue
		}
		if strings.HasPrefix(t, "```") {
			return fmt.Errorf("SKILL.md 第 %d 行是代码围栏（%s）——正文跑进了 frontmatter，八成是漏了收口的 `---`%s",
				i+1, clipRunes(t, 40), frontmatterFixHint)
		}
		idx := strings.IndexByte(lines[i], ':')
		if idx < 0 {
			return fmt.Errorf("SKILL.md 第 %d 行不是 `key: value` 形态（%s）%s",
				i+1, clipRunes(t, 60), frontmatterFixHint)
		}
		key := strings.TrimSpace(lines[i][:idx])
		if !skillFrontmatterKeyRe.MatchString(key) {
			return fmt.Errorf("SKILL.md 第 %d 行的键 `%s` 不是合法标识符（%s）%s",
				i+1, clipRunes(key, 40), clipRunes(t, 60), frontmatterFixHint)
		}
	}
	return nil
}

// skillManifestIssues 给技能清单用：hard = 前导块不合法（元数据改动会被拒绝），
// soft = 影响可读性的清单问题（缺 name / 缺 description）。前导块都不合法时只报 hard，
// 免得再往上叠一堆"缺字段"的噪声。
func skillManifestIssues(md []byte) (hard, soft string) {
	if err := validateSkillFrontmatter(md); err != nil {
		return err.Error(), ""
	}
	start, end, err := skillFrontmatterSpan(md)
	if err != nil {
		return err.Error(), ""
	}
	lines := strings.Split(string(md), "\n")
	hasName, hasDesc := false, false
	for i := start; i < end; i++ {
		idx := strings.IndexByte(lines[i], ':')
		if idx < 0 {
			continue
		}
		switch strings.TrimSpace(lines[i][:idx]) {
		case "name":
			hasName = true
		case "description":
			hasDesc = true
		}
	}
	var missing []string
	if !hasName {
		missing = append(missing, "缺 name 字段（技能名会退回目录名）")
	}
	if !hasDesc {
		missing = append(missing, "缺 description（Agent 看不到它是做什么的）")
	}
	return "", strings.Join(missing, "；")
}
