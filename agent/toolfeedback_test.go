package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"

	"github.com/lin9527ya/blade/agent"
)

type fakeTool struct {
	name string
	res  actool.Result
}

func (f *fakeTool) Name() string                           { return f.name }
func (f *fakeTool) Description() string                    { return "" }
func (f *fakeTool) InputSchema() map[string]any            { return map[string]any{} }
func (f *fakeTool) IsReadOnly(json.RawMessage) bool        { return false }
func (f *fakeTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (f *fakeTool) CheckPermissions(context.Context, json.RawMessage, permission.Context) permission.Decision {
	return permission.Allowed()
}
func (f *fakeTool) Prompt() string { return "" }
func (f *fakeTool) Call(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
	return f.res, nil
}

// TestFailureHintOnlyOnErrors 钉住两条：失败结果带上可执行的下一步；
// **成功**结果即使正文里出现 "permission denied"（很可能是扫到的东西）也不加提示。
func TestFailureHintOnlyOnErrors(t *testing.T) {
	cases := []struct {
		name string
		res  actool.Result
		want string
	}{
		{"not found", actool.Errorf("bash: foo: command not found\n\n[exit code 127]"), "命令不存在"},
		{"perm", actool.Errorf("open /etc/shadow: permission denied\n\n[exit code 1]"), "权限不足"},
		{"refused", actool.Errorf("curl: (7) Failed to connect: Connection refused"), "连接被拒"},
		{"timeout", actool.Errorf("context deadline exceeded: timed out"), "执行超时"},
		{"generic", actool.Errorf("boom\n\n[exit code 2]"), "非零退出"},
		{"success mentions keyword", actool.Text("响应体里含有 permission denied 字样"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool := agent.WithFailureHint(&fakeTool{name: "Bash", res: c.res})
			got, err := tool.Call(context.Background(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			text := got.Flatten()
			if c.want == "" {
				if strings.Contains(text, "提示：") {
					t.Fatalf("成功的工具结果不该加失败提示，实际：%s", text)
				}
				return
			}
			if !strings.Contains(text, c.want) {
				t.Fatalf("失败结果应包含 %q，实际：%s", c.want, text)
			}
			// 原文必须保留：提示是**追加**，不是替换。
			if !strings.Contains(text, c.res.Flatten()) {
				t.Fatalf("追加提示时不该丢掉原始输出，实际：%s", text)
			}
		})
	}
}
