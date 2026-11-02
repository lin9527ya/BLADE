package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/transcript"
)

// 默认：跨轮复用**热会话**——第二轮沿用同一个 Session，历史被折叠成「上一轮输入 +
// 结论」两条消息，既吃到缓存前缀，又不会像 resume 全量那样把 transcript 越滚越大。
func TestPlannerDefaultsToHotSession(t *testing.T) {
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
	var carried, authoritative, firstRoundInput bool
	for _, m := range prov.last().Messages {
		text := m.Text()
		if strings.Contains(text, "第一轮结论") {
			carried = true
		}
		if strings.Contains(text, "权威快照") {
			authoritative = true
		}
		if strings.Contains(text, "本轮态势") {
			firstRoundInput = true
		}
	}
	if !carried {
		t.Fatal("第二轮没有承接上一轮结论（热会话应保留「上一轮输入 + 结论」两条消息）")
	}
	if !firstRoundInput {
		t.Fatal("热会话应保留上一轮的输入作为前缀")
	}
	if authoritative {
		t.Fatal("默认模式不应 resume 全量 transcript（不出现权威快照声明）")
	}
	// 热会话必须被裁短：两轮之后历史里不该堆积完整工具记录。
	if n := len(p.hot.Messages()); n > 6 {
		t.Fatalf("热会话历史应被折叠成少量消息，got %d 条", n)
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

// 热会话的上下文体积必须**有界**：轮数增加时历史不增长（每轮折叠成「输入 + 结论」）。
func TestHotSessionStaysBounded(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("hot bounded", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	prov := &recordingProvider{text: "结论：继续补测 /admin"}
	tx := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
	p := NewPlanner(prov, "m", t.TempDir(), tx, 0, 4)
	p.SetNonStreaming(func() bool { return true })
	for r := 0; r < 5; r++ {
		if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "g", nil, nil); err != nil {
			t.Fatalf("round %d: %v", r, err)
		}
	}
	if n := len(p.hot.Messages()); n > 4 {
		t.Fatalf("热会话历史应保持有界（折叠成输入+结论），got %d 条", n)
	}
	if p.hotRounds < 4 {
		t.Fatalf("应复用同一热会话，rounds=%d", p.hotRounds)
	}
}

// 热会话到达轮数上限后必须重建（不能让摘要层数与陈旧会话无限累积）。
func TestHotSessionRecyclesAtRoundLimit(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("hot recycle", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	prov := &recordingProvider{text: "结论"}
	tx := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
	p := NewPlanner(prov, "m", t.TempDir(), tx, 0, 4)
	p.SetNonStreaming(func() bool { return true })
	for r := 0; r < plannerHotMaxRounds+1; r++ {
		if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "g", nil, nil); err != nil {
			t.Fatalf("round %d: %v", r, err)
		}
	}
	if p.hotRounds > plannerHotMaxRounds {
		t.Fatalf("到上限后应重建热会话，rounds=%d > %d", p.hotRounds, plannerHotMaxRounds)
	}
}
