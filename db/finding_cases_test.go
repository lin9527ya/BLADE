package db

import (
	"strings"
	"testing"
)

func TestFindingCaseLifecycleAndStats(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTask("case test", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	tid := task.ID

	f1, err := d.AddFinding(tid, 0, "sqli", "SQL 注入", "high", "登录处可注入", "evidence", "w", []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := d.AddFinding(tid, 0, "sqli", "SQL 注入（后台）", "high", "后台可注入", "evidence", "w", []int64{2})
	if err != nil {
		t.Fatal(err)
	}
	f3, err := d.AddFinding(tid, 0, "xss", "反射型 XSS", "medium", "搜索页反射", "evidence", "w", []int64{3})
	if err != nil {
		t.Fatal(err)
	}
	f4, err := d.AddFinding(tid, 0, "sqli", "SQL 注入（搜索）", "high", "搜索参数可注入", "evidence", "w", []int64{2})
	if err != nil {
		t.Fatal(err)
	}

	c, err := d.CreateFindingCase(tid, tid, "登录与后台 SQL 注入", "同根因", "high", []int64{f1, f2}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if c.MemberCount != 2 || len(c.Members) != 2 {
		t.Fatalf("案例成员数不对：%+v", c)
	}
	st, err := d.FindingCaseStats(tid)
	if err != nil {
		t.Fatal(err)
	}
	if st.Cases != 1 || st.Ungrouped != 2 || st.Distinct != 3 || st.Reports != 4 {
		t.Fatalf("统计口径不对：%+v", st)
	}

	cands, err := d.FindingCaseCandidates(tid, f1, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cand := range cands {
		if cand.Finding.ID == f4 && cand.Score >= 4 && len(cand.Reasons) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("f4 应作为高分候选：%+v", cands)
	}

	// 已在其它案例里的发现不能再被归并。
	if _, err := d.CreateFindingCase(tid, tid, "重复归并", "", "high", []int64{f1}, "user"); err == nil {
		t.Fatal("已归并的发现应被拒绝")
	}

	if _, err := d.UpdateFindingCaseReport(t.Context(), c.ID, 0, "", "# 统一报告", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateFindingCaseReport(t.Context(), c.ID, 999, "", "# 新报告", "", ""); err == nil || !strings.Contains(err.Error(), "版本冲突") {
		t.Fatalf("过期版本应冲突：%v", err)
	}

	if added, err := d.AddFindingCaseMembers(c.ID, []int64{f3}, "补充", "user"); err != nil || added != 1 {
		t.Fatalf("加成员失败：%v added=%d", err, added)
	}
	if st, _ := d.FindingCaseStats(tid); st.Ungrouped != 1 || st.Distinct != 2 {
		t.Fatalf("加成员后统计不对：%+v", st)
	}
	if err := d.RemoveFindingCaseMember(c.ID, f1, "拆分", "user"); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveFindingCaseMember(c.ID, f2, "拆分", "user"); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveFindingCaseMember(c.ID, f3, "拆分", "user"); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetFindingCase(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active {
		t.Fatal("成员清空后案例应自动关闭")
	}
}

func TestFindingCaseSuggestionsAndBlocks(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTask("case suggest", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	tid := task.ID
	f1, _ := d.AddFinding(tid, 0, "sqli", "SQL 注入 A", "high", "登录处", "e", "w", []int64{1})
	if _, err := d.AddFinding(tid, 0, "sqli", "SQL 注入 B", "high", "后台处", "e", "w", []int64{1}); err != nil {
		t.Fatal(err)
	}
	f3, _ := d.AddFinding(tid, 0, "sqli", "SQL 注入 C", "high", "搜索处", "e", "w", []int64{1})

	created, err := d.GenerateFindingCaseSuggestions(tid)
	if err != nil || created < 2 {
		t.Fatalf("应生成至少 2 条建议：created=%d err=%v", created, err)
	}
	items, err := d.ListFindingCaseSuggestions(tid, "pending", 10)
	if err != nil || len(items) == 0 {
		t.Fatalf("建议列表为空：%v", err)
	}
	c, err := d.AcceptFindingCaseSuggestion(items[0].ID, "user")
	if err != nil || c.MemberCount != 2 {
		t.Fatalf("采纳建议失败：%v %+v", err, c)
	}
	// 人工否决一对：候选与后续建议都不再提。
	if err := d.BlockFindingCasePair(tid, f1, f3, "不同入口"); err != nil {
		t.Fatal(err)
	}
	cands, err := d.FindingCaseCandidates(tid, f1, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, cand := range cands {
		if cand.Finding.ID == f3 {
			t.Fatalf("已否决的对应从候选中排除：%+v", cand)
		}
	}
}

func TestUnifiedFindingReportTracksEvidenceVersion(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTask("unified report", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	f1, err := d.AddFinding(task.ID, 0, "sqli", "SQL 注入", "high", "登录处可注入", "proof", "w", []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := d.AddFinding(task.ID, 0, "sqli", "后台 SQL 注入", "high", "后台可注入", "proof2", "w", []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	c, err := d.CreateFindingCase(task.ID, task.ID, "同根因 SQL 注入", "同入口同参数", "high", []int64{f1, f2}, "user")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := d.UpdateFindingCaseReport(t.Context(), c.ID, c.Version, "统一 SQL 注入", "# 概述\n完整报告", "high", "已验证可注入")
	if err != nil {
		t.Fatal(err)
	}
	if saved.ReportVersion != c.Version || saved.Version != c.Version {
		t.Fatalf("saved report must pin read version: saved=%+v read=%d", saved, c.Version)
	}
	// Any member edit bumps the case version; the saved report is then stale and
	// cannot be overwritten with the old version.
	if _, err := d.Exec(`UPDATE findings SET summary='changed evidence' WHERE id=$1`, f1); err != nil {
		t.Fatal(err)
	}
	after, err := d.GetFindingCase(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != c.Version+1 || after.ReportVersion == after.Version {
		t.Fatalf("member edit did not stale the report: %+v", after)
	}
	if _, err := d.UpdateFindingCaseReport(t.Context(), c.ID, c.Version, "旧版本", "# 概述\n旧", "high", "旧"); err == nil || !strings.Contains(err.Error(), "版本冲突") {
		t.Fatalf("stale version accepted: %v", err)
	}
	fresh, err := d.UpdateFindingCaseReport(t.Context(), c.ID, after.Version, "统一 SQL 注入", "# 概述\n新报告", "high", "复测依据")
	if err != nil || fresh.ReportVersion != after.Version {
		t.Fatalf("fresh report: %+v %v", fresh, err)
	}
	// Removing a member also invalidates the unified report.
	if err := d.RemoveFindingCaseMember(c.ID, f2, "拆分", "user"); err != nil {
		t.Fatal(err)
	}
	afterRemove, err := d.GetFindingCase(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRemove.ReportVersion == afterRemove.Version {
		t.Fatalf("member removal did not stale the report: %+v", afterRemove)
	}
	if _, err := d.UpdateFindingCaseReport(t.Context(), c.ID, afterRemove.Version, "", "", "", ""); err == nil {
		t.Fatal("empty unified report accepted")
	}
}

func TestMergeFindingCaseUnionsGroupsAndRespectsVeto(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTask("merge cases", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	var ids []int64
	for i := 0; i < 4; i++ {
		id, err := d.AddFinding(task.ID, 0, "sqli", "SQL 注入", "high", "summary", "proof", "w", []int64{1})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	a, err := d.CreateFindingCase(task.ID, task.ID, "案例 A", "同根因", "high", ids[:2], "user")
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.CreateFindingCase(task.ID, task.ID, "案例 B", "同根因", "high", ids[2:], "user")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := d.MergeFindingCase(t.Context(), task.ID, ids, "统一案例", "Reporter 核对同一处理器", "reporter")
	if err != nil {
		t.Fatal(err)
	}
	if merged.ID != a.ID || merged.MemberCount != 4 || merged.Version <= a.Version {
		t.Fatalf("groups not unioned into one case: %+v", merged)
	}
	other, err := d.GetFindingCase(b.ID)
	if err != nil || other.Active {
		t.Fatalf("losing case must be inactive: %+v %v", other, err)
	}
	if _, err := d.SuggestFindingCase(t.Context(), task.ID, ids[:2], "重复建议", "already grouped"); err == nil {
		t.Fatal("suggestion for grouped records accepted")
	}
	// A human veto blocks both merges and new suggestions.
	if err := d.BlockFindingCasePair(task.ID, ids[0], ids[1], "人工否决"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MergeFindingCase(t.Context(), task.ID, ids[:2], "再次归并", "veto", "reporter"); err == nil {
		t.Fatal("vetoed pair merged")
	}
}
