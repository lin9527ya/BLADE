package enrich

import (
	"testing"
	"time"
)

// 队列满必须计数：静默丢弃会让"资产没补全"看起来像目标本来就没信息。
func TestEnqueueCountsAndDrop(t *testing.T) {
	e := &Engine{jobs: make(chan job, 1), closed: make(chan struct{})}
	e.enqueue(job{jobDNS, 1, "a.example"})
	e.enqueue(job{jobDNS, 2, "b.example"}) // 队列满
	st := e.Stats()
	if st.Enqueued != 2 || st.Dropped != 1 || st.QueueLen != 1 {
		t.Fatalf("stats = %+v, want enqueued=2 dropped=1 queue=1", st)
	}
}

// 冷却键必须回收：长任务里每个资产/站点都会留一条，只增不减会持续涨内存。
func TestSweepCooldownDropsExpired(t *testing.T) {
	e := &Engine{jobs: make(chan job, 1), closed: make(chan struct{})}
	e.cool.Store("d:1", time.Now().Add(-2*cooldown))
	e.cool.Store("d:2", time.Now())
	e.sweepCooldown()
	if _, ok := e.cool.Load("d:1"); ok {
		t.Fatal("过期冷却键未回收")
	}
	if _, ok := e.cool.Load("d:2"); !ok {
		t.Fatal("未过期冷却键被误删")
	}
}
