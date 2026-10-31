package db

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// readSourceForTest 读本包源码（测试用；路径相对本文件定位，不依赖工作目录）。
func readSourceForTest(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), name))
	if err != nil {
		t.Fatalf("读取 %s: %v", name, err)
	}
	return string(b)
}
