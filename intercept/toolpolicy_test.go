package intercept

import (
	"slices"
	"testing"

	"github.com/lin9527ya/blade/toolpolicy"
)

// 新增动作类工具只在 toolpolicy 登记一次；这条测试确保五个派生点没有漏接。
func TestToolpolicyRegistryWiring(t *testing.T) {
	if len(toolpolicy.Registry) == 0 {
		t.Fatal("toolpolicy.Registry is empty")
	}
	for _, entry := range toolpolicy.Registry {
		if entry.Class != toolpolicy.Action {
			continue
		}
		if !BuiltinActionTool(entry.Name) {
			t.Errorf("%s 未进入 builtinActionTools：停机开关/时间窗/范围门会漏掉它", entry.Name)
		}
		if entry.InterceptDefault && !slices.Contains(defaultEnabledTools, entry.Name) {
			t.Errorf("%s 标了 InterceptDefault 但不在 defaultEnabledTools", entry.Name)
		}
	}
}

// Surface 必须与拆分前的 shellCommandOf 完全一致，避免迁移改变 RoE 目标提取。
func TestToolpolicySurfaceMatchesLegacy(t *testing.T) {
	samples := map[string]string{
		"Bash":          `{"command":"nmap 10.0.0.1"}`,
		"run_execution": `{"command":"nuclei -u https://a.example"}`,
		"shell_open":    `{"command":"ssh root@10.0.0.5"}`,
		"shell_send":    `{"text":"cat /etc/passwd 10.0.0.5"}`,
		"loot_exec":     `{"host":"10.0.0.5","command":"id"}`,
		"foothold_exec": `{"host":"10.0.0.6","command":"whoami"}`,
		"tunnel_open":   `{"host":"10.0.0.7"}`,
		"loot_upload":   `{"host":"10.0.0.8","remote_path":"/tmp/x"}`,
		"loot_download": `{"host":"10.0.0.8","remote_path":"/etc/passwd"}`,
		"cve_exploit":   `{"target":"10.0.0.9","args":["--port","8080"]}`,
		"WebFetch":      `{"url":"https://a.example/x"}`,
		"http_request":  `{"url":"https://a.example/x"}`,
		"session_login": `{"url":"https://a.example/login"}`,
	}
	for name, input := range samples {
		got := toolpolicy.Surface(name, []byte(input))
		want := legacyShellCommandOf(name, []byte(input))
		if got != want {
			t.Errorf("%s Surface = %q, legacy = %q", name, got, want)
		}
		if got == "" {
			t.Errorf("%s 没有提取出目标（RoE 会 fail-open）", name)
		}
	}
}
