package server

import (
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestUnifiedFindingCaseExport(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	task, err := pg.CreateTask("unified export", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteTask(task.ID) })
	var ids []int64
	for _, summary := range []string{"登录点注入", "后台注入", "搜索注入"} {
		id, err := pg.AddFinding(task.ID, 0, "sqli", "SQL 注入", "high", summary, "proof "+summary, "w", []int64{1})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	c, err := pg.CreateFindingCase(task.ID, task.ID, "同根因 SQL 注入", "同一处理器同一参数", "high", ids[:2], "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.UpdateFindingCaseReport(t.Context(), c.ID, c.Version, "统一 SQL 注入", "# 概述\n完整统一报告", "high", "已验证影响"); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg, dir: t.TempDir()}}
	fs, err := pg.ListFindingsForExport(db.FindingFilter{}, []int64{ids[0]})
	if err != nil || len(fs) != 1 {
		t.Fatalf("selected member: %d %v", len(fs), err)
	}
	expanded, plan, err := s.prepareFindingCaseExport(fs)
	if err != nil || len(expanded) != 2 {
		t.Fatalf("selected one member must expand whole case: %d %v", len(expanded), err)
	}
	out, err := s.consolidateFindingCaseExport(expanded, plan, false)
	if err != nil || len(out) != 1 || out[0].ExportCaseID != c.ID || out[0].Report != "# 概述\n完整统一报告" || !strings.Contains(out[0].Summary, "原始记录") {
		t.Fatalf("unified export: %+v %v", out, err)
	}
	out, err = s.consolidateFindingCaseExport(expanded, plan, true)
	if err != nil || len(out) != 3 {
		t.Fatalf("originals option lost records: %d %v", len(out), err)
	}
	// Evidence changes after planning must surface as a stale export instead of
	// presenting the old unified report as current.
	if _, err := pg.Exec(`UPDATE findings SET severity='critical' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	out, err = s.consolidateFindingCaseExport(expanded, plan, false)
	if err != nil || !strings.Contains(out[0].Report, "已过期") || !strings.Contains(out[0].Evidence, "proof 登录点注入") {
		t.Fatalf("stale export omitted notice or member evidence: %+v %v", out, err)
	}
}
