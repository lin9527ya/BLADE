package report

import (
	"strings"
	"testing"
	"time"
)

// TestSrcMarkdownLayout pins the src-dig 交付排版纪律：标题/URL/等级各占一行不分隔；
// 「漏洞描述：」「漏洞危害：」冒号后换行再写正文；大块之间单独一行全角＃；复现步骤
// 每条一步、步间一行＃；整包进可复制代码块；空内容给规范占位而非静默留白。
func TestSrcMarkdownLayout(t *testing.T) {
	in := SrcInput{
		Title:       "某站匿名未授权 SQL 注入（可批量拖库）",
		TargetURL:   "https://example.com/search",
		Severity:    "critical",
		Description: "我没登录，用搜索框的单引号直接写出了库名。",
		Impact:      "能批量拖走用户表（手机号、地址）。",
		Endpoints: []SrcEndpoint{
			{Method: "GET", URL: "https://example.com/search?q=1", Source: "proof 流量 #12"},
			{Method: "GET", URL: "https://example.com/search?q=1'"},
		},
		Steps: []SrcStep{
			{Desc: "打出报错注入，证明参数进了 SQL。", Raw: "GET /search?q=1' HTTP/1.1\r\nHost: example.com\r\n\r\n"},
			{Desc: "带出库名。", Raw: "GET /search?q=1'/**/and/**/1=2/**/union/**/select/**/database()-- HTTP/1.1\r\nHost: example.com"},
		},
		Fix:   "参数化查询。",
		Notes: []string{"发现 #42 · 证据版本 v3", "生成时间：" + time.Unix(0, 0).Format("2006-01-02")},
	}
	md := SrcMarkdown(in)

	// 8 块与行内形状
	for _, want := range []string{
		"漏洞标题：某站匿名未授权 SQL 注入（可批量拖库）\n",
		"目标网站URL：https://example.com/search\n",
		"漏洞等级：严重\n",
		"漏洞描述：\n我没登录",
		"漏洞危害：\n能批量拖走用户表",
		"涉及接口清单\n1、GET https://example.com/search?q=1（proof 流量 #12）\n2、GET https://example.com/search?q=1'\n",
		"复现步骤\n1、打出报错注入，证明参数进了 SQL。\n```\nGET /search?q=1' HTTP/1.1\nHost: example.com\n```\n",
		"修复建议\n参数化查询。\n",
		"备注：发现 #42 · 证据版本 v3\n",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("导出缺少 %q\n---\n%s", want, md)
		}
	}
	// 复现步骤之间一行＃；描述/危害/清单/步骤/修复各块之间也是＃
	if n := strings.Count(md, "\n＃\n"); n < 5 {
		t.Fatalf("分隔线数量异常(%d)：\n%s", n, md)
	}
	// 禁止半角 # 作为分隔行（排版纪律）
	for _, line := range strings.Split(md, "\n") {
		if strings.TrimSpace(line) == "#" {
			t.Fatalf("出现半角 # 分隔行：\n%s", md)
		}
	}

	// 空内容 → 规范占位（不静默留白）；低危照实标出
	bare := SrcMarkdown(SrcInput{Title: "T", TargetURL: "https://x", Severity: "low"})
	for _, want := range []string{"漏洞等级：低危", "（待补：第一人称大白话", "（待补：只写最终伤害", "（无已登记接口", "（无整包证据", "（待补：贴合本漏洞"} {
		if !strings.Contains(bare, want) {
			t.Fatalf("占位缺失 %q：\n%s", want, bare)
		}
	}
}

// TestSrcSeverityAndFilename pins the label mapping and the download filename rule
// (文件名就是漏洞标题那一行；剥离路径分隔符/控制字符并限长).
func TestSrcSeverityAndFilename(t *testing.T) {
	for sev, want := range map[string]string{"critical": "严重", "high": "高危", "medium": "中危", "low": "低危"} {
		if got := SrcSeverityLabel(sev); got != want {
			t.Fatalf("SrcSeverityLabel(%s)=%s want %s", sev, got, want)
		}
	}
	if got := SrcFilename("某站/匿名:SQL 注入", ".docx"); strings.ContainsAny(got, `/\:`) || !strings.HasSuffix(got, ".docx") {
		t.Fatalf("filename not sanitized: %q", got)
	}
	if got := SrcFilename("   ", ".md"); got != "漏洞报告.md" {
		t.Fatalf("empty title fallback: %q", got)
	}
}
