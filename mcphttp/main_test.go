package mcphttp

import (
	"os"
	"testing"
)

// TestMain 打开本机回环的逃生口：本包的用例全部用 127.0.0.1 上的 httptest 假
// MCP server，而出站守卫默认拒绝环回（防 SSRF 打到同机服务与云元数据）。
// 与 server 包的 notify 夹具同一做法。
//
// 守卫本身"默认拒绝"的行为由 ssrf_test.go 覆盖——那里的用例在关掉这个开关的
// 前提下断言拒绝，所以不会被这里的放行掩盖。
func TestMain(m *testing.M) {
	os.Setenv(AllowLocalEnv, "1")
	os.Exit(m.Run())
}
