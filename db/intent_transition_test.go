package db

import "testing"

// TestIntentStateTransitionsAreGuarded 钉住"写意图状态必须带谓词"这一条。
//
// 覆盖的是**语义**层面的三种典型冲突：并发收尾、租约回收、删除。它们各自对应一种
// 谓词（expected state / owner+lease / 状态白名单），用例断言的是"冲突时不动"。
func TestIntentStateTransitionsAreGuarded(t *testing.T) {
	_, st := intentLeaseFixture(t)

	add := func(state string) int64 {
		id, err := st.AddIntent(map[string]any{"summary": "guard-" + state}, 1, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetIntentState(id, state); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// ① CAS 命中：当前状态匹配才迁移。
	id := add("running")
	if ok, err := st.CompareAndSetIntentState(id, "running", "done"); err != nil || !ok {
		t.Fatalf("CAS(running→done) 应成功：ok=%v err=%v", ok, err)
	}
	// ② CAS 落空：状态已变，不再迁移（这就是"并发收尾不能被覆盖"的机制）。
	if ok, err := st.CompareAndSetIntentState(id, "running", "blocked"); err != nil || ok {
		t.Fatalf("状态已不在 running，CAS 应失败：ok=%v err=%v", ok, err)
	}

	// ③ 租约回收带 owner + 租约谓词：owner 不符不动。
	claimed := add("open")
	if ok, err := st.ClaimIntent(claimed, "work#1", 60e9); err != nil || !ok {
		t.Fatalf("领取应成功：ok=%v err=%v", ok, err)
	}
	if ok, err := st.ReopenExpiredIntent(claimed, "work#2"); err != nil || ok {
		t.Fatalf("owner 不符时不该回收：ok=%v err=%v", ok, err)
	}

	// ④ 删除带状态白名单：终态意图不能删。
	finished := add("done")
	if _, _, err := st.SoftDeleteIntent(finished, "test"); err == nil {
		t.Fatal("终态意图应拒绝删除")
	}
	// 待领状态可以删。
	open := add("open")
	if _, _, err := st.SoftDeleteIntent(open, "test"); err != nil {
		t.Fatalf("open 意图应可删：%v", err)
	}
}
