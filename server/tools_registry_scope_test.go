package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

func TestToolRegistryIsRunScoped(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Fatal(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	old := agent.CurrentOverrides()
	t.Cleanup(func() { agent.SetOverrides(old) })
	wireTools(pg, buildDomainReg(pg.Assets()))
	var tasks []*db.Task
	initial := map[int64]bool{}
	for i := 0; i < 2; i++ {
		task, err := pg.CreateTask(fmt.Sprintf("registry-scope-%d", i), "fixture", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
		facts, err := pg.Exploration(task.ExplorationID).ListByKind(db.KindFact, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range facts {
			initial[f.ID] = true
		}
		t.Cleanup(func() { _ = pg.DeleteTask(task.ID) })
	}
	rf, err := pg.GetTool("record_fact")
	if err != nil || rf == nil {
		t.Fatalf("record_fact: %v", err)
	}
	t.Cleanup(func() { _ = pg.UpdateTool(rf.Key, rf.Description, rf.Schema, mustJSON(rf.Agents), rf.Enabled) })
	if err := pg.UpdateTool(rf.Key, rf.Description, rf.Schema, mustJSON([]string{"worker"}), true); err != nil {
		t.Fatal(err)
	}
	resolve := agent.ToolResolve
	ctxFor := func(task *db.Task) context.Context {
		return agent.WithRunInfo(t.Context(), agent.RunInfo{TaskID: task.ID, ExplorationID: task.ExplorationID})
	}
	// A task-bound resolution must not change the later task-less baseline.
	_ = resolve(ctxFor(tasks[0]), "worker", nil)
	tool := names(resolve(t.Context(), "worker", nil))["record_fact"]
	if tool == nil {
		t.Fatal("missing task-less record_fact")
	}
	res, err := tool.Call(t.Context(), json.RawMessage(`{"summary":"must not write A"}`), nil)
	if err != nil || !res.IsError {
		t.Fatalf("task-less tool must reject: %+v %v", res, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for _, task := range tasks {
		for i := 0; i < 8; i++ {
			wg.Go(func() {
				ctx := ctxFor(task)
				tool := names(resolve(ctx, "worker", nil))["record_fact"]
				if tool == nil {
					errs <- fmt.Errorf("task %d missing tool", task.ID)
					return
				}
				raw, _ := json.Marshal(map[string]string{"summary": fmt.Sprintf("task-%d", task.ID)})
				res, err := tool.Call(ctx, raw, nil)
				if err != nil || res.IsError {
					errs <- fmt.Errorf("task %d: %+v %v", task.ID, res, err)
				}
			})
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for _, task := range tasks {
		facts, err := pg.Exploration(task.ExplorationID).ListByKind(db.KindFact, 100)
		if err != nil {
			t.Fatal(err)
		}
		added := 0
		for _, fact := range facts {
			if initial[fact.ID] {
				continue
			}
			added++
			var payload struct {
				Summary string `json:"summary"`
			}
			if err := json.Unmarshal(fact.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Summary != fmt.Sprintf("task-%d", task.ID) {
				t.Fatalf("cross-task write: %s", fact.Payload)
			}
		}
		if added != 8 {
			t.Fatalf("task %d added facts=%d", task.ID, added)
		}
	}
}
