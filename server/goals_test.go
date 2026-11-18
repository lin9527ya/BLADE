package server

import (
	"context"
	"strings"
	"testing"
)

func TestCreateGoalsNilTask(t *testing.T) {
	if got := (&Server{}).createGoals(context.Background(), nil, nil); len(got) != 0 {
		t.Fatalf("nil task produced goals: %+v", got)
	}
}

func TestBroadCoverageGoalDetectionAndText(t *testing.T) {
	if !broadCoverageRequested("目标：对所属资产做全面测试，覆盖所有子域") {
		t.Fatal("全面测试/所有子域应识别为覆盖任务")
	}
	if broadCoverageRequested("对 https://example.com 拿后台权限") {
		t.Fatal("普通拿权限任务不应被强行加覆盖目标")
	}
	if got := coverageGoalText("全面测试，覆盖度达到 80%"); !strings.Contains(got, "80%") {
		t.Fatalf("应沿用显式覆盖度：%s", got)
	}
	if got := coverageGoalText("全面测试所有资产"); !strings.Contains(got, "100%") {
		t.Fatalf("未写百分比时默认 100%%：%s", got)
	}
	if !hasCoverageGoal([]goalSpec{{Text: "范围内资金覆盖度达到 90%"}}) {
		t.Fatal("已有覆盖目标不应重复追加")
	}
}
