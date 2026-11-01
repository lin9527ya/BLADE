package server

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 收官期阈值：短任务不切（否则一开工就收尾）；常规任务取总时长 15%，夹在 15–60 分钟。
func TestEndgameStartUnix(t *testing.T) {
	const deadline = int64(2_000_000_000)
	cases := []struct {
		name    string
		dl, sec int64
		want    int64 // 相对 deadline 的提前量（秒）
	}{
		{"无截止", 0, 3600, 0},
		{"无超时", deadline, 0, 0},
		{"短任务不切收官期", deadline, int64(10 * time.Minute / time.Second), 0},
		{"一小时任务取 15 分钟下限", deadline, int64(time.Hour / time.Second), int64(15 * time.Minute / time.Second)},
		{"两小时任务取 15%", deadline, int64(2 * time.Hour / time.Second), int64(18 * time.Minute / time.Second)},
		{"八小时任务封顶 60 分钟", deadline, int64(8 * time.Hour / time.Second), int64(time.Hour / time.Second)},
		{"半小时任务取 15%", deadline, int64(30 * time.Minute / time.Second), int64(15 * time.Minute / time.Second)},
	}
	for _, c := range cases {
		got := endgameStartUnix(c.dl, c.sec)
		want := int64(0)
		if c.want != 0 && c.dl != 0 {
			want = c.dl - c.want
		}
		if got != want {
			t.Fatalf("%s: endgameStartUnix = %d, want %d", c.name, got, want)
		}
	}
}

// 对账清单是把"库里已有、但没人统一巡检"的状态聚起来：遗留物、未闭合候选、未达成目标、
// 未结算意图。没有事项时返回空串（对账失败不该让任务收不了官）。
func TestReconcileChecklistPicksUpPendingWork(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	task, err := m.CreateTask("endgame reconcile", "对账", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())

	if got := s.engine.reconcileChecklist(task); got != "" {
		t.Fatalf("干净任务不该有对账事项：%s", got)
	}

	seed := func(kind, state string) {
		if _, err := m.pg.Exec(`INSERT INTO exploration_nodes(exploration_id, kind, state, payload)
VALUES ($1,$2,$3,'{}'::jsonb)`, task.ExpID, kind, state); err != nil {
			t.Fatalf("seed %s/%s: %v", kind, state, err)
		}
	}
	seed("goal", "open")
	seed("artifact", "active")
	seed("finding", "open_proof_gap")
	seed("intent", "open")

	got := s.engine.reconcileChecklist(task)
	for _, want := range []string{
		"未达成目标 1 条",
		"未销账遗留物 1 条",
		"未闭合候选 1 条",
		"未结算意图 1 条",
		"收尾对账清单",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("对账清单缺少 %q：\n%s", want, got)
		}
	}
}
