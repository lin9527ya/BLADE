package server

import (
	"encoding/json"
	"testing"
)

// 待领队列倍数是“每轮现读”的设置：保存后 Manager 的生效值立即变化，
// 且 GET /api/settings 回显同一个值；非法倍数必须在写库前被拒。
func TestIntentQueueFactorSettingRoundTrip(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	prev, had, _ := s.m.pg.GetSetting(settingIntentQueueFactor)
	t.Cleanup(func() {
		if had {
			_ = s.m.pg.SetSetting(settingIntentQueueFactor, prev)
		} else {
			_, _ = s.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingIntentQueueFactor)
		}
	})
	if got := s.m.IntentQueueFactor(); got != 4 {
		t.Fatalf("default factor = %d, want 4", got)
	}
	if w := request("PUT", "/api/settings", `{"intent_queue_factor":5}`); w.Code != 200 {
		t.Fatalf("save factor: %d %s", w.Code, w.Body)
	}
	if got := s.m.IntentQueueFactor(); got != 5 {
		t.Fatalf("saved factor = %d, want 5", got)
	}
	w := request("GET", "/api/settings", "")
	if w.Code != 200 {
		t.Fatalf("get settings: %d", w.Code)
	}
	var payload struct {
		IntentQueueFactor int `json:"intent_queue_factor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.IntentQueueFactor != 5 {
		t.Fatalf("payload factor = %d, want 5", payload.IntentQueueFactor)
	}
	if w := request("PUT", "/api/settings", `{"intent_queue_factor":33}`); w.Code != 400 {
		t.Fatalf("out-of-range factor should be 400, got %d %s", w.Code, w.Body)
	}
	if got := s.m.IntentQueueFactor(); got != 5 {
		t.Fatalf("invalid save changed factor to %d", got)
	}
}
