package intercept

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 这两条用例钉住「动作类工具的边界必须覆盖自定义/MCP」：
//  1. 停机开关先于目标提取——抽不出目标的动作类工具也要停；
//  2. 自定义/MCP 工具入参（以及自定义工具的 exec 规格）里的目标要进 RoE 判定；
//  3. 读类内置工具不受影响（停的是动手，不是看）。

func TestKillSwitchCoversCustomAndMCPTools(t *testing.T) {
	d := killSwitchDB(t)
	t.Setenv(EnvKillSwitch, "")
	defer setSetting(t, d, SettingScopeMode, "off")()
	defer setSetting(t, d, SettingScopeHours, "")()
	defer setSetting(t, d, SettingKillSwitch, "")()
	i := New(d)

	const key = "acc_custom_scan"
	if err := d.CreateCustomTool(&db.Tool{
		Key: key, Description: "test", Schema: json.RawMessage(`{"type":"object"}`),
		Enabled: true, Kind: "command",
		Exec: json.RawMessage(`{"command":"nmap {{args}}"}`),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM tools WHERE key=$1`, key) })

	// 未停机：动作类工具照常放行。
	if _, hit := i.CheckScope(context.Background(), 1, key, json.RawMessage(`{"args":"10.0.0.5"}`)); hit {
		t.Fatal("停机未启用时不该拦")
	}

	st := KillSwitchReceipt{On: true, At: time.Now().Format(time.RFC3339), Reason: "测试停机"}
	if err := WriteKillSwitch(d, st); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		tool  string
		input string
	}{
		{"自定义命令工具", key, `{"args":"10.0.0.5"}`},
		{"MCP 工具", "mcp__browser__navigate", `{"url":"https://example.com"}`},
		{"目标抽不出的动作工具", "Write", `{"file_path":"/tmp/x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dec, hit := i.CheckScope(context.Background(), 1, tc.tool, json.RawMessage(tc.input))
			if !hit {
				t.Fatalf("%s 绕过了停机开关", tc.tool)
			}
			if dec.Action != "deny" || !strings.Contains(dec.RuleName, "停机开关") {
				t.Fatalf("应硬拒并点明停机，得到 %+v", dec)
			}
		})
	}

	// 读类工具仍然放行。
	if _, hit := i.CheckScope(context.Background(), 1, "graph_overview", json.RawMessage(`{}`)); hit {
		t.Fatal("读类工具不该被停机开关拦住")
	}
}

func TestScopeSeesTargetsInCustomAndMCPTools(t *testing.T) {
	d := killSwitchDB(t)
	t.Setenv(EnvKillSwitch, "")
	task, err := d.CreateTask("acc-action-gate", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, task.ExplorationID)
	})
	if _, err := d.Assets().AddAgentScope(task.ID, "root_domain", "in-scope.example.com", "test", "manual"); err != nil {
		t.Fatal(err)
	}
	defer setSetting(t, d, SettingScopeMode, "deny")()
	defer setSetting(t, d, SettingScopeHours, "")()
	defer setSetting(t, d, SettingKillSwitch, "")()
	i := New(d)

	const key = "acc_custom_http"
	if err := d.CreateCustomTool(&db.Tool{
		Key: key, Description: "test", Schema: json.RawMessage(`{"type":"object"}`),
		Enabled: true, Kind: "http",
		Exec: json.RawMessage(`{"url":"https://evil.other.net/api"}`),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM tools WHERE key=$1`, key) })

	ctx := context.Background()
	// 目标只在 exec 规格里（入参没写 URL）——之前完全看不见，现在要拦。
	if dec, hit := i.CheckScope(ctx, task.ID, key, json.RawMessage(`{"args":"x"}`)); !hit || dec.Action != "deny" {
		t.Fatalf("自定义工具的 exec 目标应进 RoE 判定，得到 hit=%v dec=%+v", hit, dec)
	}
	// 目标在任意入参字段里（MCP 的 schema 不固定）。
	if dec, hit := i.CheckScope(ctx, task.ID, "mcp__browser__navigate", json.RawMessage(`{"url":"https://evil.other.net/x"}`)); !hit || dec.Action != "deny" {
		t.Fatalf("MCP 入参里的越界目标应被拦，得到 hit=%v dec=%+v", hit, dec)
	}
	// 范围内目标照常放行。
	if _, hit := i.CheckScope(ctx, task.ID, "mcp__browser__navigate", json.RawMessage(`{"url":"https://api.example.com/x"}`)); hit {
		t.Fatal("范围内目标不该拦")
	}
	// 读类工具带越界域名也不该被范围门误伤（它不是动作）。
	if _, hit := i.CheckScope(ctx, task.ID, "list_assets", json.RawMessage(`{"dsl":"domain=evil.other.net"}`)); hit {
		t.Fatal("读类工具不该被范围门拦住")
	}
}
