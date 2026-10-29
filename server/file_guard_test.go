package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
)

func TestFileRootGuardAppliesWithoutPrivilegeDrop(t *testing.T) {
	oldGuard := fileGuard
	t.Cleanup(func() {
		fileGuard = oldGuard
		actool.SetPathGuard(nil)
	})
	t.Setenv(settingFileRootsOpenEnv, "")

	data := t.TempDir()
	skills := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "tasks", "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InstallFileRootGuard(data, skills); err != nil {
		t.Fatal(err)
	}
	if !fileGuard.installed {
		t.Fatal("guard must install even when BLADE_AGENT_USER is unset")
	}

	inside := filepath.Join(data, "tasks", "1", "out.txt")
	if err := agentPathGuard(inside); err != nil {
		t.Fatalf("task dir must stay writable: %v", err)
	}
	if err := agentPathGuard(filepath.Join(skills, "redteam-opsec", "SKILL.md")); err != nil {
		t.Fatalf("skill dir must stay readable: %v", err)
	}
	outside := filepath.Join(string(filepath.Separator), "etc", "passwd")
	if err := agentPathGuard(outside); err == nil {
		t.Fatalf("path outside the roots must be rejected: %s", outside)
	}
	if !strings.Contains(agentPathGuard(outside).Error(), "拒绝访问") {
		t.Fatal("rejection should explain the boundary")
	}
}

func TestFileRootGuardEscapeHatch(t *testing.T) {
	oldGuard := fileGuard
	t.Cleanup(func() {
		fileGuard = oldGuard
		actool.SetPathGuard(nil)
	})
	t.Setenv(settingFileRootsOpenEnv, "1")
	if err := InstallFileRootGuard(t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if fileGuard.installed {
		t.Fatal("escape hatch must not install the guard")
	}
	if err := agentPathGuard("/etc/passwd"); err != nil {
		t.Fatalf("escape hatch should allow any path: %v", err)
	}
}
