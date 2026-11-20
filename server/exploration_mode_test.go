package server

import "testing"

func TestAutoCoverageGoalOnlyWhenRequested(t *testing.T) {
	depthTask := &Task{Goal: "获取管理员权限", Description: "对 http://t.example 做深度验证"}
	if shouldAutoAddCoverageGoal(depthTask, nil) {
		t.Fatal("depth-focused task must not get an auto coverage goal")
	}
	broadTask := &Task{Goal: "全面测试目标所属资产", Description: "全资产覆盖"}
	if !shouldAutoAddCoverageGoal(broadTask, nil) {
		t.Fatal("explicit broad coverage task should auto-add coverage goal")
	}
}
