package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 进程重启后 Planner 对象是新的，TodoWrite 清单与上一轮结论必须从 DB 恢复；
// 同一对象内只恢复一次，模型主动清空清单后不会再被旧状态回填。
func TestPlannerRestoresPersistentTodoAndConclusion(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("planner state", "goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)

	p1 := NewPlanner(nil, "", t.TempDir(), nil, 0, 1)
	store1 := p1.restorePlannerState(context.Background(), ts, task.ID, task.ExplorationID)
	payload, _ := json.Marshal(map[string]any{"todos": []map[string]string{
		{"content": "确认登录后台上传点", "status": "in_progress"},
		{"content": "等 worker 产出会话", "status": "pending"},
	}})
	if _, err := store1.Tool().Call(context.Background(), payload, nil); err != nil {
		t.Fatal(err)
	}
	p1.lastRoundNote = "上一轮：登录口可用，等待上传点验证"
	p1.savePlannerState(ts, task.ID, task.ExplorationID, store1)

	p2 := NewPlanner(nil, "", t.TempDir(), nil, 0, 1)
	store2 := p2.restorePlannerState(context.Background(), ts, task.ID, task.ExplorationID)
	got := store2.List()
	if len(got) != 2 || got[0].Content != "确认登录后台上传点" || got[0].Status != "in_progress" {
		t.Fatalf("restored todos=%+v", got)
	}
	if p2.lastRoundNote != "上一轮：登录口可用，等待上传点验证" {
		t.Fatalf("restored note=%q", p2.lastRoundNote)
	}

	// 同一对象再调用 restore 不应覆盖模型在本进程内的更新。
	payload, _ = json.Marshal(map[string]any{"todos": []map[string]string{}})
	if _, err := store2.Tool().Call(context.Background(), payload, nil); err != nil {
		t.Fatal(err)
	}
	p2.savePlannerState(ts, task.ID, task.ExplorationID, store2)
	if again := p2.restorePlannerState(context.Background(), ts, task.ID, task.ExplorationID); len(again.List()) != 0 {
		t.Fatalf("same-process restore must not refill: %+v", again.List())
	}
}

// 当 restorePlannerState 因瞬态数据库故障读失败时，todoLoaded 保持 false；
// 此时回合结束的 savePlannerState 不得用空清单/空结论覆盖数据库中已有状态。
func TestPlannerSaveSkipsWhenInitialRestoreFailed(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	brokenDB := testDB(t)
	task, err := d.CreateTaskWithOptions("planner preserve", "goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	tsGood := d.Exploration(task.ExplorationID)
	tsBroken := brokenDB.Exploration(task.ExplorationID)

	wantTodos := []db.PlannerTodoItem{{Content: "重要已持久化待办", Status: "in_progress"}}
	if err := tsGood.SavePlannerState(task.ID, wantTodos, "已持久化结论"); err != nil {
		t.Fatal(err)
	}

	// 关闭 brokenDB 模拟读取阶段瞬态数据库连接错误。
	_ = brokenDB.Close()

	p := NewPlanner(nil, "", t.TempDir(), nil, 0, 1)
	store := p.restorePlannerState(context.Background(), tsBroken, task.ID, task.ExplorationID)
	p.savePlannerState(tsGood, task.ID, task.ExplorationID, store)

	state, found, err := tsGood.LoadPlannerState(task.ID)
	if err != nil || !found {
		t.Fatalf("LoadPlannerState err=%v found=%v", err, found)
	}
	if len(state.Todos) != 1 || state.Todos[0].Content != "重要已持久化待办" || state.LastRoundNote != "已持久化结论" {
		t.Fatalf("persisted planner state was overwritten: %+v", state)
	}
}
