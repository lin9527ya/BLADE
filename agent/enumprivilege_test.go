package agent

import (
	"strings"
	"testing"
)

// enum_privilege 的 profile 是闭集且只读：不允许出现凭据抓取/持久化工具。
func TestPrivilegeProfilesAreReadOnly(t *testing.T) {
	want := []string{"linux", "windows", "network"}
	if len(privilegeProfiles) != len(want) {
		t.Fatalf("profile 集合 = %v, want %v", privilegeProfiles, want)
	}
	banned := []string{"mimikatz", "sekurlsa", "lsadump", "procdump", "comsvcs", "nanodump", "dpapi", "/etc/shadow", "sam dump"}
	for _, name := range want {
		cmd, ok := privilegeProfiles[name]
		if !ok || strings.TrimSpace(cmd) == "" {
			t.Fatalf("profile %q 缺失或为空", name)
		}
		lower := strings.ToLower(cmd)
		for _, token := range banned {
			if strings.Contains(lower, token) {
				t.Errorf("profile %q 含有禁止的凭据抓取/持久化内容 %q", name, token)
			}
		}
	}
}
