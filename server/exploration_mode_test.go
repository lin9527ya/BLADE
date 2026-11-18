package server

import "testing"

func TestDivergentModeSkipsAutoCoverageGoal(t *testing.T) {
	task := &Task{Goal: "全面测试目标所属资产", Description: "全资产覆盖"}
	task.ExplorationMode = "divergent"
	if shouldAutoAddCoverageGoal(task, nil) {
		t.Fatal("divergent task must not get an auto coverage goal")
	}
	task.ExplorationMode = "balanced"
	if !shouldAutoAddCoverageGoal(task, nil) {
		t.Fatal("balanced task should keep the coverage goal behavior")
	}
}
