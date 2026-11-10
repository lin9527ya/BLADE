package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

func TestUnifiedFindingReportReporterTools(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	task, err := pg.CreateTask("reporter case tools", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteTask(task.ID) })
	var ids []int64
	for _, summary := range []string{"登录点注入", "后台注入", "搜索注入", "接口注入"} {
		id, err := pg.AddFinding(task.ID, 0, "sqli", "SQL 注入", "high", summary, "proof "+summary, "w", []int64{7})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}}
	tools := s.findingCaseTools()
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: task.ID})

	found := workflowCall(t, ctx, workflowTool(t, tools, "search_finding_duplicates"), map[string]any{"finding_id": ids[0]}, false)
	if !strings.Contains(found, "评分只是线索") {
		t.Fatalf("duplicate search missing guidance: %s", found)
	}
	merged := workflowCall(t, ctx, workflowTool(t, tools, "merge_finding_records"), map[string]any{
		"task_id": task.ID, "finding_ids": ids[:2], "title": "同根因 SQL 注入", "reason": "同一处理器同一参数",
	}, false)
	var mergeOut struct {
		CaseID  string `json:"case_id"`
		Version int64  `json:"version"`
	}
	if err := json.Unmarshal([]byte(merged), &mergeOut); err != nil || mergeOut.CaseID == "" {
		t.Fatalf("merge result: %s %v", merged, err)
	}
	var caseID int64
	_, _ = fmt.Sscan(mergeOut.CaseID, &caseID)
	read := workflowCall(t, ctx, workflowTool(t, tools, "get_finding_case"), map[string]any{"case_id": caseID}, false)
	var readOut struct {
		Case struct {
			Version int64 `json:"version"`
		} `json:"case"`
		Members []map[string]any `json:"members"`
	}
	if err := json.Unmarshal([]byte(read), &readOut); err != nil || len(readOut.Members) != 2 {
		t.Fatalf("case read: %s %v", read, err)
	}
	// Truncated report is refused before touching the database.
	workflowCall(t, ctx, workflowTool(t, tools, "update_finding_case_report"), map[string]any{
		"case_id": caseID, "version": readOut.Case.Version, "title": "统一", "report": "# 概述\n半篇", "severity": "high", "severity_reason": "已验证",
	}, true)
	full := "# 概述\n同根因注入\n\n## 影响\n可读库\n\n## 前置条件\n登录\n\n## 复现步骤\n1. 注入\n\n## 证据\n回显\n\n## PoC\n```\npayload\n```\n\n## 根因分析\n拼接\n\n## 修复建议\n参数化"
	workflowCall(t, ctx, workflowTool(t, tools, "update_finding_case_report"), map[string]any{
		"case_id": caseID, "version": readOut.Case.Version, "title": "统一 SQL 注入", "report": full, "severity": "high", "severity_reason": "已实测读库",
	}, false)
	if _, err := pg.Exec(`UPDATE findings SET summary='changed' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	workflowCall(t, ctx, workflowTool(t, tools, "update_finding_case_report"), map[string]any{
		"case_id": caseID, "version": readOut.Case.Version, "title": "统一", "report": full, "severity": "high", "severity_reason": "旧版本",
	}, true)
	// A reporter may propose a group, never silently merge it.
	suggested := workflowCall(t, ctx, workflowTool(t, tools, "suggest_finding_merge"), map[string]any{
		"task_id": task.ID, "finding_ids": ids[2:], "title": "疑似同根因", "reason": "同参数但缺回显证据",
	}, false)
	if !strings.Contains(suggested, "suggestion_id") {
		t.Fatalf("suggestion result: %s", suggested)
	}
	// Cross-task access is refused even with a plausible id.
	other, err := pg.CreateTask("other", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteTask(other.ID) })
	otherCtx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: other.ID})
	workflowCall(t, otherCtx, workflowTool(t, tools, "get_finding_case"), map[string]any{"case_id": caseID}, true)
}

func TestFindingCaseToolsSeedReporterBinding(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}}
	s.seedFindingCaseTools()
	var agents string
	if err := pg.QueryRow(`SELECT agents::text FROM tools WHERE key='get_finding_case' AND system`).Scan(&agents); err != nil {
		t.Fatalf("case tool not seeded: %v", err)
	}
	if !strings.Contains(agents, "reporter") {
		t.Fatalf("case tool not bound to reporter: %s", agents)
	}
}
