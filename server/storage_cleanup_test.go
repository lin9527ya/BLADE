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

func TestScanNoaStorageCategoryAndScopedHelpers(t *testing.T) {
	dataDir := t.TempDir()
	old := time.Now().Add(-10 * 24 * time.Hour)
	seedFile := func(rel string) {
		t.Helper()
		p := filepath.Join(dataDir, "noa", rel, "state.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 128), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
	}
	seedFile("exp12-planner")
	seedFile("exp13-planner")
	seedFile("chat-conv-7")
	seedFile("chat-conv-8")

	s := &Server{m: &Manager{dir: dataDir}}
	s.chatRuns.busy = map[string]bool{"conv-7": true}

	spec, ok := storageCategorySpecByKey("noa")
	if !ok || spec.SettingKey != settingStorageRetentionNoa {
		t.Fatalf("unexpected noa storage category spec: ok=%v spec=%+v", ok, spec)
	}
	if root := s.storageCategoryRoot("noa"); root != filepath.Join(dataDir, "noa") {
		t.Fatalf("storageCategoryRoot(noa) = %q", root)
	}

	rep, err := s.scanStorageCategory(spec, 7, nil, map[int64]bool{12: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Units != 4 || rep.Protected != 2 || rep.ExpiredUnits != 2 || rep.ExpiredBytes != 256 {
		t.Fatalf("noa category scan mismatch: %+v", rep)
	}
	byName := make(map[string]storageUnit, len(rep.Items))
	for _, item := range rep.Items {
		byName[item.Name] = item
	}
	if byName["exp12-planner"].Protected == "" || byName["chat-conv-7"].Protected == "" {
		t.Fatalf("expected active exploration & busy conversation protected: %+v", byName)
	}
	if byName["exp13-planner"].Protected != "" || byName["chat-conv-8"].Protected != "" {
		t.Fatalf("expected finished exploration & idle conversation cleanable: %+v", byName)
	}

	// Verify per-task noa scope parsing, scanExpRoot, removeExpPrefixedEntries and conv cleanup.
	_, _, noaScope, _, err := parseStorageCleanScopes([]string{"noa"})
	if err != nil || !noaScope {
		t.Fatalf("parseStorageCleanScopes(noa) = noa:%v err:%v", noaScope, err)
	}
	items := map[string]*storageTaskItem{}
	scanExpRoot(filepath.Join(dataDir, "noa"), items, map[int64]string{13: "99"}, "孤儿 noa (%d)", func(it *storageTaskItem, sz int64) {
		it.NoaBytes += sz
		it.NoaCount++
	})
	if items["exp-12"] == nil || items["exp-12"].NoaBytes != 128 {
		t.Fatalf("expected orphan exp-12 stats, got %+v", items["exp-12"])
	}
	if items["99"] == nil || items["99"].NoaBytes != 128 {
		t.Fatalf("expected mapped task 99 stats, got %+v", items["99"])
	}
	removed, errs := removeExpPrefixedEntries(filepath.Join(dataDir, "noa"), "exp13-", "task #99 noa")
	if removed != 1 || len(errs) != 0 {
		t.Fatalf("removeExpPrefixedEntries = %d, errs=%v", removed, errs)
	}
	s.removeConversationNoaDir(8)
	for _, gone := range []string{"exp13-planner", "chat-conv-8"} {
		if _, err := os.Stat(filepath.Join(dataDir, "noa", gone)); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, got %v", gone, err)
		}
	}
	for _, kept := range []string{"exp12-planner", "chat-conv-7"} {
		if _, err := os.Stat(filepath.Join(dataDir, "noa", kept)); err != nil {
			t.Fatalf("expected %s kept, got %v", kept, err)
		}
	}
}
