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

	if _, err := d.UpdateFindingCaseReport(c.ID, "# 统一报告", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateFindingCaseReport(c.ID, "# 新报告", 999); err == nil || !strings.Contains(err.Error(), "版本冲突") {
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
