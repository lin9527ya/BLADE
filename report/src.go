package report

import (
	"fmt"
	"strings"
)

// SRC 交付版式（对齐 src-dig 报告规范：8 块 + 全角＃分隔排版）。
//
// 导出的是「机器已知事实的骨架」：标题 / 目标 URL / 等级 / 涉及接口清单 / 复现整包
// 由 BLADE 从发现记录与绑定证据里填齐；描述 / 危害 / 修复建议给规范占位提示，交人工
// 按写作纪律补写后提交。渲染器保持纯函数，便于单测与两种输出（md / docx）共用一份输入。

// SrcEndpoint 是涉及接口清单的一条（只列真正打过的接口）。
type SrcEndpoint struct {
	Method string
	URL    string
	Source string // 出处：绑定的证据角色 / 流量 id / note
}

// SrcStep 是复现步骤的一条：一两句描述 + 一个可复制整包（请求行/头/空行/体）。
type SrcStep struct {
	Desc string
	Raw  string
}

// SrcInput is everything the SRC export renders, already resolved by the caller.
type SrcInput struct {
	Title       string
	TargetURL   string
	Severity    string // 内部取值 critical|high|medium|low
	VulnClass   string
	Description string // 空 → 规范占位
	Impact      string // 空 → 规范占位
	Endpoints   []SrcEndpoint
	Steps       []SrcStep
	Fix         string // 空 → 规范占位
	// Notes 走文件末尾备注区（发现 id / 证据版本 / 生成时间 / 撤回警告），不进 8 块正文。
	Notes []string
}

// SrcSeverityLabel maps the internal severity to 交付用语（只写中危/高危/严重；低危
// 照实标出，便于人工判断该不该交）。
func SrcSeverityLabel(sev string) string {
	switch sev {
	case "critical":
		return "严重"
	case "high":
		return "高危"
	case "medium":
		return "中危"
	case "low":
		return "低危"
	}
	return nz(sev, "中危")
}

// SrcMarkdown renders the 8-block SRC submission layout. 排版纪律：标题/URL/等级各占一
// 行不分隔；「漏洞描述：」「漏洞危害：」冒号后换行再写正文；大块之间单独一行全角＃
// （禁止半角 #）；复现步骤每条一步、步间一行＃；不出现多余空行。
func SrcMarkdown(in SrcInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "漏洞标题：%s\n", nz(in.Title, "（待补：谁的站、做成了啥）"))
	fmt.Fprintf(&b, "目标网站URL：%s\n", nz(in.TargetURL, "（待补）"))
	fmt.Fprintf(&b, "漏洞等级：%s\n", SrcSeverityLabel(in.Severity))
	b.WriteString("＃\n")
	b.WriteString("漏洞描述：\n")
	b.WriteString(nz(in.Description, "（待补：第一人称大白话写做成了什么；没登录态用「我没登录」，有会话用「我拿自己的会话」）"))
	b.WriteString("\n＃\n")
	b.WriteString("漏洞危害：\n")
	b.WriteString(nz(in.Impact, "（待补：只写最终伤害，每种伤害单独一行；不写步骤）"))
	b.WriteString("\n＃\n")
	b.WriteString("涉及接口清单\n")
	if len(in.Endpoints) == 0 {
		b.WriteString("（无已登记接口：证据流量未绑定，请在发现详情绑定 proof/verification 流量后重新导出）\n")
	} else {
		for i, ep := range in.Endpoints {
			line := strings.TrimSpace(strings.TrimSpace(ep.Method) + " " + ep.URL)
			if strings.TrimSpace(ep.Source) != "" {
				line += "（" + strings.TrimSpace(ep.Source) + "）"
			}
			fmt.Fprintf(&b, "%d、%s\n", i+1, line)
		}
	}
	b.WriteString("＃\n")
	b.WriteString("复现步骤\n")
	if len(in.Steps) == 0 {
		b.WriteString("（无整包证据：请在发现详情绑定 proof/verification 流量后重新导出）\n")
	} else {
		for i, st := range in.Steps {
			fmt.Fprintf(&b, "%d、%s\n", i+1, nz(st.Desc, "打穿步骤"))
			// 整包按 LF 输出（提交平台惯例；原始报文多为 CRLF）。
			raw := strings.ReplaceAll(strings.TrimRight(st.Raw, "\r\n"), "\r\n", "\n")
			if strings.TrimSpace(raw) != "" {
				b.WriteString("```\n")
				b.WriteString(raw)
				b.WriteString("\n```\n")
			}
			if i < len(in.Steps)-1 {
				b.WriteString("＃\n")
			}
		}
	}
	b.WriteString("＃\n")
	b.WriteString("修复建议\n")
	b.WriteString(nz(in.Fix, "（待补：贴合本漏洞、可落地的整改方案）"))
	b.WriteString("\n")
	for _, n := range in.Notes {
		if strings.TrimSpace(n) == "" {
			continue
		}
		if !strings.Contains(b.String(), "＃\n备注：") {
			b.WriteString("＃\n")
		}
		b.WriteString("备注：" + strings.TrimSpace(n) + "\n")
	}
	return b.String()
}

// SrcFilename 是下载文件名（规范：文件名就是漏洞标题那一行）；去掉路径分隔符与
// 控制字符后截断，保证各平台可写。
func SrcFilename(title, ext string) string {
	name := unsafeFilenameChars.ReplaceAllString(strings.TrimSpace(title), "_")
	name = strings.Trim(name, "._")
	if runes := []rune(name); len(runes) > 80 {
		name = string(runes[:80])
	}
	if name == "" {
		name = "漏洞报告"
	}
	return name + ext
}

// Section 从报告 Markdown 里取一个「小节」的正文：标题行包含 keyword 的那段，直到下一个
// 标题（# 开头）或文末。用于把报告 Agent 写的「影响 / 修复建议」摘出来填进 SRC 提交稿——
// 这两个字段此前从来没被赋值，导出稿永远带着「（待补：…）」占位。
func Section(md, keyword string) string {
	if strings.TrimSpace(md) == "" || strings.TrimSpace(keyword) == "" {
		return ""
	}
	lines := strings.Split(md, "\n")
	start := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "#") {
			continue
		}
		if strings.Contains(t, keyword) {
			start = i + 1
			continue
		}
		if start >= 0 { // 下一个标题 → 小节结束
			return strings.TrimSpace(strings.Join(lines[start:i], "\n"))
		}
	}
	if start >= 0 {
		return strings.TrimSpace(strings.Join(lines[start:], "\n"))
	}
	return ""
}
