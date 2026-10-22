package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wsResolve 只做词法收口不够：工作区里一个指向外部的软链（agent 随手能建）会让
// os.ReadFile 跟着走出去。这里把两条路都钉住。
func TestWorkspaceResolveRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	s := &Server{m: &Manager{dir: root}}
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 正常路径与"写入还不存在的文件"必须放行。
	for _, rel := range []string{"a/b.txt", "newdir/new.txt", ""} {
		if _, ok := s.wsResolve(rel); !ok {
			t.Fatalf("正常路径 %q 被拒", rel)
		}
	}
	// ".." 在词法层就出不去：路径先以 "/" 为根收口，拼出来的永远在工作区内（可能不存在）。
	// 钉住这个契约：不报错，但结果必须在 root 之下。
	for _, rel := range []string{"../../etc/passwd", "/../etc/passwd", "a/../../x"} {
		abs, ok := s.wsResolve(rel)
		if !ok {
			t.Fatalf("%q 不该报错（它会被收口到工作区内）", rel)
		}
		if !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			t.Fatalf("%q 被解析到了工作区外：%q", rel, abs)
		}
	}

	// 软链 → 外部文件 / 外部目录：都必须拒。
	if err := os.Symlink(outsideFile, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"link.txt", "linkdir/new.txt", "linkdir/../link.txt"} {
		if _, ok := s.wsResolve(rel); ok {
			t.Fatalf("软链逃逸 %q 没被拦（会读到工作区外的文件）", rel)
		}
	}

	// 软链 → 工作区内部：允许（例如任务里给同一文件建个短名）。
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "inside.txt")); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.wsResolve("inside.txt"); !ok {
		t.Fatal("工作区内部的软链不该被拒")
	}
}

// 工作区根目录本身是软链（部署时 data -> 另一块盘）时，一切照常——否则整个文件面板
// 会在这种部署形态下全部 404。
func TestWorkspaceResolveAllowsSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "data")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{dir: link}}
	if _, ok := s.wsResolve("f.txt"); !ok {
		t.Fatal("软链根目录下的正常文件被拒")
	}
	// 但逃出这个软链根同样要拦。
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(real, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.wsResolve("escape/x"); ok {
		t.Fatal("从软链根里指向外部的软链没被拦")
	}
}
