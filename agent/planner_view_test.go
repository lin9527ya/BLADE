package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 态势里的 recent_facts 只是最新一窗：planner 需要知道「还有更旧的」以及从哪接着翻。
// 没有游标时它只能自己猜 id——或者以为这就是全部。
func TestGraphOverviewGivesFactCursor(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions("cursor task", "把面上的点都过一遍", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	tools := NewToolSet(ts, "planner")
	tools.SetTaskID(task.ID)

	addFacts := func(n int) []int64 {
		ids := make([]int64, 0, n)
		for i := 0; i < n; i++ {
			id, err := ts.AddNode(db.KindFact, map[string]any{"summary": fmt.Sprintf("fact %d", i)}, 50, "confirmed", "worker", nil)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		return ids
	}

	// 窗口内：不撒谎地给出「这就是全部」（不给游标）。
	addFacts(3)
	out := tools.graphOverviewData()
	baseTotal, _ := out["facts"].(int)
	if _, ok := out["recent_facts_next_before"]; ok {
		t.Fatalf("事实没超过窗口时不该给续翻游标：%v", out["recent_facts_next_before"])
	}

	// 超出窗口：游标必须指向**这一窗最后一条**，否则翻页会重复或跳条。
	addFacts(recentFactsCap + 2)
	out = tools.graphOverviewData()
	shown, _ := out["recent_facts"].([]map[string]any)
	if len(shown) != recentFactsCap {
		t.Fatalf("recent_facts 窗口应是 %d 条，实际 %d", recentFactsCap, len(shown))
	}
	cursor, ok := out["recent_facts_next_before"].(int64)
	if !ok {
		t.Fatalf("超过窗口时必须给续翻游标：%#v", out["recent_facts_next_before"])
	}
	if last, _ := shown[len(shown)-1]["id"].(int64); cursor != last {
		t.Fatalf("游标应等于这一窗最后一条的 id：cursor=%d last=%d", cursor, last)
	}
	// facts 是真 COUNT：不能是窗口长度（那正是这次要修的那类"截断数字骗人"）。
	if total, _ := out["facts"].(int); total != baseTotal+recentFactsCap+2 {
		t.Fatalf("facts 应是真总数而不是窗口长度：got=%v want=%d", out["facts"], baseTotal+recentFactsCap+2)
	}
	if note, _ := out["recent_facts_note"].(string); note == "" {
		t.Fatal("给了游标就该说清它怎么用")
	}
}

// get_worker_output 只回最终结论时，planner 看不出这条 work 到底做到哪、是不是真跑过。
// 补上最后 3 步与后台执行台账。
func TestGetWorkerOutputAddsStepsAndExecutions(t *testing.T) {
	tools, ts, intentID := newToolWorker(t)
	node := intentID
	for i := 0; i < 5; i++ {
		if _, err := ts.AppendActivity(db.Activity{
			NodeID: &node, Worker: "work#1", Kind: "text", Tool: "Bash",
			Summary: fmt.Sprintf("step %d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ts.AppendActivity(db.Activity{
		NodeID: &node, Kind: "result", Summary: "跑完了", Detail: "结论：上传点可用，能落地 webshell",
	}); err != nil {
		t.Fatal(err)
	}

	exit := 0
	tools.SetExecService(&fakeExecService{ledger: []*db.ToolExecution{{
		ID: 7, Status: db.ExecCompleted, Command: "nuclei -l urls.txt -o out.txt",
		ExitCode: &exit, OutputBytes: 2048, OutputPath: "/data/tasks/1/executions/7.log",
	}}})

	raw := callTool(t, tools.PlannerTools(), "get_worker_output", fmt.Sprintf(`{"intent_id":%d}`, intentID))
	var res map[string]any
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("返回不是 JSON：%v\n%s", err, raw)
	}
	if got, _ := res["final_text"].(string); got != "结论：上传点可用，能落地 webshell" {
		t.Fatalf("final_text 应取 result 的完整内容：%q", got)
	}

	steps, _ := res["recent_steps"].([]any)
	if len(steps) != 3 {
		t.Fatalf("recent_steps 应只给最后 3 步：%d", len(steps))
	}
	lastStep, _ := steps[len(steps)-1].(map[string]any)
	if lastStep["kind"] != "result" {
		t.Fatalf("最后一步应是 result：%#v", lastStep)
	}

	execs, _ := res["executions"].([]any)
	if len(execs) != 1 {
		t.Fatalf("应带上执行台账：%#v", res["executions"])
	}
	exec, _ := execs[0].(map[string]any)
	if exec["execution_id"] != float64(7) || exec["command"] != "nuclei -l urls.txt -o out.txt" {
		t.Fatalf("台账行不对：%#v", exec)
	}
	if exec["exit_code"] != float64(0) || exec["output_path"] != "/data/tasks/1/executions/7.log" {
		t.Fatalf("台账行缺退出码/输出路径：%#v", exec)
	}
}

// 没接线执行服务时不该凭空出现 executions 键（否则模型会以为"没有执行记录"）。
func TestGetWorkerOutputOmitsExecutionsWithoutService(t *testing.T) {
	tools, ts, intentID := newToolWorker(t)
	node := intentID
	if _, err := ts.AppendActivity(db.Activity{NodeID: &node, Kind: "result", Summary: "ok", Detail: "ok"}); err != nil {
		t.Fatal(err)
	}
	raw := callTool(t, tools.PlannerTools(), "get_worker_output", fmt.Sprintf(`{"intent_id":%d}`, intentID))
	var res map[string]any
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("返回不是 JSON：%v\n%s", err, raw)
	}
	if _, ok := res["executions"]; ok {
		t.Fatalf("未接线时不该给 executions：%#v", res["executions"])
	}
}
