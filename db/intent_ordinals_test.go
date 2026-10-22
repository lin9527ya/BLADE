package db

import (
	"testing"
)

// TestIntentOrdinalsArePerTask 钉住「Worker #N 是任务内序号」：意图 id 是全局序列，
// 任务 2 的意图 id 会继续增长，但它的 1-based 序号必须从 1 重新开始。
func TestIntentOrdinalsArePerTask(t *testing.T) {
	d, task1, exp1 := coverageMatrixFixture(t)
	task2, err := d.CreateTask("ordinals-task-2", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var exp2 int64
	if err := d.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task2.ID).Scan(&exp2); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, exp2)
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task2.ID)
	})
	st1, st2 := d.Exploration(exp1), d.Exploration(exp2)
	var ids1, ids2 []int64
	for i := 0; i < 3; i++ {
		id, err := st1.AddIntent(map[string]any{"summary": "t1"}, 5, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		ids1 = append(ids1, id)
	}
	for i := 0; i < 2; i++ {
		id, err := st2.AddIntent(map[string]any{"summary": "t2"}, 5, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		ids2 = append(ids2, id)
	}

	seq1, err := st1.IntentOrdinals(0)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids1 {
		if seq1[id] != int64(i+1) {
			t.Fatalf("任务1 第 %d 条意图序号=%d，want %d（%v）", i+1, seq1[id], i+1, seq1)
		}
	}
	seq2, err := st2.IntentOrdinals(0)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids2 {
		if seq2[id] != int64(i+1) {
			t.Fatalf("任务2 第 %d 条意图序号=%d，want %d（全局 id 应被忽略）", i+1, seq2[id], i+1)
		}
	}
	_ = task1

	// maxID 截断：只看前 2 条时不返回第 3 条。
	seqCapped, err := st1.IntentOrdinals(ids1[1])
	if err != nil {
		t.Fatal(err)
	}
	if seqCapped[ids1[0]] != 1 || seqCapped[ids1[1]] != 2 || seqCapped[ids1[2]] != 0 {
		t.Fatalf("maxID 截断不对：%v", seqCapped)
	}
}
