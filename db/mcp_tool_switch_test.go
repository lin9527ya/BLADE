package db

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestMCPToolSwitch 覆盖工具级开关与 schema 缓存（P25）的四条契约：
//
//	① 关掉的工具在缓存的三处读取里口径一致（MCPToolsDetailed / MCPDisabledTools / MCPToolSummary）；
//	② 重发现（SaveMCPTools 重建缓存）**不会**把人的开关翻回默认——这是这个开关最危险的失效形态；
//	③ 名字不在缓存里 → ErrMCPToolNotFound（API 据此 404，而不是静默成功或静默失败）；
//	④ 发现时抓到的 inputSchema 随缓存往返，重发现拿到新 schema 会更新（展开参数表靠它）。
func TestMCPToolSwitch(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	// 先清同名残留：复用库上上一次运行可能留下（t.Cleanup 里的删除在 defer d.Close() 之后
	// 才跑，未必生效）——这套 db 测试的既有惯例就是「先删再建」。
	if _, err := d.Exec(`DELETE FROM mcp_servers WHERE name=$1`, "mcp-switch-test"); err != nil {
		t.Fatalf("清残留: %v", err)
	}
	id, err := d.SaveMCP(&MCPServer{Name: "mcp-switch-test", Transport: "stdio", Command: "echo", Enabled: false})
	if err != nil {
		t.Fatalf("save mcp: %v", err)
	}
	t.Cleanup(func() { _ = d.DeleteMCP(id) })

	// seed：alpha 带一份 schema（参数 q），其余只有名字+描述。
	seed := func(qField string, names ...string) []MCPTool {
		out := make([]MCPTool, 0, len(names))
		for _, n := range names {
			t := MCPTool{Name: n, Description: "d-" + n}
			if n == "alpha" && qField != "" {
				t.Schema = json.RawMessage(`{"type":"object","properties":{"` + qField + `":{"type":"string"}}}`)
			}
			out = append(out, t)
		}
		return out
	}
	if err := d.SaveMCPTools(id, seed("q", "alpha", "beta")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 新缓存的工具默认启用（没人动过开关）。
	names, disabled, err := d.MCPToolSummary(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || disabled != 0 {
		t.Fatalf("新缓存应为 2 个工具、0 停用，实际 %v / %d", names, disabled)
	}

	if err := d.SetMCPToolEnabled(id, "alpha", false); err != nil {
		t.Fatalf("关掉 alpha: %v", err)
	}

	// ① 三处读取口径一致。
	off, err := d.MCPDisabledTools(id)
	if err != nil {
		t.Fatal(err)
	}
	if !off["alpha"] || off["beta"] {
		t.Fatalf("MCPDisabledTools 应只含 alpha: %v", off)
	}
	det, err := d.MCPToolsDetailed(id)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]bool{}
	for _, x := range det {
		byName[x.Name] = x.Enabled
	}
	if byName["alpha"] || !byName["beta"] {
		t.Fatalf("MCPToolsDetailed 的 enabled 不对: %+v", det)
	}
	if _, disabled, _ := d.MCPToolSummary(id); disabled != 1 {
		t.Fatalf("停用计数应为 1，实际 %d", disabled)
	}

	// ④ schema 随缓存往返（展开参数表的数据源）。
	if len(det) == 0 || !strings.Contains(string(det[0].Schema), `"q"`) {
		t.Fatalf("schema 应随缓存往返: %+v", det)
	}

	// ② 重发现：多出一个 gamma、alpha 的 schema 换成 q2，人的开关必须活下来。
	if err := d.SaveMCPTools(id, seed("q2", "alpha", "beta", "gamma")); err != nil {
		t.Fatalf("重发现: %v", err)
	}
	det, err = d.MCPToolsDetailed(id)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, x := range det {
		got[x.Name] = x.Enabled
	}
	if len(got) != 3 || got["alpha"] || !got["beta"] || !got["gamma"] {
		t.Fatalf("重发现后开关被翻回默认了: %+v", det)
	}
	// ④ 重发现拿到的新 schema 要落库（老 schema 不能粘住）。
	var alphaSchema string
	for _, x := range det {
		if x.Name == "alpha" {
			alphaSchema = string(x.Schema)
		}
	}
	if !strings.Contains(alphaSchema, `"q2"`) || strings.Contains(alphaSchema, `"q":`) {
		t.Fatalf("重发现应更新 schema: %s", alphaSchema)
	}

	// ③ 未知工具名报错（不静默成功）。
	if err := d.SetMCPToolEnabled(id, "nope", false); !errors.Is(err, ErrMCPToolNotFound) {
		t.Fatalf("未知工具应返回 ErrMCPToolNotFound，实际 %v", err)
	}

	// 再打开：计数回到 0。
	if err := d.SetMCPToolEnabled(id, "alpha", true); err != nil {
		t.Fatal(err)
	}
	if _, disabled, _ := d.MCPToolSummary(id); disabled != 0 {
		t.Fatalf("重新启用后不该还有停用项，实际 %d", disabled)
	}
}
