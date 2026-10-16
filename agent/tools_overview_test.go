package agent

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lin9527ya/blade/db"
)

// TestGraphOverviewReportsTrueFindingTotal pins that findings_total is a real
// COUNT rather than len() of the fetched list. The display list is fetched with a
// small LIMIT (findingListCap), so a length-derived total starts reporting that cap
// the moment a task passes it — and findings_total is what the planner reads when
// judging whether the goal is met, i.e. a truncated number silently understates
// the work already done. 25 findings against a window of 10 catches the
// regression without needing the old 1000-row cap to be exceeded.
func TestGraphOverviewReportsTrueFindingTotal(t *testing.T) {
	d := testDB(t)
	defer d.Close()

	task, err := d.CreateTaskWithOptions("overview totals", "count the findings", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	const want = 25
	for i := 0; i < want; i++ {
		if _, err := ts.AddNode(db.KindFinding, map[string]any{
			"summary": fmt.Sprintf("finding %d", i),
		}, 50, "confirmed", "worker", nil); err != nil {
			t.Fatal(err)
		}
	}

	tools := NewToolSet(ts, "planner")
	tools.SetTaskID(task.ID)
	out := tools.graphOverviewData()

	got, ok := out["findings_total"].(int)
	if !ok {
		t.Fatalf("findings_total missing or not an int: %#v", out["findings_total"])
	}
	if got != want {
		t.Fatalf("findings_total=%d want=%d — 总数被展示窗口截断了", got, want)
	}
	// 展示窗口必须短于总数，否则本用例的前提（窗口截断 vs 真实计数）不成立，
	// 也就测不出「用 len(列表) 当总数」这个退化。
	list, _ := out["finding_list"].([]map[string]any)
	if len(list) >= want {
		t.Fatalf("finding_list len=%d 未短于总数 %d——用例前提被破坏", len(list), want)
	}
}

func TestOverviewTextBudgetIsFairAndUTF8Safe(t *testing.T) {
	if got := relatedOverviewBudgetForSources(1); got != relatedOverviewMaxTextPerSource {
		t.Fatalf("single source budget=%d want=%d", got, relatedOverviewMaxTextPerSource)
	}
	perSource := relatedOverviewBudgetForSources(db.MaxTaskSourceCount)
	if perSource*db.MaxTaskSourceCount > relatedOverviewTotalTextRunes {
		t.Fatalf("aggregate budget exceeded: per_source=%d", perSource)
	}

	budget := overviewTextBudget{remaining: 5}
	got := budget.take(strings.Repeat("中", 10), 20)
	if !utf8.ValidString(got) {
		t.Fatalf("budget truncation produced invalid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) != 5 || !budget.truncated || budget.remaining != 0 {
		t.Fatalf("unexpected truncation: got=%q budget=%+v", got, budget)
	}
	if tail := budget.take("more", 20); tail != "" {
		t.Fatalf("exhausted budget returned more text: %q", tail)
	}
}
