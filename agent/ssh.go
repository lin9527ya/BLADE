package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// sshHostKeyOpts 返回 SSH 主机密钥校验选项。
//
// 旧实现用 `UserKnownHostsFile=/dev/null`，等于每次都不校验主机密钥——同一条隧道/凭据
// 连接在会话中途被替换主机也发现不了。现在把 known_hosts 放在本次 run 的工作目录
// （`<runDir>/.ssh/known_hosts`，0700 目录）：执行 SSH 的 agent 用户可写，连接同一目标
// 时能发现密钥变化；没有 runDir 时退回执行用户的默认 known_hosts，绝不再写 /dev/null。
func (t *ToolSet) sshHostKeyOpts() string {
	base := "-o StrictHostKeyChecking=accept-new"
	if t == nil || t.runDir == "" {
		return base
	}
	dir := filepath.Join(t.runDir, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return base
	}
	_ = os.Chmod(dir, 0o700)
	file := filepath.Join(dir, "known_hosts")
	return fmt.Sprintf("%s -o UserKnownHostsFile=%s", base, shQuote(file))
}
