package server

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// 暂停中的任务不判「策略停摆」。停摆收尾写的是终态，一旦在暂停态上落下去，人工暂停
// 就被系统抢先收尾，用户连「继续运行」都按不动——任务 18：05:17 因连续空轮被停滞
// 保护暂停，05:20 又被停摆判定改写成 policy_blocked。
func TestPolicyStallSkipsPausedTask(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("policy stall paused", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteTask(tid)
	if err := m.pg.UpdateTaskExplorationPolicy(tid, json.RawMessage(
		`{"override":{"techniques":["bruteforce"]},"effective":{"source":"task","version":1,`+
			`"techniques":["bruteforce"],"forbidden_writes":[],"max_open_intents":0,"max_intents_total":0,"max_intents_per_round":4}}`)); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m)
	savedW, savedN, savedG := policyStallWindow, policyStallMinRejects, policyStallGrace
	t.Cleanup(func() { policyStallWindow, policyStallMinRejects, policyStallGrace = savedW, savedN, savedG })
	policyStallWindow, policyStallMinRejects, policyStallGrace = time.Minute, 3, 0
	for i := 0; i < policyStallMinRejects; i++ {
		e.emitActivity(task, db.Activity{Worker: "planner", Kind: "policy_reject", Summary: "探索策略拒绝：本轮意图配额"})
	}
	if !e.policyStall(task) {
		t.Fatal("基线：窗口内连续被拒且无新意图，应判定为策略停摆")
	}
	e.Pause(task.ID, agent.AbortPausedByUser)
	if e.policyStall(task) {
		t.Fatal("暂停的任务不该再判策略停摆：再落终态就把人工暂停吞掉了")
	}
}

// 配额/节奏（pace:*）与否定语境放行不是"方向被禁"，不能计入策略停摆。任务 19 实测：
// 热会话额度卡死后连续 pace:round 拒绝，被当成"策略内已无可行方向"直接写成终态。
func TestPolicyStallIgnoresPacingRejects(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("policy stall pacing", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteTask(tid)
	if err := m.pg.UpdateTaskExplorationPolicy(tid, json.RawMessage(
		`{"override":{"techniques":["bruteforce"]},"effective":{"source":"task","version":1,`+
			`"techniques":["bruteforce"],"forbidden_writes":[],"max_open_intents":0,"max_intents_total":0,"max_intents_per_round":4}}`)); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m)
	savedW, savedN, savedG := policyStallWindow, policyStallMinRejects, policyStallGrace
	t.Cleanup(func() { policyStallWindow, policyStallMinRejects, policyStallGrace = savedW, savedN, savedG })
	policyStallWindow, policyStallMinRejects, policyStallGrace = time.Minute, 3, 0

	reject := func(item string, benign bool) {
		meta, _ := json.Marshal(map[string]any{"policy_item": item, "benign": benign})
		e.emitActivity(task, db.Activity{Worker: "system", Kind: "policy_reject",
			Summary: "探索策略拒绝：" + item, Metadata: meta})
	}
	for i := 0; i < policyStallMinRejects; i++ {
		reject("pace:round", false)
	}
	reject("technique:bruteforce", true) // 否定语境放行，不是拒绝
	if e.policyStall(task) {
		t.Fatal("pace:* 与 benign 记录不该被判成策略停摆")
	}
	for i := 0; i < policyStallMinRejects; i++ {
		reject("technique:bruteforce", false) // 真正的方向禁用
	}
	if !e.policyStall(task) {
		t.Fatal("连续方向禁用应判策略停摆")
	}
}
