package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestCacheDiagnosticsExposeOnlyStructuralSurfaces(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	task, err := s.m.CreateTask(fmt.Sprintf("surface-api-%d", time.Now().UnixNano()), "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	if err := s.m.pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if err := s.m.pg.InsertLLMUsage(&db.LLMUsage{TaskID: task.ID, Worker: "planner", Model: "fixture", Status: "ok", InputTokens: 10, Surface: &db.RequestSurface{Observed: true, Model: "fixture", Format: "anthropic", BuildVersion: "test", SystemSHA: "sys", ToolsSHA: "tools", ParametersSHA: "params", EndpointSHA: "endpoint"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM llm_usage WHERE task_id=$1`, task.ID) })
	w := request(http.MethodGet, "/api/diagnostics/cache?task="+task.ID, "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got struct {
		Surfaces []db.RequestSurfaceSummary `json:"surfaces"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Surfaces) != 1 || got.Surfaces[0].Worker != "planner" || got.Surfaces[0].SystemSHA != "sys" || got.Surfaces[0].Calls != 1 {
		t.Fatalf("surfaces=%+v", got.Surfaces)
	}
	// 直接断言线上 JSON 键名：bench/scheduler.py 按 snake_case 读取，而 Go 结构体
	// 反序列化对 PascalCase/蛇形两种键名都能成功，只有原始键能防止归因契约回归。
	var raw struct {
		Surfaces []map[string]any `json:"surfaces"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Surfaces) != 1 {
		t.Fatalf("raw surfaces=%v", raw.Surfaces)
	}
	for _, key := range []string{
		"worker", "model", "format", "build_version", "system_sha", "tools_sha",
		"parameters_sha", "endpoint_sha", "calls", "changed_calls", "avg_input",
		"avg_output", "cache_hit_tokens",
	} {
		if _, ok := raw.Surfaces[0][key]; !ok {
			t.Fatalf("missing json key %q: %s", key, w.Body)
		}
	}
}
