package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestScheduleSpecRoundTripKeepsHiddenFields(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	var profiles []int64
	for i := 0; i < 2; i++ {
		id, err := s.m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("schedule-fixture-%d", i), Format: "openai", Model: "fixture", BaseURL: "http://127.0.0.1:9", APIKey: "synthetic-not-a-real-key", PoolExclude: true})
		if err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, id)
		t.Cleanup(func() { _ = s.m.pg.DeleteProfile(id) })
	}
	off := false
	spec := scheduleSpec{Description: "roundtrip fixture", Goal: "no target activity", CompanyIDs: []int64{99999990}, LLMProfileIDs: profiles, TimeoutSeconds: 42, AssetScopePolicy: "exact", CoverageEnabled: &off, SeedFirstIntent: &off,
		InterceptRules: []taskInterceptRuleReq{{Enabled: true, Action: "block", Kind: "exact_domain", Pattern: "fixture.invalid", Note: "keep"}}}
	body, _ := json.Marshal(map[string]any{"name": "roundtrip", "enabled": true, "task_spec": spec, "frequency": map[string]any{"kind": "every_hours", "every_hours": 1}})
	w := request(http.MethodPost, "/api/task-schedules", string(body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	var created taskScheduleDTO
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.DeleteTaskSchedule(created.ID) })
	// This is the simple editor's complete snapshot payload, with only the
	// schedule name/frequency changed. The API must retain all supplied fields.
	patch, _ := json.Marshal(map[string]any{"name": "renamed", "frequency": map[string]any{"kind": "every_hours", "every_hours": 2}, "task_spec": created.TaskSpec})
	w = request(http.MethodPatch, fmt.Sprintf("/api/task-schedules/%d", created.ID), string(patch))
	if w.Code != 200 {
		t.Fatalf("patch %d %s", w.Code, w.Body)
	}
	var updated taskScheduleDTO
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated.TaskSpec, spec) {
		t.Fatalf("spec lost: %#v != %#v", updated.TaskSpec, spec)
	}
}
