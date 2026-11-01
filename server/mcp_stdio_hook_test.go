package server

import (
	"context"
	"os/exec"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/mcp"
	actool "github.com/lin9527ya/Norma/tool"
)

// MCP stdio 子进程过去由 SDK 直接 exec，绕过了 agent 的降权/环境清洗钩子——即
// "能配置 MCP 的 agent 等于能以服务端身份执行任意命令"。SDK 现在对 stdio 也调
// ApplyExecHook，这条用例把它钉住：SDK 一旦回退，这里会立刻红。
//
// 默认隔离关闭时 hardenAgentCmd 是 no-op，所以临时替换钩子是安全的；用完恢复，
// 不影响同包其它用例。
func TestMCPStdioSpawnAppliesExecHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stdio MCP 的降权钩子只在 unix 生效")
	}
	var calls atomic.Int32
	actool.SetExecHook(func(*exec.Cmd) { calls.Add(1) })
	t.Cleanup(func() { actool.SetExecHook(hardenAgentCmd) })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// /bin/true 会立刻退出：握手必然失败，但钩子是在 Start 之前调的。
	if c, err := mcp.NewStdioClient(ctx, "hook-test", "/bin/true", nil); err == nil {
		_ = c.Close()
	}
	if calls.Load() == 0 {
		t.Fatal("stdio MCP 子进程没有经过 ExecHook（降权/环境清洗被绕过）")
	}
}
