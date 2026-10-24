package server

import (
	"context"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 没有显式超时的托管执行必须有平台级存活上限：否则一条忘记设超时的僵尸扫描会把意图
// 钉在 running 上（收尾门只认"还有执行在跑"），看门狗要 24 小时才放弃。
func TestExecMaxAgeSetting(t *testing.T) {
	_, pg, _, _ := newExecFixture(t)
	prev, had, err := pg.GetSetting(settingExecMaxAge)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = pg.SetSetting(settingExecMaxAge, prev)
			return
		}
		_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, settingExecMaxAge)
	})
	clear := func() { _, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, settingExecMaxAge) }

	clear()
	if got := execMaxAgeMinutes(pg); got != defaultExecMaxAgeMin {
		t.Fatalf("缺省应回落到 %d 分钟，实际 %d", defaultExecMaxAgeMin, got)
	}
	if err := pg.SetSetting(settingExecMaxAge, "0"); err != nil {
		t.Fatal(err)
	}
	if got := execMaxAgeMinutes(pg); got != 0 {
		t.Fatalf("显式 0 应表示不限，实际 %d", got)
	}
	if err := pg.SetSetting(settingExecMaxAge, "30"); err != nil {
		t.Fatal(err)
	}
	if got := execMaxAgeMinutes(pg); got != 30 {
		t.Fatalf("应读 30 分钟，实际 %d", got)
	}
	if err := pg.SetSetting(settingExecMaxAge, "abc"); err != nil {
		t.Fatal(err)
	}
	if got := execMaxAgeMinutes(pg); got != defaultExecMaxAgeMin {
		t.Fatalf("非法值应回退默认，实际 %d", got)
	}
}

// 执行落终态要回调 —— server 用它唤醒任务（planner 自起的执行此前无人观察）。
func TestExecutionOnDoneFires(t *testing.T) {
	svc, _, _, task := newExecFixture(t)
	done := make(chan int64, 1)
	svc.SetOnDone(func(row *db.ToolExecution) {
		if row != nil {
			done <- row.ID
		}
	})
	st, err := svc.Submit(context.Background(), task, 0, "test-worker", "true", "onDone 用例", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-done:
		if id != st.ID {
			t.Fatalf("回调的执行 id 不对：got=%d want=%d", id, st.ID)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("执行结束后 onDone 没有被调用")
	}
}
