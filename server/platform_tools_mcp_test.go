package server

import (
	"testing"

	"github.com/lin9527ya/blade/intercept"
)

// TestMCPCreateDefaultsToDisabledForStdio 钉住一条安全默认：由模型发起的
// create_mcp 不该顺手把一条 **stdio 命令** 跑起来。
//
// stdio 传输的 command/args 就是一条任意命令，启用即等于让平台以服务端身份启动它
// （修复前连降权/环境清洗都绕过，见 Norma v0.5.6）。所以默认落配置不启用；
// 显式传 enabled=true 才启动。HTTP/SSE 只是发请求，默认仍按调用方意愿启用。
func TestMCPCreateDefaultsToDisabledForStdio(t *testing.T) {
	cases := []struct {
		name    string
		in      mcpToolInput
		wantOn  bool
		wantWhy string
	}{
		{"stdio 默认不启用", mcpToolInput{Name: "x", Transport: "stdio", Command: "/bin/sh"}, false, "stdio 命令默认不得被执行"},
		{"stdio 显式启用", mcpToolInput{Name: "x", Transport: "stdio", Command: "npx", Enabled: boolPtr(true)}, true, "显式 enabled=true 应被尊重"},
		{"stdio 显式关闭", mcpToolInput{Name: "x", Transport: "stdio", Command: "npx", Enabled: boolPtr(false)}, false, ""},
		{"http 默认启用", mcpToolInput{Name: "x", Transport: "http", URL: "https://example.com/mcp"}, true, "HTTP 传输不 spawn 进程"},
		{"sse 默认启用", mcpToolInput{Name: "x", Transport: "sse", URL: "https://example.com/sse"}, true, "SSE 传输不 spawn 进程"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in.toDB().Enabled; got != c.wantOn {
				t.Fatalf("Enabled=%v，期望 %v（%s）", got, c.wantOn, c.wantWhy)
			}
		})
	}
}

// TestMCPMutationToolsAreActionTools 钉住：MCP / 自定义工具的配置写入必须算"动作"，
// 否则停机开关与作业时间窗管不住它们（它们能把一条任意命令变成平台的常驻能力）。
func TestMCPMutationToolsAreActionTools(t *testing.T) {
	for _, key := range []string{"create_mcp", "update_mcp", "create_custom_tool", "update_custom_tool"} {
		if !actionToolForTest(key) {
			t.Fatalf("%s 未登记为动作类工具：停机开关与 RoE 范围门对它不生效", key)
		}
	}
	// 反向对照：读类工具不该被误伤。
	if actionToolForTest("get_assets") {
		t.Fatal("读类工具被误判为动作类")
	}
}

func boolPtr(b bool) *bool { return &b }

func actionToolForTest(key string) bool { return intercept.BuiltinActionTool(key) }
