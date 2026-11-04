package db

import (
	"strings"
	"testing"
)

// 每任务并发覆盖与备注：合法值落库并读回，越界值在写入前被拒。
func TestTaskWorkerCountAndRemarkRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	tk, err := d.CreateTaskWithOptions("并发覆盖", "g", TaskCreateOptions{WorkerCount: 6, Remark: "客户要求只在夜间跑"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(tk.ID) })
	got, err := d.GetTask(tk.ID)
	if err != nil || got == nil {
		t.Fatalf("读回失败：%v", err)
	}
	if got.WorkerCount != 6 || got.Remark != "客户要求只在夜间跑" {
		t.Fatalf("并发覆盖/备注未落库：%+v", got)
	}

	// 0 = 跟随全局，不是"零个 worker"。
	tk2, err := d.CreateTaskWithOptions("跟随全局", "g", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(tk2.ID) })
	if got2, _ := d.GetTask(tk2.ID); got2 == nil || got2.WorkerCount != 0 {
		t.Fatalf("未指定时应为 0（跟随全局）：%+v", got2)
	}
}

func TestTaskCreateRejectsInvalidWorkerCountAndRemark(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	if _, err := d.CreateTaskWithOptions("too many", "g", TaskCreateOptions{WorkerCount: MaxTaskWorkerCount + 1}); err == nil {
		t.Fatal("超过上限的并发覆盖应被拒绝")
	}
	if _, err := d.CreateTaskWithOptions("long remark", "g", TaskCreateOptions{Remark: strings.Repeat("x", MaxTaskRemarkRunes+1)}); err == nil {
		t.Fatal("超长备注应被拒绝")
	}
	// 负数归一成 0（跟随全局），而不是报错——老调用方可能传 -1。
	tk, err := d.CreateTaskWithOptions("negative", "g", TaskCreateOptions{WorkerCount: -1})
	if err != nil {
		t.Fatalf("负数应归一为 0：%v", err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(tk.ID) })
	if got, _ := d.GetTask(tk.ID); got == nil || got.WorkerCount != 0 {
		t.Fatalf("负数应归一为 0：%+v", got)
	}
}
