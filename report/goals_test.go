package report

import (
	"strings"
	"testing"
	"time"
)

func TestMarkdownGoalsSection(t *testing.T) {
	md := Markdown(Input{
		Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0),
		Goals: []GoalNote{
			{Text: "拿到后台权限", State: "met", VulnClass: "authz"},
			{Text: "读取指定数据", State: "open"},
			{Text: "清理遗留物", State: "abandoned"},
		},
	})
	for _, want := range []string{
		"## 目标达成情况",
		"**1/3** 已达成",
		"**1** 个已放弃",
		"1. [已达成] 拿到后台权限（authz）",
		"2. [未达成] 读取指定数据",
		"3. [已放弃] 清理遗留物",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("report missing %q:\n%s", want, md)
		}
	}
}
