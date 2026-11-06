package server

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// seedRunningIntent 建一条 running 意图并灌入若干「重复 403」轨迹。
func seedRunningIntent(t *testing.T, task *Task, summary string, rounds int) int64 {
	t.Helper()
	iid, err := task.Store.AddNode(db.KindIntent, map[string]any{
		"summary": summary, "risk_area": "authz", "intent_kind": "verify",
	}, 5, "running", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rounds; i++ {
		if _, err := task.Store.AppendActivity(db.Activity{
			NodeID: &iid, Worker: "work#1", Kind: "tool_use", Tool: "Bash",
			Summary: "Bash curl -s http://10.0.0.5/admin",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := task.Store.AppendActivity(db.Activity{
			NodeID: &iid, Worker: "work#1", Kind: "tool_result", Tool: "Bash",
			Summary: "HTTP 403 Forbidden", IsError: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return iid
}

func newSupervisorTestEngine(t *testing.T) (*Engine, *Task) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { m.Close() })
	task, err := m.CreateTask("supervisor", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	t.Cleanup(func() { m.pg.DeleteTask(tid) })
	e := NewEngine(m)
	e.SetConstraintResolver(allConstraints)
	return e, task
}

// 审核员判定偏离 → 通过 steer 通道下发纠偏（来源必须是审核员，不是 planner/人），
// 并在时间线留一条能让人复核的结论。
func TestSupervisorSteersDriftingWorker(t *testing.T) {
	e, task := newSupervisorTestEngine(t)
	iid := seedRunningIntent(t, task, "验证 /admin 越权访问", 4)
	e.registerWork(iid, func(error) {}) // steer 要求 work 正在运行

	calls := 0
	e.SetSupervisor(func(_ context.Context, _ *Task, claim agent.SupervisorClaim) (agent.SupervisorVerdict, string, error) {
		calls++
		if claim.Goal != "x" {
			t.Errorf("审核输入应带任务目标，收到 %q", claim.Goal)
		}
		if len(claim.Intents) != 1 || claim.Intents[0].ID != iid {
			t.Fatalf("审核输入的意图不对：%+v", claim.Intents)
		}
		if claim.Intents[0].Repeated == "" || claim.Intents[0].Errors < 3 {
			t.Errorf("轨迹统计没带上：%+v", claim.Intents[0])
		}
		return agent.SupervisorVerdict{Intents: []agent.SupervisorIntentVerdict{{
			IntentID: iid, OnTrack: false, Reason: "连续 4 次重放同一个 403 请求",
			Steer: "停止重放 /admin；换参数或先写回 blocked",
		}}}, `{"intents":[]}`, nil
	})

	e.supervisorTick(t.Context(), task)
	msg, ok := e.drainSteer(iid)
	if !ok {
		t.Fatal("判定偏离后应下发纠偏")
	}
	if msg.from != steerFromSupervisor {
		t.Fatalf("纠偏来源应为审核员（%d），实际 %d", steerFromSupervisor, msg.from)
	}
	if !strings.Contains(msg.text, "停止重放") {
		t.Fatalf("纠偏内容不对：%q", msg.text)
	}
	if !strings.Contains(msg.render(), "【审核员实时纠偏】") {
		t.Fatalf("worker 看到的归属文案不对：%q", msg.render())
	}

	// 审核过程必须能在 supervisor 会话里看到：轮次分隔、送审输入、模型原始输出、纠偏动作。
	acts, _, err := task.Store.ActivityList(nil, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range acts {
		if a.Worker == "supervisor" {
			seen[a.Kind] = true
		}
	}
	for _, kind := range []string{"round", "supervisor_input", "result", "supervisor_steer"} {
		if !seen[kind] {
			t.Fatalf("supervisor 会话缺少 %q 记录：%+v", kind, acts)
		}
	}

	// 没有新轨迹 → 不重复调模型。
	e.supervisorTick(t.Context(), task)
	if calls != 1 {
		t.Fatalf("无新轨迹时不该再调审核员，实际调用 %d 次", calls)
	}
}

// 同一意图在冷却期内不重复纠偏；有新增轨迹时才会再次审核。
func TestSupervisorRespectsSteerCooldown(t *testing.T) {
	e, task := newSupervisorTestEngine(t)
	iid := seedRunningIntent(t, task, "验证 /admin 越权访问", 4)
	e.registerWork(iid, func(error) {})

	calls := 0
	e.SetSupervisor(func(context.Context, *Task, agent.SupervisorClaim) (agent.SupervisorVerdict, string, error) {
		calls++
		return agent.SupervisorVerdict{Intents: []agent.SupervisorIntentVerdict{{
			IntentID: iid, OnTrack: false, Reason: "还在重放", Steer: "别重放了",
		}}}, `{"intents":[]}`, nil
	})
	e.supervisorTick(t.Context(), task)
	if _, ok := e.drainSteer(iid); !ok {
		t.Fatal("首次应下发纠偏")
	}
	// 新轨迹 → 会再审核，但冷却期内不再注入。
	if _, err := task.Store.AppendActivity(db.Activity{
		NodeID: &iid, Worker: "work#1", Kind: "tool_use", Tool: "Bash", Summary: "Bash curl /admin",
	}); err != nil {
		t.Fatal(err)
	}
	e.supervisorTick(t.Context(), task)
	if calls != 2 {
		t.Fatalf("有新轨迹应再次审核，实际调用 %d 次", calls)
	}
	if _, ok := e.drainSteer(iid); ok {
		t.Fatal("冷却期内不该重复下发纠偏")
	}
}

// 在轨就不打扰：不发指令，也不写系统活动。
func TestSupervisorQuietWhenOnTrack(t *testing.T) {
	e, task := newSupervisorTestEngine(t)
	iid := seedRunningIntent(t, task, "验证 /admin 越权访问", 4)
	e.registerWork(iid, func(error) {})
	e.SetSupervisor(func(context.Context, *Task, agent.SupervisorClaim) (agent.SupervisorVerdict, string, error) {
		return agent.SupervisorVerdict{Intents: []agent.SupervisorIntentVerdict{{
			IntentID: iid, OnTrack: true, Reason: "在按意图验证",
		}}}, `{"intents":[]}`, nil
	})
	e.supervisorTick(t.Context(), task)
	if _, ok := e.drainSteer(iid); ok {
		t.Fatal("在轨的意图不该被纠偏")
	}
	acts, _, _ := task.Store.ActivityList(nil, 0, 50)
	for _, a := range acts {
		if strings.Contains(a.Summary, "审核员纠偏") {
			t.Fatalf("在轨时不该写纠偏记录：%+v", a)
		}
	}
}

// 模型编造意图 id 时不能误伤别的 work。
func TestSupervisorIgnoresUnknownIntent(t *testing.T) {
	e, task := newSupervisorTestEngine(t)
	iid := seedRunningIntent(t, task, "验证 /admin 越权访问", 4)
	e.registerWork(iid, func(error) {})
	e.SetSupervisor(func(context.Context, *Task, agent.SupervisorClaim) (agent.SupervisorVerdict, string, error) {
		return agent.SupervisorVerdict{Intents: []agent.SupervisorIntentVerdict{{
			IntentID: iid + 999, OnTrack: false, Reason: "幻觉", Steer: "乱指挥",
		}}}, `{"intents":[]}`, nil
	})
	e.supervisorTick(t.Context(), task)
	if _, ok := e.drainSteer(iid); ok {
		t.Fatal("不存在的意图 id 不该产生纠偏")
	}
}

// 轨迹太少的意图（刚领到）不送审：此时看不出偏不偏。
func TestSupervisorSkipsFreshIntent(t *testing.T) {
	e, task := newSupervisorTestEngine(t)
	seedRunningIntent(t, task, "刚领到的意图", 1)
	calls := 0
	e.SetSupervisor(func(context.Context, *Task, agent.SupervisorClaim) (agent.SupervisorVerdict, string, error) {
		calls++
		return agent.SupervisorVerdict{}, `{"intents":[]}`, nil
	})
	e.supervisorTick(t.Context(), task)
	if calls != 0 {
		t.Fatalf("轨迹不足时不该调审核员，实际 %d 次", calls)
	}
}

// 审核会话在历史接口里可读：session=supervisor 映射到 worker='supervisor'。
func TestParseActivitySessionSupervisor(t *testing.T) {
	f, ok := parseActivitySession("supervisor")
	if !ok || f.Worker != "supervisor" {
		t.Fatalf("supervisor 会话解析不对：%+v ok=%v", f, ok)
	}
}
