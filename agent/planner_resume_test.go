package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/llm"
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

// 热会话的上下文体积必须**有界**：在增量窗内追加游标变更以保留 Turn 1 缓存前缀，
// 超过消息上限后再折叠回最小前缀并重置游标，轮数继续增加时历史不无限增长。
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
	// Round 1 starts with full snapshot; Round 2..N should use incremental delta view until recycled.
	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "g", nil, nil); err != nil {
		t.Fatalf("round 0: %v", err)
	}
	for r := 1; r < 10; r++ {
		if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "g", nil, nil); err != nil {
			t.Fatalf("round %d: %v", r, err)
		}
	}
	if n := len(p.hot.Messages()); n > plannerHotMaxMessages+2 {
		t.Fatalf("热会话历史应保持有界（<= %d），got %d 条", plannerHotMaxMessages+2, n)
	}
	foundDelta := false
	for _, req := range prov.reqs {
		for _, m := range req.Messages {
			if strings.Contains(m.Text(), "【本轮增量态势") {
				foundDelta = true
			}
		}
	}
	if !foundDelta {
		t.Fatal("热会话第 2 轮起应走增量态势而不是每轮重发全量 snapshots")
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

// 800K/200K 等窗口在过半（55%–60%）时不应被旧的「compactionWindow/2」半窗砍断折叠。
func TestHotSessionPreservesAboveHalfWindow(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("hot window", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	prov := &recordingProvider{text: "结论：继续深挖"}
	tx := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
	p := NewPlanner(prov, "m", t.TempDir(), tx, 200_000, 4)
	p.SetNonStreaming(func() bool { return true })

	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), ts, "g", nil, nil); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	// 将热会话填充到 110K tokens（200K 的 55%：高于旧 100K 半窗，低于新 150K 3/4 窗），验证 parkHotSession 不 Reset。
	p.hot.Reset()
	p.hot.AppendMessages(
		prov.reqs[0].Messages[0],
		assistantMsg(strings.Repeat("x", 440_000)), // 110K tokens
	)
	beforeLen := len(p.hot.Messages())
	p.parkHotSession(p.hot, p.hotTools, "round-in", "ok", ts.ID(), 1, "ev1:1")
	if len(p.hot.Messages()) != beforeLen {
		t.Fatalf("110K/200K (55%%) 热会话不应被提前 Reset（len %d -> %d）", beforeLen, len(p.hot.Messages()))
	}
	if p.hotExpID != ts.ID() {
		t.Fatalf("hotExpID=%d, want %d", p.hotExpID, ts.ID())
	}
}

// 无数据库回归：直接验证 parkHotSession 在 55%（>50% 旧半窗，<=75% 新阈值）保留热会话，超过 75% 时正常折叠。
func TestParkHotSessionAboveHalfWindow(t *testing.T) {
	prov := &recordingProvider{text: "ok"}
	tx := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
	p := NewPlanner(prov, "m", t.TempDir(), tx, 200_000, 4)
	s := agentcore.NewSession(agentcore.Options{Provider: prov})
	tools := &ToolSet{}

	// 110K ASCII tokens（200K 的 55%，高于旧 100K 半窗，低于新 150K 3/4 窗）必须保留。
	s.AppendMessages(
		llm.UserText("round-1-input"),
		assistantMsg(strings.Repeat("x", 440_000)),
	)
	p.parkHotSession(s, tools, "round-1-input", "round-1-out", 42, 1, "ev1:1")
	if p.hot != s || len(p.hot.Messages()) != 2 {
		t.Fatalf("110K/200K (55%%) 热会话应保留，hot=%v len=%d", p.hot == s, len(p.hot.Messages()))
	}
	if p.hotExpID != 42 {
		t.Fatalf("hotExpID=%d, want 42", p.hotExpID)
	}

	// 再追加 200KB（+50K tokens，累计 ~160K > 150K 3/4 窗）后，再次 park 应触发 Reset + 折叠成「本轮输入 + 结论」两条消息。
	p.hot.AppendMessages(assistantMsg(strings.Repeat("y", 200_000)))
	p.parkHotSession(p.hot, p.hotTools, "round-2-input", "round-2-out", 42, 2, "ev2:1")
	if len(p.hot.Messages()) != 2 {
		t.Fatalf("超过 75%% 上限后应折叠成 2 条消息，got %d", len(p.hot.Messages()))
	}
	if !strings.Contains(p.hot.Messages()[0].Text(), "round-2-input") || !strings.Contains(p.hot.Messages()[1].Text(), "round-2-out") {
		t.Fatalf("折叠后的热会话消息内容不符合预期: %q / %q", p.hot.Messages()[0].Text(), p.hot.Messages()[1].Text())
	}
}
