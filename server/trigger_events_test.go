package server

import "testing"

func TestNotifyFactAndAssetQueueSemanticTriggers(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	task.NotifyFact("contradiction", 7, 11, "新观察推翻旧结论", []int64{3, 4})
	task.NotifyAsset("insert_assets 新登记 2 个资产")
	evs := task.drainTriggers()
	if len(evs) != 2 {
		t.Fatalf("want 2 triggers, got %+v", evs)
	}
	if evs[0].Kind != "contradiction" || evs[0].IntentID != 7 || evs[0].FactID != 11 || len(evs[0].AssetIDs) != 2 {
		t.Fatalf("fact trigger lost fields: %+v", evs[0])
	}
	if evs[1].Kind != "assets_batch" || evs[1].Count != 1 || evs[1].Summary == "" {
		t.Fatalf("asset trigger should be coalesced into assets_batch: %+v", evs[1])
	}
}

// 普通 fact 不逐条唤醒；drain 时合成一条 facts_batch，完整内容仍在 recent_facts。
func TestLowPriorityFactsBatchUntilDrain(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	for i := 0; i < 5; i++ {
		task.NotifyFact("fact", 1, int64(i+1), "普通事实", nil)
	}
	if got := len(task.notify); got != 0 {
		t.Fatalf("普通 fact 不应每条约唤醒 planner，notify=%d", got)
	}
	evs := task.drainTriggers()
	if len(evs) != 1 || evs[0].Kind != "facts_batch" || evs[0].Count != 5 {
		t.Fatalf("drain 应产出一条 5 条的 facts_batch：%+v", evs)
	}
}

// 定时器到点也冲刷待合并的普通 fact/asset。
func TestFlushLowPriorityBatch(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	task.NotifyFact("fact", 1, 1, "普通事实", nil)
	task.NotifyAsset("资产变更")
	task.flushLowPriority()
	evs := task.drainTriggers()
	if len(evs) != 2 || evs[0].Kind != "facts_batch" || evs[1].Kind != "assets_batch" {
		t.Fatalf("flush 应产出 fact/asset 两个批次：%+v", evs)
	}
}

// 高价值事件保持即时；队列上限只丢弃最旧事件，并在 drain 时显式告知。
func TestTriggerQueueCapAndDroppedNotice(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	for i := 0; i < 100; i++ {
		task.NotifyFinding(int64(i+1), "finding")
	}
	evs := task.drainTriggers()
	if len(evs) != maxPendingTriggers+1 {
		t.Fatalf("want %d events (cap + dropped notice), got %d", maxPendingTriggers+1, len(evs))
	}
	last := evs[len(evs)-1]
	if last.Kind != "triggers_dropped" || last.Count != 100-maxPendingTriggers {
		t.Fatalf("dropped notice mismatch: %+v", last)
	}
}
