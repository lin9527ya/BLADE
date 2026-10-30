package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/transcript"
)

// 默认：每轮新会话，只承接「上一轮结论摘要」——不再把全量 transcript 反复喂回去
// （任务 14 实测 576KB / 26 万 token，换来的主要是噪声）。
func TestPlannerDefaultsToConclusionSummary(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("planner summary", "把面上的点都过一遍", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	prov := &recordingProvider{text: "第一轮结论：api.example 的 /admin 登录口值得深挖"}
	tx := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
	p := NewPlanner(prov, "test-model", t.TempDir(), tx, 0, 4)
	p.SetNonStreaming(func() bool { return true })

	for round := 1; round <= 2; round++ {
		if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "拿下目标", nil, nil); err != nil {
			t.Fatalf("第 %d 轮 Plan: %v", round, err)
		}
	}
	var summary, authoritative bool
	for _, m := range prov.last().Messages {
		text := m.Text()
		if strings.Contains(text, "上一轮结论摘要") {
			summary = true
		}
		if strings.Contains(text, "权威快照") {
			authoritative = true
		}
	}
	if !summary {
		t.Fatal("第二轮没有承接上一轮结论摘要")
	}
	if authoritative {
		t.Fatal("默认模式不应 resume 全量 transcript（不出现权威快照声明）")
	}
}

// 回滚开关：BLADE_PLANNER_RESUME_SESSION=1 恢复旧的跨轮 transcript resume。
func TestPlannerResumeSwitchRestoresOldBehavior(t *testing.T) {
	t.Setenv("BLADE_PLANNER_RESUME_SESSION", "1")
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("planner resume", "把面上的点都过一遍", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)

	prov := &recordingProvider{text: "第一轮结论：api.example 的 /admin 登录口值得深挖"}
	tx := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
	p := NewPlanner(prov, "test-model", t.TempDir(), tx, 0, 4)
	p.SetNonStreaming(func() bool { return true })

	for round := 1; round <= 2; round++ {
		if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "拿下目标", nil, nil); err != nil {
			t.Fatalf("第 %d 轮 Plan: %v", round, err)
		}
	}
	var resumed, authoritative bool
	for _, m := range prov.last().Messages {
		text := m.Text()
		if strings.Contains(text, "第一轮结论") {
			resumed = true
		}
		if strings.Contains(text, "权威快照") {
			authoritative = true
		}
	}
	if !resumed {
		t.Fatal("开关打开时第二轮应续跑上一轮规划会话")
	}
	if !authoritative {
		t.Fatal("续跑时应声明本轮态势是权威快照")
	}
}
