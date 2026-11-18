package db

import "testing"

func TestTaskLootExecOptInCreateAndUpdate(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("loot exec opt-in task", "fixture", TaskCreateOptions{LootExecEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTaskCascade(task.ID, false, false) //nolint:errcheck
	if !task.LootExecEnabled || !d.TaskLootExecEnabled(task.ID) {
		t.Fatal("task loot exec should be enabled at creation")
	}
	disabled := false
	updated, err := d.UpdateTask(task.ID, TaskPatch{LootExecEnabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.LootExecEnabled || d.TaskLootExecEnabled(task.ID) {
		t.Fatalf("task loot exec should be disabled: %+v", updated)
	}
}
