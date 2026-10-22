package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snapshotIso 保存/恢复进程级隔离状态，避免测试之间互相污染（InstallAgentIsolation
// 写的就是这个全局变量）。
func snapshotIso(t *testing.T) {
	t.Helper()
	old := agentIso
	t.Cleanup(func() { agentIso = old })
}

func TestScrubAgentEnvDropsCredentialsKeepsToolchain(t *testing.T) {
	snapshotIso(t)
	agentIso = agentIsoState{enabled: true, user: "blade-worker", home: "/home/blade-worker"}

	in := []string{
		"PATH=/usr/bin:/bin",
		"JAVA_HOME=/usr/lib/jvm/default",
		"NODE_OPTIONS=--max-old-space-size=2048",
		"ANTHROPIC_API_KEY=sk-ant-xxx",
		"OPENAI_API_KEY=sk-xxx",
		"BLADE_PG_DSN=postgres://blade:secret@127.0.0.1/blade",
		"AWS_SECRET_ACCESS_KEY=xxx",
		"GITHUB_TOKEN=ghp_xxx",
		"GPG_AGENT_INFO=/run/user/0/gpg",
		"SSH_AUTH_SOCK=/tmp/ssh-agent.sock",
		"HOME=/root",
		"USER=root",
		"LOGNAME=root",
		"DB_PASSWORD=hunter2",
		"MY_CREDENTIALS=xxx",
		"TOKENIZERS_PARALLELISM=false",
	}
	out := scrubAgentEnv(in)
	joined := strings.Join(out, "\n")

	for _, keep := range []string{"PATH=", "JAVA_HOME=", "NODE_OPTIONS="} {
		if !strings.Contains(joined, keep) {
			t.Errorf("工具链变量 %s 被误删：agent 的常规命令会坏", keep)
		}
	}
	for _, drop := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "BLADE_PG_DSN",
		"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "GPG_AGENT_INFO", "SSH_AUTH_SOCK",
		"DB_PASSWORD", "MY_CREDENTIALS"} {
		if strings.Contains(joined, drop+"=") {
			t.Errorf("凭据类变量 %s 泄漏给子进程", drop)
		}
	}
	if !strings.Contains(joined, "HOME=/home/blade-worker") || !strings.Contains(joined, "USER=blade-worker") {
		t.Errorf("HOME/USER 没改写成 worker：\n%s", joined)
	}
}

// TestScrubAgentEnvLastWins 钉住去重语义：BLADE 在 os.Environ() 之后 append 覆盖项
// （如代理 env），libc getenv 取第一个，所以必须保留**最后一次**出现的值。
func TestScrubAgentEnvLastWins(t *testing.T) {
	snapshotIso(t)
	agentIso = agentIsoState{enabled: true, user: "w", home: "/home/w"}
	out := scrubAgentEnv([]string{"HTTP_PROXY=http://old:1", "HTTP_PROXY=http://new:2", "HTTP_PROXY=http://newest:3"})
	got := strings.Join(out, ",")
	if strings.Count(got, "HTTP_PROXY=") != 1 || !strings.Contains(got, "http://newest:3") {
		t.Fatalf("去重结果 = %q，要保留最后一次出现", got)
	}
}

func TestAgentEnvDenied(t *testing.T) {
	for _, k := range []string{"ANTHROPIC_API_KEY", "blade_agent_user", "GH_TOKEN", "SSH_AUTH_SOCK", "HOME", "some_secret"} {
		if !agentEnvDenied(k) {
			t.Errorf("%s 应当被清洗", k)
		}
	}
	for _, k := range []string{"PATH", "JAVA_HOME", "LANG", "TERM", "PLAYWRIGHT_BROWSERS_PATH", "http_proxy"} {
		if agentEnvDenied(k) {
			t.Errorf("%s 不该被清洗", k)
		}
	}
}

