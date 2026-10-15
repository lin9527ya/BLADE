package agent

import (
	"context"
	"encoding/json"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
)

// TestBuiltinToolSeeds ensures the catalog builds from a nil-store ToolSet without
// panicking, has stable keys, unions agent bindings, and carries real descriptions.
func TestBuiltinToolSeeds(t *testing.T) {
	seeds := BuiltinToolSeeds()
	if len(seeds) == 0 {
		t.Fatal("no seeds")
	}
	byKey := map[string]ToolSeed{}
	for _, s := range seeds {
		if s.Key == "" || s.Desc == "" {
			t.Errorf("seed %q missing key/desc", s.Key)
		}
		byKey[s.Key] = s
	}
	// record_fact is bound to worker (also mainagent, which can log confirmed facts).
	rf, ok := byKey["record_fact"]
	hasWorker := false
	for _, a := range rf.Agents {
		if a == "worker" {
			hasWorker = true
		}
	}
	if !ok || !hasWorker {
		t.Errorf("record_fact agents = %v, want to include worker", rf.Agents)
	}
	// add_task_scope is bound to the planner (deliberate scope widening).
	if ts, ok := byKey["add_task_scope"]; !ok || len(ts.Agents) == 0 {
		t.Errorf("add_task_scope not seeded / has no agent binding: %v", ts.Agents)
	}
	// SDK generic tools (incl. sleep, now part of DefaultTools) are deliberately NOT
	// seeded — every agent owns them; they flow through ToolResolve untouched.
	for _, k := range []string{"Bash", "Read", "Write", "Edit", "Grep", "sleep"} {
		if _, ok := byKey[k]; ok {
			t.Errorf("SDK tool %q should not be seeded", k)
		}
	}
}

// TestDecorateToolInjectsDefaults verifies a schema "default" fills a missing param
// before the underlying handler runs, and an explicitly-provided value is kept.
func TestDecorateToolInjectsDefaults(t *testing.T) {
	var seen map[string]any
	base := actool.Build(actool.Spec{
		Name: "probe", Description: "orig",
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			_ = json.Unmarshal(in, &seen)
			return actool.Text("ok"), nil
		},
	})
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"limit": map[string]any{"type": "integer", "description": "n", "default": float64(3)},
		"q":     map[string]any{"type": "string", "description": "query"},
	}}
	dec := DecorateTool(base, "new desc", schema)
	if dec.Description() != "new desc" {
		t.Errorf("description = %q", dec.Description())
	}

	// limit omitted → default 3 injected; q kept.
	if _, err := dec.Call(context.Background(), json.RawMessage(`{"q":"x"}`), nil); err != nil {
		t.Fatal(err)
	}
	if seen["limit"] != float64(3) || seen["q"] != "x" {
		t.Errorf("injected = %v, want limit=3 q=x", seen)
	}

	// limit provided → default does NOT override.
	if _, err := dec.Call(context.Background(), json.RawMessage(`{"limit":9}`), nil); err != nil {
		t.Fatal(err)
	}
	if seen["limit"] != float64(9) {
		t.Errorf("limit = %v, want 9 (no override)", seen["limit"])
	}
}

// TestDomainToolCatalogCoversAll pins the catalog completeness invariant：经
// AllDomainTools()（服务器为非任务 agent 构建工具绑定注册表用的并集）可达的每个
// 工具，必须出现在 BuiltinToolSeeds()（用户在 UI 上可勾选绑定的目录）里——目录之外的
// 工具是「静默不可绑」的：新工具加进了构造器却漏进目录，用户永远看不到它。
func TestDomainToolCatalogCoversAll(t *testing.T) {
	ts := NewToolSet(nil, "")
	seeds := map[string]bool{}
	for _, seed := range BuiltinToolSeeds() {
		seeds[seed.Key] = true
	}
	if len(seeds) == 0 {
		t.Fatal("BuiltinToolSeeds 为空：目录构造已失效")
	}
	for _, tool := range ts.AllDomainTools() {
		if !seeds[tool.Name()] {
			t.Errorf("工具 %q 可经 AllDomainTools 触达但不在 BuiltinToolSeeds 目录里：加到 builtinToolsByAgent 的某个 agent 列表，否则用户无法绑定它", tool.Name())
		}
	}
}

// TestDefaultUnboundNotBound pins the escape hatch itself：defaultUnbound 里的工具
// 在目录（BuiltinToolSeeds）里必须绑定为空——注意它仍可留在某个 agent 的 base 工具集
// 里（如 goal_met 在 PlannerTools：让 seed 能构造它拿到 desc/schema，用户手动绑回后
// 运行时才留得住），被判定的口径是 seed 的 Agents 列表，不是 base 成员资格。
func TestDefaultUnboundNotBound(t *testing.T) {
	for _, seed := range BuiltinToolSeeds() {
		if defaultUnbound[seed.Key] && len(seed.Agents) > 0 {
			t.Errorf("工具 %q 在 defaultUnbound 里但在目录中仍绑定 %v：应强制为空（须显式 opt-in）", seed.Key, seed.Agents)
		}
	}
}
