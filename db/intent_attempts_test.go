package db

import (
	"testing"
)

// 领取次数是升级阶梯的判据：每次 claim 在同一原子 UPDATE 里 +1；结算时把终态写进
// payload.last_outcome。三个 claim 入口都要覆盖——漏一个就会出现"这条永远第 1 次"。
func TestClaimCountsAttempts(t *testing.T) {
	d, st := auditFixture(t)
	id, err := st.AddIntent(map[string]any{"summary": "反复失败的方向"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	if ok, err := st.ClaimIntent(id, "work#1", IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("第一次领取失败：ok=%v err=%v", ok, err)
	}
	if att := payloadAttemptsOf(t, d, id); att != 1 {
		t.Fatalf("第一次领取后 attempts=%d，want 1", att)
	}
	// 领回 open 再领一次（引擎在租约过期/模型失败后就是这么重试的）。
	if ok, err := st.CompareAndSetIntentState(id, "running", "open"); err != nil || !ok {
		t.Fatalf("退回 open 失败：ok=%v err=%v", ok, err)
	}
	if ok, err := st.ClaimIntent(id, "work#1", IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("第二次领取失败：ok=%v err=%v", ok, err)
	}
	if att := payloadAttemptsOf(t, d, id); att != 2 {
		t.Fatalf("第二次领取后 attempts=%d，want 2", att)
	}

	// 结算写 last_outcome（CAS 的终态分支）。
	if ok, err := st.CompareAndSetIntentState(id, "running", "no_output"); err != nil || !ok {
		t.Fatalf("结算失败：ok=%v err=%v", ok, err)
	}
	var last string
	if err := d.QueryRow(`SELECT COALESCE(payload->>'last_outcome','') FROM exploration_nodes WHERE id=$1`, id).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last != "no_output" {
		t.Fatalf("last_outcome = %q，want no_output", last)
	}
	// 索引侧要能读到次数。
	digests, err := st.ListNodeDigests(KindIntent, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range digests {
		if n.ID == id {
			found = true
			if n.Attempts != 2 {
				t.Fatalf("digest attempts = %d，want 2", n.Attempts)
			}
		}
	}
	if !found {
		t.Fatal("ListNodeDigests 没返回这条意图")
	}
}

// payload.attempts 被写进脏值时，claim 不能失败（否则一条模型写坏的节点会让任务卡死）。
func TestClaimToleratesDirtyAttemptsPayload(t *testing.T) {
	d, st := auditFixture(t)
	id, err := st.AddIntent(map[string]any{"summary": "脏 payload"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET payload = payload || '{"attempts":"abc"}'::jsonb WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntentForSlot(id, "work#1", IntentLeaseTTL, []string{"work#1"}); err != nil || !ok {
		t.Fatalf("脏 payload 下领取失败：ok=%v err=%v", ok, err)
	}
	if att := payloadAttemptsOf(t, d, id); att != 1 {
		t.Fatalf("脏值应被当成 0 再 +1，attempts=%d", att)
	}
}

func payloadAttemptsOf(t *testing.T, d *DB, id int64) int {
	t.Helper()
	var att int
	if err := d.QueryRow(`SELECT CASE WHEN COALESCE(payload->>'attempts','') ~ '^[0-9]{1,6}$'
THEN (payload->>'attempts')::int ELSE 0 END FROM exploration_nodes WHERE id=$1`, id).Scan(&att); err != nil {
		t.Fatal(err)
	}
	return att
}
