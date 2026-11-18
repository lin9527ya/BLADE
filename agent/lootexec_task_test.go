package agent

import (
	"context"
	"testing"
)

func TestLootExecTaskOverride(t *testing.T) {
	prevGlobal := LootExecEnabled
	prevTask := LootExecTaskEnabled
	t.Cleanup(func() {
		LootExecEnabled = prevGlobal
		LootExecTaskEnabled = prevTask
	})
	LootExecEnabled = func() bool { return false }
	LootExecTaskEnabled = func(taskID int64) bool { return taskID == 42 }

	if lootExecOn(context.Background()) {
		t.Fatal("no run info must not enable task loot exec")
	}
	if !lootExecOn(WithRunInfo(context.Background(), RunInfo{TaskID: 42})) {
		t.Fatal("task 42 opt-in should enable loot exec")
	}
	if lootExecOn(WithRunInfo(context.Background(), RunInfo{TaskID: 7})) {
		t.Fatal("unrelated task must not be enabled")
	}
	LootExecEnabled = func() bool { return true }
	if !lootExecOn(context.Background()) {
		t.Fatal("global switch should still enable loot exec")
	}
}
