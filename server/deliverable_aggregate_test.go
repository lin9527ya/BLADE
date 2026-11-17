package server

import (
	"strings"
	"testing"
)

func TestBuildAggregateMarkdown(t *testing.T) {
	md := buildAggregateMarkdown("Acme 交付", "company", "Acme", []aggregateTask{
		{ID: 1, Name: "外部评估", Status: "done", Report: "# Task report\n\nfinding"},
		{ID: 2, Name: "复测", Status: "failed", Error: "报告读取失败"},
	})
	for _, want := range []string{"# Acme 交付", "范围：公司 · Acme", "任务数：2", "| #1 外部评估 | done | 已收录 |", "# Task report", "> 报告读取失败：报告读取失败"} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q:\n%s", want, md)
		}
	}
}
