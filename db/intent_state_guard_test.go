package db

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestSetIntentStateOnlyUsedInTests 钉住一条边界：无状态谓词的 SetIntentState
// 只允许测试/播种使用，生产代码必须走 CompareAndSetIntentState。
//
// 为什么值得一条静态断言：这是个"用了也不会报错"的 API——并发下它会把 worker 的
// 收尾结论覆盖回去，表现成"状态莫名其妙地变回旧值"。靠 code review 记这件事不可靠，
// 靠编译期又区分不了测试与生产；扫一遍源码最便宜。
func TestSetIntentStateOnlyUsedInTests(t *testing.T) {
	root := repoRootForTest(t)
	forbidden := regexp.MustCompile(`\.SetIntentState\(`)
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "web", "node_modules", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// 定义本身在 db/exploration.go 里，不在调用之列。
		if filepath.Base(path) == "exploration.go" {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if forbidden.Match(body) {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("生产代码里仍在用无状态谓词的 SetIntentState（应改用 CompareAndSetIntentState）：%v", offenders)
	}
}

// repoRootForTest 从本文件位置回溯到仓库根。
func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	return filepath.Dir(filepath.Dir(file))
}
