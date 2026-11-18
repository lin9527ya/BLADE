package db

import "testing"

func TestTaskExplorationModeCreateAndUpdate(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("divergent mode task", "fixture", TaskCreateOptions{ExplorationMode: ExplorationModeDivergent})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTaskCascade(task.ID, false, false) //nolint:errcheck
	if task.ExplorationMode != ExplorationModeDivergent {
		t.Fatalf("created mode = %q", task.ExplorationMode)
	}
	mode := ExplorationModeBalanced
	updated, err := d.UpdateTask(task.ID, TaskPatch{ExplorationMode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.ExplorationMode != ExplorationModeBalanced {
		t.Fatalf("updated mode = %+v", updated)
	}
	got, err := d.GetTask(task.ID)
	if err != nil || got == nil || got.ExplorationMode != ExplorationModeBalanced {
		t.Fatalf("reloaded mode = %+v, err=%v", got, err)
	}
}
