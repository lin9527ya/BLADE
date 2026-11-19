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
	if evs[1].Kind != "asset" || evs[1].Summary == "" {
		t.Fatalf("asset trigger lost fields: %+v", evs[1])
	}
}
