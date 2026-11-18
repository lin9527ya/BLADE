package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHHostKeyOptsUsesRunDir(t *testing.T) {
	dir := t.TempDir()
	ts := NewToolSet(nil, "test")
	ts.SetRunDir(dir)
	opts := ts.sshHostKeyOpts()
	want := filepath.Join(dir, ".ssh", "known_hosts")
	if !strings.Contains(opts, want) {
		t.Fatalf("known_hosts 未落在 runDir：%q", opts)
	}
	if strings.Contains(opts, "/dev/null") {
		t.Fatal("仍在用 /dev/null，等于不校验主机密钥")
	}
	if fi, err := os.Stat(filepath.Join(dir, ".ssh")); err != nil || !fi.IsDir() {
		t.Fatalf(".ssh 目录未创建：%v", err)
	}
	cmd := buildLootCommand("password", "ssh", "app", "web01", 22, "/tmp/secret", "id", opts)
	if !strings.Contains(cmd, want) {
		t.Fatalf("ssh 命令未带 known_hosts：%s", cmd)
	}
	if strings.Contains(cmd, "/dev/null") {
		t.Fatalf("ssh 命令仍指向 /dev/null：%s", cmd)
	}
}

func TestSSHHostKeyOptsWithoutRunDir(t *testing.T) {
	ts := NewToolSet(nil, "test")
	opts := ts.sshHostKeyOpts()
	if !strings.Contains(opts, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("缺少 accept-new：%q", opts)
	}
	if strings.Contains(opts, "UserKnownHostsFile") {
		t.Fatalf("无 runDir 时应使用执行用户默认 known_hosts：%q", opts)
	}
}
