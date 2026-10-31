package server

import (
	"testing"
)

// TestBinaryCleanupOnlyDeletesUpdateResidue 钉住一条破坏性边界的白名单。
//
// 「更新残留（旧二进制）」分类的根目录是**可执行文件所在目录**，jwt.key /
// config.json / data / skills / start.sh / 正在运行的二进制都住在那里。修复前它
// 扫描整目录并 os.RemoveAll 每一项，操作者一旦给这个分类设了保留天数，被删掉的
// 就不是旧二进制，而是签名密钥、DSN 与全部任务产物。
func TestBinaryCleanupOnlyDeletesUpdateResidue(t *testing.T) {
	allowed := []string{"blade.old", "blade.failed", "blade.new", "blade.upgrade.json", "blade.old.exe"}
	for _, name := range allowed {
		if !isBinaryResidue(name) {
			t.Fatalf("%q 是更新残留，应允许清理", name)
		}
	}
	denied := []string{
		"jwt.key", "config.json", "data", "skills", "start.sh", "blade",
		"blade-old", "blade.old.bak", "blade.upgrade.json.bak", "", ".", "..",
		"sub/blade.old", "blade.exe",
	}
	for _, name := range denied {
		if isBinaryResidue(name) {
			t.Fatalf("%q 不是更新残留，绝不能被清理", name)
		}
	}
}
