package db

import (
	"testing"
	"time"
)

// 运行时长分段记账的三条纪律：暂停/排队不计入、幂等、终态结算一次。
func TestTaskRunSegmentAccounting(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	tk, err := d.CreateTask("运行时长", "g", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id := tk.ID
	t.Cleanup(func() { _ = d.DeleteTask(id) })

	// 开段 → 已结算为 0、起点非空。
	if err := d.StartTaskRunSegment(id); err != nil {
		t.Fatal(err)
	}
	active, started, err := d.RunSeconds(id)
	if err != nil || started == nil || active != 0 {
		t.Fatalf("开段后应有起点且已结算为 0：active=%d started=%v err=%v", active, started, err)
	}

	// 幂等：再开一次不会把起点推后。
	first := *started
	if err := d.StartTaskRunSegment(id); err != nil {
		t.Fatal(err)
	}
	_, again, _ := d.RunSeconds(id)
	if again == nil || !again.Equal(first) {
		t.Fatalf("重复开段不该重置起点：%v → %v", first, again)
	}

	// 结算后已结算秒数增长、起点清空。
	time.Sleep(1100 * time.Millisecond)
	if err := d.FinalizeTaskRunSegment(id); err != nil {
		t.Fatal(err)
	}
	active, started, _ = d.RunSeconds(id)
	if active < 1 || started != nil {
		t.Fatalf("结算后应有 >=1 秒且无起点：active=%d started=%v", active, started)
	}

	// 幂等：没有起点时再结算一次不改变累计值。
	before := active
	if err := d.FinalizeTaskRunSegment(id); err != nil {
		t.Fatal(err)
	}
	active, _, _ = d.RunSeconds(id)
	if active != before {
		t.Fatalf("无起点时结算应是空操作：%d → %d", before, active)
	}
}

// 暂停的任务不开新段：暂停期间的时间不该被算成运行。
func TestTaskRunSegmentRefusesWhenPaused(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	tk, err := d.CreateTask("运行时长-暂停", "g", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id := tk.ID
	t.Cleanup(func() { _ = d.DeleteTask(id) })
	if err := d.SetStatus(id, "running"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := d.StartTaskRunSegment(id); err != nil {
		t.Fatal(err)
	}
	_, started, _ := d.RunSeconds(id)
	if started != nil {
		t.Fatal("暂停中的任务不该开新运行段")
	}
}

// 进入终态时自动结算当前段：不需要调用方再记得调 Finalize。
func TestTaskTerminalStatusFoldsSegment(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	tk, err := d.CreateTask("运行时长-终态", "g", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id := tk.ID
	t.Cleanup(func() { _ = d.DeleteTask(id) })
	if err := d.SetStatus(id, "running"); err != nil {
		t.Fatal(err)
	}
	if err := d.StartTaskRunSegment(id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if won, err := d.SetTerminalStatusGuarded(id, "done"); err != nil || !won {
		t.Fatalf("终态写入失败：won=%v err=%v", won, err)
	}
	active, started, _ := d.RunSeconds(id)
	if active < 1 || started != nil {
		t.Fatalf("终态应把当前段折进累计时长：active=%d started=%v", active, started)
	}
}
