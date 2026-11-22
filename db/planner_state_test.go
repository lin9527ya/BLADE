package db

import "testing"

func TestPlannerStateRoundTripIsTaskScoped(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("planner state", "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	other, err := d.CreateTaskWithOptions("planner state other", "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	es := d.Exploration(task.ExplorationID)

	if _, found, err := es.LoadPlannerState(task.ID); err != nil || found {
		t.Fatalf("empty state: found=%v err=%v", found, err)
	}
	todos := []PlannerTodoItem{
		{Content: "确认 /login 的认证绕过", Status: "in_progress"},
		{Content: "拿到后台后验证文件上传", Status: "pending"},
	}
	if err := es.SavePlannerState(task.ID, todos, "上一轮：发现登录口，等待会话"); err != nil {
		t.Fatal(err)
	}
	state, found, err := es.LoadPlannerState(task.ID)
	if err != nil || !found {
		t.Fatalf("load: found=%v err=%v", found, err)
	}
	if len(state.Todos) != 2 || state.Todos[0].Content != todos[0].Content || state.Todos[1].Status != "pending" {
		t.Fatalf("todos=%+v", state.Todos)
	}
	if state.LastRoundNote != "上一轮：发现登录口，等待会话" {
		t.Fatalf("note=%q", state.LastRoundNote)
	}
	// 同 task id 不能借另一个 exploration 的 store 读取或写入。
	otherStore := d.Exploration(other.ExplorationID)
	if _, found, err := otherStore.LoadPlannerState(task.ID); err != nil || found {
		t.Fatalf("cross-exploration load must be empty: found=%v err=%v", found, err)
	}
	if err := otherStore.SavePlannerState(task.ID, []PlannerTodoItem{{Content: "越权", Status: "pending"}}, "x"); err != nil {
		t.Fatalf("save guard should be a no-op, got %v", err)
	}
	state, _, _ = es.LoadPlannerState(task.ID)
	if len(state.Todos) != 2 || state.Todos[0].Content != todos[0].Content {
		t.Fatalf("cross-exploration save changed state: %+v", state.Todos)
	}
	if err := es.SavePlannerState(task.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	state, _, _ = es.LoadPlannerState(task.ID)
	if state.Todos == nil || len(state.Todos) != 0 || state.LastRoundNote != "" {
		t.Fatalf("cleared state=%+v", state)
	}
}
