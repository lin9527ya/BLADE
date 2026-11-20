package agent

import (
	"path/filepath"
	"testing"
)

func TestSessionJarAlwaysUsesExplicitTaskRoot(t *testing.T) {
	root := t.TempDir()
	taskRoot := filepath.Join(root, "tasks", "7")
	intentDir := filepath.Join(taskRoot, "i11")
	planner := NewToolSet(nil, "planner")
	planner.SetRunDir(taskRoot)
	worker := NewToolSet(nil, "worker")
	worker.SetRunDir(intentDir)
	plannerJar := planner.sessionJarPath("admin")
	workerJar := worker.sessionJarPath("admin")
	want := filepath.Join(taskRoot, "shared", "sessions", "admin.jar")
	if plannerJar != want || workerJar != want {
		t.Fatalf("jar paths planner=%q worker=%q want=%q", plannerJar, workerJar, want)
	}
	if got := planner.sharedDir(); got != filepath.Join(taskRoot, "shared") {
		t.Fatalf("shared dir escaped task root: %q", got)
	}
	other := NewToolSet(nil, "worker")
	other.SetRunDir(filepath.Join(root, "tasks", "8", "i1"))
	if other.sessionJarPath("admin") == plannerJar {
		t.Fatal("different task roots must not share a cookie jar")
	}
	if got := worker.sessionJarPath("../admin/../../escape"); got != filepath.Join(taskRoot, "shared", "sessions", "adminescape.jar") {
		t.Fatalf("session name was not sanitized: %q", got)
	}
	unknown := NewToolSet(nil, "worker")
	unknown.SetRunDir(filepath.Join(root, "not-a-task", "i1"))
	if unknown.sessionJarPath("admin") != "" || unknown.sharedDir() != "" {
		t.Fatal("unknown run-dir shape must not fall back to a global shared directory")
	}
	explicit := NewToolSet(nil, "worker")
	explicit.SetRunDir(filepath.Join(root, "elsewhere", "i1"))
	explicit.SetTaskRoot(taskRoot)
	if got := explicit.sessionJarPath("admin"); got != want {
		t.Fatalf("explicit task root ignored: %q", got)
	}
}
