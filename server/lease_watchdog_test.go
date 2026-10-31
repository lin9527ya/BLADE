package server

import (
	"strconv"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// TestLeaseWatchdogReclaimsStalledIntent drives the engine sweep directly: a running
// intent whose heartbeat lapsed is reclaimed to open (so the pool re-runs it), while a
// running intent with a live lease is left untouched.
func TestLeaseWatchdogReclaimsStalledIntent(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行（Go 的测试清理顺序），所以关库
	// 只能注册成 Cleanup——写 defer 会让它先于下面的数据清理跑，清理语句全部落到
	// 已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = m.Close() })
	e := NewEngine(m)
	task, err := m.pg.CreateTask("lease-watchdog-test", "回收僵死意图", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var expID int64
	if err := m.pg.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task.ID).Scan(&expID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
		_, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
	})
	st := m.pg.Exploration(expID)
	stalled, err := st.AddIntent(map[string]any{"summary": "僵死的意图"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(stalled, "work#1", db.IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim stalled: ok=%v err=%v", ok, err)
	}
	if _, err := m.pg.Exec(`UPDATE exploration_nodes SET heartbeat_at=now()-interval '30 minutes', lease_expires_at=now()-interval '15 minutes' WHERE id=$1`, stalled); err != nil {
		t.Fatal(err)
	}
	healthy, err := st.AddIntent(map[string]any{"summary": "心跳正常的意图"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(healthy, "work#2", db.IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim healthy: ok=%v err=%v", ok, err)
	}
	tk := &Task{ID: strconv.FormatInt(task.ID, 10), ExpID: expID, Store: st, notify: make(chan struct{}, 1)}
	e.sweepExpiredLeases(tk)
	node, err := st.GetNode(stalled)
	if err != nil || node == nil {
		t.Fatalf("stalled node: %+v err=%v", node, err)
	}
	// Owner stays on the slot that held the transcript so it re-claims with cache.
	if node.State != "open" || node.Owner != "work#1" {
		t.Fatalf("stalled intent must be reclaimed: state=%s owner=%q", node.State, node.Owner)
	}
	keep, err := st.GetNode(healthy)
	if err != nil || keep == nil {
		t.Fatalf("healthy node: %+v err=%v", keep, err)
	}
	if keep.State != "running" || keep.Owner != "work#2" {
		t.Fatalf("healthy intent must be untouched: state=%s owner=%q", keep.State, keep.Owner)
	}
	select {
	case <-tk.notify:
	default:
		t.Fatal("reclaim must notify the planner")
	}
}
