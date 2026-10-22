package config

import (
	"os"
	"path/filepath"
	"testing"
)

// config.json 里有数据库口令：启动时把过宽的权限收成 0600（早期 install.sh 用默认
// umask 落盘会得到 0644，同机其它用户能直接读走 DSN）。
func TestTightenConfigPerms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tightenConfigPerms(p)
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("权限应收紧为 0600，得到 %04o", perm)
	}
	// 已经 0600 时是 no-op，不应报错或改回。
	tightenConfigPerms(p)
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
		t.Fatalf("第二次调用把权限改成了 %04o", info.Mode().Perm())
	}
}
