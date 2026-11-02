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
