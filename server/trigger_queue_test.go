package server

import "testing"

// 这些测试是「把触发队列抽成独立类型」的主要回报：排队/合并/在跑计数/空闲清理
// 此前埋在一个持锁的 Server 方法里，只能靠跑真调度器间接观察。现在可以逐条钉住。

func runOf(taskID int64, mergeable bool) triggeredRun {
	return triggeredRun{agentKey: "a", message: "m", taskID: taskID, mergeable: mergeable}
}

func serial() triggerBehavior { return triggerBehavior{runMode: "serial", mergeMode: "none"} }
func parallel(n int) triggerBehavior {
	return triggerBehavior{runMode: "parallel", mergeMode: "none", maxParallel: n}
}

// TestTriggerQueueSerialRunsOneAtATime：serial 模式下无论排多少条，一次只放一条。
func TestTriggerQueueSerialRunsOneAtATime(t *testing.T) {
	q := newTriggerQueue()
	var got []triggeredRun
	for i := 0; i < 3; i++ {
		got = append(got, q.Enqueue("a", runOf(0, false), serial(), true)...)
	}
	if len(got) != 1 {
		t.Fatalf("serial：首批应只放 1 条，实际 %d", len(got))
	}
	if n := q.Active("a"); n != 1 {
		t.Fatalf("serial：在跑数应为 1，实际 %d", n)
	}
	// 第一条跑完后补齐下一条。
	next := q.Done("a", true)
	if len(next) != 1 {
		t.Fatalf("serial：Done 应补 1 条，实际 %d", len(next))
	}
}

// TestTriggerQueueParallelHonorsLimit：parallel 模式下在跑数不超过 maxParallel。
func TestTriggerQueueParallelHonorsLimit(t *testing.T) {
	q := newTriggerQueue()
	var got []triggeredRun
	for i := 0; i < 5; i++ {
		got = append(got, q.Enqueue("a", runOf(0, false), parallel(2), true)...)
	}
	if len(got) != 2 {
		t.Fatalf("parallel(2)：首批应放 2 条，实际 %d", len(got))
	}
	if n := q.Active("a"); n != 2 {
		t.Fatalf("在跑数应为 2，实际 %d", n)
	}
	if next := q.Done("a", true); len(next) != 1 {
		t.Fatalf("空出一个位应补 1 条，实际 %d", len(next))
	}
}

// TestTriggerQueueParallelZeroIsUnlimited：maxParallel<=0 是用户可选的「不限」，
// 必须把队列一次放空（见前端 types.ts 的 trigger_max_parallel 注释）。
func TestTriggerQueueParallelZeroIsUnlimited(t *testing.T) {
	q := newTriggerQueue()
	var got []triggeredRun
	for i := 0; i < 7; i++ {
		got = append(got, q.Enqueue("a", runOf(0, false), parallel(0), true)...)
	}
	if len(got) != 7 {
		t.Fatalf("maxParallel=0 应一次放空 7 条，实际 %d", len(got))
	}
}

// TestTriggerQueueMergeAllCollapsesQueue：mergeMode=all 把整个队列并成一条。
func TestTriggerQueueMergeAllCollapsesQueue(t *testing.T) {
	q := newTriggerQueue()
	cfg := triggerBehavior{runMode: "serial", mergeMode: "all"}
	q.Enqueue("a", runOf(1, true), cfg, true) // 第一条放出去
	got := q.Enqueue("a", runOf(2, true), cfg, true)
	if len(got) != 0 {
		t.Fatalf("还在跑时不该再放，实际 %d", len(got))
	}
	next := q.Done("a", true)
	if len(next) != 1 {
		t.Fatalf("all: 应合并成 1 条，实际 %d", len(next))
	}
	// 队列必须被清空，否则 Done 会一直重新泵。
	if rest := q.Done("a", true); len(rest) != 0 {
		t.Fatalf("合并后队列应已空，实际又放出 %d 条", len(rest))
	}
}

// TestTriggerQueueMergeNoneKeepsItemsSeparate：mergeMode=none 逐条跑，不合并。
func TestTriggerQueueMergeNoneKeepsItemsSeparate(t *testing.T) {
	q := newTriggerQueue()
	cfg := serial()
	q.Enqueue("a", runOf(1, true), cfg, true)
	next := q.Done("a", true) // 队列还空着
	if len(next) != 0 {
		t.Fatalf("队列空时 Done 不该放东西，实际 %d", len(next))
	}
	q.Enqueue("a", runOf(1, true), cfg, true)
	q.Enqueue("a", runOf(1, true), cfg, true)
	one := q.Done("a", true)
	if len(one) != 1 {
		t.Fatalf("none: 应逐条放出，实际 %d", len(one))
	}
}

// TestTriggerQueueMergeByTaskGroupsOnlySameTask：by_task 只合并「同任务且可合并」的，
// 其它项保持原有顺序留在队列里。
func TestTriggerQueueMergeByTaskGroupsOnlySameTask(t *testing.T) {
	q := newTriggerQueue()
	cfg := triggerBehavior{runMode: "serial", mergeMode: "by_task"}
	q.Enqueue("a", runOf(7, true), cfg, true)   // 先放一条出去占住位
	q.Enqueue("a", runOf(7, true), cfg, false)  // 同任务可合并
	q.Enqueue("a", runOf(8, true), cfg, false)  // 别的任务：不该被并进去
	q.Enqueue("a", runOf(7, false), cfg, false) // 同任务但不可合并：也不该被并

	next := q.Done("a", true)
	if len(next) != 1 {
		t.Fatalf("应放出 1 条（合并后的），实际 %d", len(next))
	}
	if next[0].taskID != 7 {
		t.Fatalf("合并后的 run 应属于任务 7，实际 %d", next[0].taskID)
	}
	// 剩下两条应还在队列里（逐条跑得出两次 Done）。
	n := 0
	for {
		b := q.Done("a", true)
		if len(b) == 0 {
			break
		}
		n += len(b)
	}
	if n != 2 {
		t.Fatalf("剩余应还有 2 条，实际又放出 %d 条", n)
	}
}

// TestTriggerQueueDoneCleansUpWhenIdle：agent 彻底空闲时三张表都要清掉，
// 否则队列会随见过的 agent 数无限增长。
func TestTriggerQueueDoneCleansUpWhenIdle(t *testing.T) {
	q := newTriggerQueue()
	q.Enqueue("a", runOf(0, false), serial(), true)
	q.Done("a", true)
	if _, ok := q.queue["a"]; ok {
		t.Fatal("空闲后 queue 里仍留着该 agent")
	}
	if _, ok := q.active["a"]; ok {
		t.Fatal("空闲后 active 里仍留着该 agent")
	}
	if _, ok := q.cfg["a"]; ok {
		t.Fatal("空闲后 cfg 里仍留着该 agent")
	}
}

// TestTriggerQueueCanceledDoesNotLaunch：调度器已取消时只入队、不启动，
// 也不占在跑计数——恢复后这些项仍在队列里。
func TestTriggerQueueCanceledDoesNotLaunch(t *testing.T) {
	q := newTriggerQueue()
	if got := q.Enqueue("a", runOf(0, false), serial(), false); len(got) != 0 {
		t.Fatalf("取消状态下不该启动，实际 %d", len(got))
	}
	if n := q.Active("a"); n != 0 {
		t.Fatalf("取消状态下不该占在跑计数，实际 %d", n)
	}
	if len(q.queue["a"]) != 1 {
		t.Fatalf("排队项应保留，实际 %d", len(q.queue["a"]))
	}
}
