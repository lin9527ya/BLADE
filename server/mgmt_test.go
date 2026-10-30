package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// TestMgmtAPI exercises the PostgreSQL-backed management API through the real mux.
func TestMgmtAPI(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping management API test", err)
	}
	if m.pg == nil {
		t.Skip("postgres unavailable — skipping management API test")
	}
	// 必须在用例结束时关掉 Manager：不关的话它的连接池和 New 起的后台 goroutine
	// 会一直活到测试进程退出，后面的用例（尤其是通知投递）会与它共享同一个测试库。
	t.Cleanup(func() { _ = m.Close() })
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
	h := s.Handler()
	tok, err := signJWT(s.auth.signKey.get())
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	do := func(method, path string, body any) (int, map[string]any) {
		var r *http.Request
		if body != nil {
			b, _ := json.Marshal(body)
			r = httptest.NewRequest(method, path, bytes.NewReader(b))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// agents list — must include at least the seeded builtins
	code, out := do("GET", "/api/agents", nil)
	if code != 200 {
		t.Fatalf("GET agents: %d", code)
	}
	if ags, _ := out["agents"].([]any); len(ags) < 5 {
		t.Fatalf("want >=5 agents (seeded builtins), got %d", len(ags))
	}

	// invalid template var → 400 (catalog whitelist)
	code, out = do("PUT", "/api/agents/planner/prompt", map[string]string{"template": "hi {{.Nope}}"})
	if code != 400 {
		t.Fatalf("bad var should be 400, got %d (%v)", code, out)
	}

	// valid template using seeded catalog vars → 200
	code, _ = do("PUT", "/api/agents/planner/prompt", map[string]string{"template": "你是规划者，目标：{{.Goal}}，摘要 {{.AssetSummary}}"})
	if code != 200 {
		t.Fatalf("valid prompt save: %d", code)
	}

	// preview renders with catalog examples
	code, out = do("POST", "/api/agents/planner/prompt/preview", map[string]any{})
	if code != 200 {
		t.Fatalf("preview: %d", code)
	}
	if rendered, _ := out["rendered"].(string); rendered == "" || rendered == "你是规划者，目标：{{.Goal}}，摘要 {{.AssetSummary}}" {
		t.Fatalf("preview did not substitute: %q", out["rendered"])
	}

	// mcp create → visible to planner → resource-side sees planner → delete clears it
	// Remove any leftover from prior run to keep the test idempotent.
	m.pg.Exec(`DELETE FROM mcp_servers WHERE name = 't-itest'`)
	code, out = do("POST", "/api/mcp", map[string]any{"name": "t-itest", "transport": "stdio", "command": "x", "enabled": true})
	if code != 200 {
		t.Fatalf("create mcp: %d", code)
	}
	mid := int64(out["id"].(float64))

	ag, _ := m.pg.GetAgentByKey("planner")
	code, _ = do("PUT", "/api/agents/planner/visibility", map[string]any{"mcp": []int64{mid}, "skill": []int64{}})
	if code != 200 {
		t.Fatalf("set visibility: %d", code)
	}
	code, out = do("GET", "/api/visibility/mcp/"+itoaTest(mid), nil)
	if code != 200 {
		t.Fatalf("resource visibility: %d", code)
	}
	if agents, _ := out["agents"].([]any); len(agents) != 1 || agents[0].(string) != itoaTest(ag.ID) {
		t.Fatalf("resource-side should list planner, got %v", out["agents"])
	}

	// cleanup
	do("DELETE", "/api/mcp/"+itoaTest(mid), nil)
	m.pg.Exec(`DELETE FROM agent_prompts WHERE agent_id=$1`, ag.ID)
	m.pg.Exec(`UPDATE agents SET current_prompt_id=NULL WHERE id=$1`, ag.ID)
}

func itoaTest(n int64) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestAgentPayloadCarriesSkillAlways 钉住一个契约：GET /api/agents/{key} 的 visibility 必须
// 带上 skill_always（常驻技能）。agent 编辑器据此渲染"常驻"勾选，并在保存可见性时整份回传；
// 一旦这个字段缺席，前端恒为 []，保存任何一次可见性都会把该 agent 的常驻标记**静默清空**
// （唯一返回它的 /agents/{key}/visibility 那条路由，前端从未调用过——这个坑就是这么来的）。
func TestAgentPayloadCarriesSkillAlways(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping", err)
	}
	if m.pg == nil {
		t.Skip("postgres unavailable — skipping")
	}
	// 见 TestMgmtAPI：Manager 不关就是池子 + 后台 goroutine 泄漏。这里先注册，
	// LIFO 保证它晚于下面那些还要用 m.pg 的清理函数执行。
	t.Cleanup(func() { _ = m.Close() })
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
	h := s.Handler()
	tok, err := signJWT(s.auth.signKey.get())
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body any) (int, map[string]any) {
		var r *http.Request
		if body != nil {
			b, _ := json.Marshal(body)
			r = httptest.NewRequest(method, path, bytes.NewReader(b))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	const key = "always_contract_probe"
	_, _ = do("DELETE", "/api/agents/"+key, nil)
	if code, out := do("POST", "/api/agents", map[string]string{"key": key, "name": "常驻契约探针"}); code != 200 {
		t.Fatalf("create agent: %d %v", code, out)
	}
	a, err := m.pg.GetAgentByKey(key)
	if err != nil || a == nil {
		t.Fatalf("agent row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = do("DELETE", "/api/agents/"+key, nil)
		_ = m.pg.SetAgentSkillBindings(a.ID, nil)
	})
	if err := m.pg.SetAgentSkillBindings(a.ID, []db.SkillBinding{{Name: "always-rules", Always: true}}); err != nil {
		t.Fatal(err)
	}

	// ① 详情载荷必须带 skill_always（编辑器读的就是它）。
	code, out := do("GET", "/api/agents/"+key, nil)
	if code != 200 {
		t.Fatalf("get agent: %d", code)
	}
	vis, _ := out["visibility"].(map[string]any)
	if vis == nil {
		t.Fatalf("payload missing visibility: %v", out)
	}
	got, ok := vis["skill_always"].([]any)
	if !ok || len(got) != 1 || got[0] != "always-rules" {
		t.Fatalf("visibility.skill_always 缺失或不对（编辑器会因此清空常驻）: %#v", vis["skill_always"])
	}

	// ② 与专门那条路由同源（两边必须一致，否则"读一处写另一处"照样丢）。
	_, vout := do("GET", "/api/agents/"+key+"/visibility", nil)
	if vout["skill_always"] == nil || len(vout["skill_always"].([]any)) != 1 {
		t.Fatalf("visibility 路由不一致: %v", vout)
	}

	// ③ 按编辑器的方式整份回传（带上读到的 skill_always）→ 常驻保留。
	code, _ = do("PUT", "/api/agents/"+key+"/visibility",
		map[string]any{"mcp": []int64{}, "skill": []string{"always-rules"}, "skill_always": []string{"always-rules"}})
	if code != 200 {
		t.Fatalf("set visibility: %d", code)
	}
	if names, err := m.pg.AgentAlwaysSkillNames(a.ID); err != nil || len(names) != 1 || names[0] != "always-rules" {
		t.Fatalf("整份回传后常驻应保留: %v %v", names, err)
	}
}
