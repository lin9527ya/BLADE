//go:build windows

package server

import (
	"os/exec"
	"syscall"
)

// Windows：新进程组（Ctrl-C 不波及），杀进程用 Kill（无组信号语义；子进程由
// 任务管理器语义兜底——单机 Windows 部署非本产品主场景，行为降级但不假成功）。
func startDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func shellCommandArgs(command string) []string { return []string{"cmd", "/c", command} }

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func hardKillGroup(cmd *exec.Cmd) { killGroup(cmd) }
