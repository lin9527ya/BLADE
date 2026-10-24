package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/transcript"
)

// planner 每轮此前都从空对话开始：上一轮读过的 node_detail 证据、coverage_matrix、
// trace 片段全部丢失，只能重新取或干脆不再看（worker 早就会 Resume，planner 一直漏了）。
func TestPlannerResumesPreviousRound(t *testing.T) {
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
	p.SetNonStreaming(func() bool { return true }) // 测试 provider 只有非流式路径

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
		t.Fatal("第二轮没有续跑上一轮的规划会话：提示词里找不到第一轮内容")
	}
	// 续跑会让旧态势块"粘住"，必须明说本轮快照才是权威。
	if !authoritative {
		t.Fatal("续跑时应声明本轮态势是权威快照")
	}
}
