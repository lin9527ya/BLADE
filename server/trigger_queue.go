package server

import (
	"math"
	"sync"
)

// triggerQueue 是 P3 触发器的运行队列：按 agent 分桶，每个 agent 受自己的策略约束。
//
// 它只管三件事——**排队、按合并策略取出、记在跑数**。启动 goroutine 与真正执行由
// 调用方（Server）做，队列因此不依赖 Server 的任何其他部分，可以脱离它单独测试。
//
// 锁纪律：状态只在 mu 下读写；取出的一批在**锁外**启动，避免在持锁期间起 goroutine。
type triggerQueue struct {
	mu     sync.Mutex
	queue  map[string][]triggeredRun
	active map[string]int
	cfg    map[string]triggerBehavior
}

func newTriggerQueue() *triggerQueue {
	return &triggerQueue{
		queue:  map[string][]triggeredRun{},
		active: map[string]int{},
		cfg:    map[string]triggerBehavior{},
	}
}

// Enqueue 追加一次触发。pump=false 表示"只入队、先别启动"（调度器已取消的情形）；
// 返回本次应当立刻启动的批次——启动由调用方在锁外做。
func (q *triggerQueue) Enqueue(agentKey string, item triggeredRun, cfg triggerBehavior, pump bool) []triggeredRun {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cfg[agentKey] = cfg
	q.queue[agentKey] = append(q.queue[agentKey], item)
	if !pump {
		return nil
	}
	return q.takeLocked(agentKey, cfg)
}

// Done 记录一次 run 结束，返回为补满空位应启动的批次（pump=false 时返回 nil）。
// 该 agent 彻底空闲时把它的三张表清掉——否则队列会随见过的 agent 数无限增长。
func (q *triggerQueue) Done(agentKey string, pump bool) []triggeredRun {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active[agentKey]--
	if q.active[agentKey] <= 0 && len(q.queue[agentKey]) == 0 {
		delete(q.active, agentKey)
		delete(q.queue, agentKey)
		delete(q.cfg, agentKey)
		return nil
	}
	if !pump {
		return nil
	}
	return q.takeLocked(agentKey, q.cfg[agentKey])
}

// Active 返回某 agent 当前在跑的数量（测试与诊断用）。
func (q *triggerQueue) Active(agentKey string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.active[agentKey]
}

// takeLocked 按策略取出最多到并发上限的一批 run，并累加在跑计数。Caller holds mu.
func (q *triggerQueue) takeLocked(agentKey string, cfg triggerBehavior) []triggeredRun {
	limit := triggerLimit(cfg)
	var batch []triggeredRun
	for q.active[agentKey] < limit && len(q.queue[agentKey]) > 0 {
		batch = append(batch, q.nextLocked(agentKey, cfg))
		q.active[agentKey]++
	}
	return batch
}

// triggerLimit 是某策略下每 agent 的在跑上限：serial → 1；parallel → maxParallel
// （<=0 表示用户在设置里选了"不限"，见前端 types.ts 的 trigger_max_parallel）。
func triggerLimit(cfg triggerBehavior) int {
	if cfg.runMode != "parallel" {
		return 1
	}
	if cfg.maxParallel <= 0 {
		return math.MaxInt
	}
	return cfg.maxParallel
}

// nextLocked 按合并策略取出下一批（可能把多条并成一条）。Caller holds mu.
//
//	parallel / none → one head item, no merge
//	by_task         → head + all queued same-task mergeable fires merged into one
//	all             → the ENTIRE queue merged into one run (regardless of task/type)
func (q *triggerQueue) nextLocked(agentKey string, cfg triggerBehavior) triggeredRun {
	qd := q.queue[agentKey]
	if cfg.runMode == "parallel" || cfg.mergeMode == "none" {
		q.queue[agentKey] = qd[1:]
		return qd[0]
	}
	if cfg.mergeMode == "all" {
		q.queue[agentKey] = qd[:0:0]
		return mergeAllRuns(qd)
	}
	// by_task: head + same-task mergeable fires.
	head := qd[0]
	if !head.mergeable || head.taskID == 0 {
		q.queue[agentKey] = qd[1:]
		return head
	}
	group := []triggeredRun{head}
	rest := qd[:0:0] // keep non-matching items in order
	for _, it := range qd[1:] {
		if it.mergeable && it.taskID == head.taskID {
			group = append(group, it)
		} else {
			rest = append(rest, it)
		}
	}
	q.queue[agentKey] = rest
	return mergeTriggeredRuns(group)
}
