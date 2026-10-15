package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStorageUnitExpired 钉住保留判定的三条硬规则：受保护的不删、0 天不清理、
// 边界（正好等于保留期）不算过期——边界差一天就误删一整批，方向必须明确。
func TestStorageUnitExpired(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.Local)
	days := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	cases := []struct {
		name    string
		unit    storageUnit
		days    int
		expired bool
	}{
		{"0 天 = 不清理", storageUnit{ModTime: days(999)}, 0, false},
		{"负数也当不清理", storageUnit{ModTime: days(999)}, -3, false},
		{"超出保留期", storageUnit{ModTime: days(31)}, 30, true},
		{"正好等于保留期：不删（边界保守）", storageUnit{ModTime: days(30)}, 30, false},
		{"未到期", storageUnit{ModTime: days(29)}, 30, false},
		{"受保护的一律不删（即使很老）", storageUnit{ModTime: days(999), Protected: "任务未到终态（在跑/排队/暂停）"}, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := storageUnitExpired(c.unit, c.days, now); got != c.expired {
				t.Fatalf("expired 应为 %v，实际 %v", c.expired, got)
			}
		})
	}
}

// TestPathWithin 钉住删除前的包含性检查（防符号链接/拼接把删除带出分类目录）。
func TestPathWithin(t *testing.T) {
	root := "/data/tasks"
	cases := []struct {
		path string
		want bool
	}{
		{"/data/tasks/1", true},
		{"/data/tasks/1/executions/a.log", true},
		{"/data/tasks-evil/1", false}, // 前缀相同但不是子路径——最容易写错的一种
		{"/data/tasks", false},        // 等于根本身：不是"之内"
		{"/data", false},
		{"/etc/passwd", false},
		{"", false},
	}
	for _, c := range cases {
		if got := pathWithin(root, c.path); got != c.want {
			t.Fatalf("pathWithin(%q)=%v，应为 %v", c.path, got, c.want)
		}
	}
}

// TestScanStorageUnit 覆盖统计口径：递归求和、取**最新写入时间**；
// 空目录用目录自身 mtime（否则空目录永远不过期）；符号链接不跟随（不把统计带出数据目录）。
func TestScanStorageUnit(t *testing.T) {
	root := t.TempDir()
	unit := filepath.Join(root, "task-1")
	if err := os.MkdirAll(filepath.Join(unit, "executions"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAt := func(p string, size int, at time.Time) {
		t.Helper()
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	writeAt(filepath.Join(unit, "a.txt"), 10, old)
	writeAt(filepath.Join(unit, "executions", "b.log"), 32, newer)

	// 外部目录挂成符号链接：不计入（链接本身不是文件，跟随会把外面的算进来）
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAt(filepath.Join(outside, "big.bin"), 4096, newer)
	if err := os.Symlink(outside, filepath.Join(unit, "link")); err != nil {
		t.Skipf("本机不支持符号链接：%v", err)
	}

	bytes, mod, err := scanStorageUnit(unit)
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 42 {
		t.Fatalf("应只统计单元内的 10+32=42 字节，实际 %d", bytes)
	}
	if diff := mod.Sub(newer); diff > time.Second || diff < -time.Second {
		t.Fatalf("最新写入时间应为 %v，实际 %v", newer, mod)
	}

	// 空目录：用目录自身 mtime（且不会 panic）
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	_, mod2, err := scanStorageUnit(empty)
	if err != nil {
		t.Fatal(err)
	}
	if mod2.IsZero() {
		t.Fatal("空目录也要给出时间（目录自身 mtime），否则它永远不过期")
	}
}