func TestAgentPathGuardAllowsRootsOnly(t *testing.T) {
	snapshotIso(t)
	root := t.TempDir()
	agentIso = agentIsoState{enabled: true, roots: []string{root}}

	if err := agentPathGuard(root); err != nil {
		t.Fatalf("根目录本身被拒：%v", err)
	}
	if err := agentPathGuard(filepath.Join(root, "tasks", "1", "a.txt")); err != nil {
		t.Fatalf("子路径被拒：%v", err)
	}
	// 前缀相同但不是子目录（/x/data 与 /x/database）不能被放行。
	sibling := root + "base"
	if err := agentPathGuard(sibling); err == nil {
		t.Fatalf("%s 只是前缀相同，不该被当成根内路径", sibling)
	}
	if err := agentPathGuard("/etc/passwd"); err == nil {
		t.Fatal("/etc/passwd 应被拒")
	}

	// 未启用隔离 = 不设限（保持旧行为）。
	agentIso = agentIsoState{}
	if err := agentPathGuard("/etc/passwd"); err != nil {
		t.Fatalf("未启用时不该拦截：%v", err)
	}
}

func TestAgentAllowedRootsResolvesSymlinksAndRejectsMissing(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	skill := filepath.Join(dir, "skills")
	for _, d := range []string{data, skill} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "data-link")
	if err := os.Symlink(data, link); err != nil {
		t.Fatal(err)
	}

	roots, err := agentAllowedRoots(data, skill, link)
	if err != nil {
		t.Fatal(err)
	}
	realData, _ := filepath.EvalSymlinks(data)
	// data、skills、软链（解析后等于 data）→ 去重后 2 个，且都是真实路径。
	if len(roots) != 2 {
		t.Fatalf("roots = %v，期望 2 个（软链去重后）", roots)
	}
	for _, r := range roots {
		if r == link {
			t.Fatalf("允许根没解析成真实路径：%s", r)
		}
	}
	found := false
	for _, r := range roots {
		if r == realData {
			found = true
		}
	}
	if !found {
		t.Fatalf("roots = %v，缺少真实 data 根 %s", roots, realData)
	}

	if _, err := agentAllowedRoots(data, skill, filepath.Join(dir, "nope")); err == nil {
		t.Fatal("不存在的额外根应当报错（启动期暴露，而不是运行时静默失效）")
	}
}

func TestInstallAgentIsolationDisabledByDefault(t *testing.T) {
	snapshotIso(t)
	t.Setenv("BLADE_AGENT_USER", "")
	agentIso = agentIsoState{}
	if err := InstallAgentIsolation(t.TempDir(), t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if agentIso.enabled {
		t.Fatal("未设置 BLADE_AGENT_USER 时不该启用隔离")
	}
}

// TestInstallAgentIsolationRejectsDataDirContainingKeyDir 钉住一条 fail-closed 检查：
// -data 指向可执行文件目录时，jwt.key/config.json 就落在 agent 的可写树里，降权等于没降。
func TestInstallAgentIsolationRejectsDataDirContainingKeyDir(t *testing.T) {
	snapshotIso(t)
	dir := t.TempDir()
	t.Setenv("BLADE_AGENT_USER", "root") // 用 root 绕开"找不到用户"，让检查先命中
	err := InstallAgentIsolation(filepath.Join(dir, "data"), dir, filepath.Join(dir, "skills"))
	if err == nil {
		t.Fatal("key 目录在 data 目录里应当拒绝启动")
	}
}

// TestPrepareAgentStateDirsHandsTreeToAgent 用当前 uid/gid 代替 worker（测试进程不是
// root，chown 到别人会失败）：验证整棵 data 树的属主与目录模式（setgid + 组可写）。
func TestPrepareAgentStateDirsHandsTreeToAgent(t *testing.T) {
	snapshotIso(t)
	root := t.TempDir()
	run := filepath.Join(root, "tasks", "1", "i2")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(run, "artifacts.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentIso = agentIsoState{enabled: true, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}
	if err := prepareAgentStateDirs(root); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{root, filepath.Join(root, "tasks"), run} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSetgid == 0 {
			t.Errorf("%s 没有 setgid：之后服务端新建的子目录不会继承 worker 组", d)
		}
		if fi.Mode().Perm()&0o020 == 0 {
			t.Errorf("%s 组不可写：降权后的 agent 写不进自己的产物目录", d)
		}
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("普通文件的权限不该被改动：%v %v", fi.Mode(), err)
	}
}
