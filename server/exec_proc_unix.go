//go:build !windows

package server

import (
	"os/exec"
	"syscall"
)

// startDetached 让长任务进程独立成会话组（Setsid）：不随父进程组收到终端信号，
// 也让"杀整组"成为可能（killGroup 对 -pgid 发信号）。
func startDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// shellCommandArgs 用 /bin/sh -c 执行完整命令（与 Bash 工具同口径）。
func shellCommandArgs(command string) []string { return []string{"/bin/sh", "-c", command} }

// signalGroup 给进程组发信号（负 pid）。进程已退出时返回错误，调用方忽略。
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

// killGroup 先 SIGTERM 后（宽限后由调用方）SIGKILL 整个进程组。
func killGroup(cmd *exec.Cmd)     { signalGroup(cmd, syscall.SIGTERM) }
func hardKillGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGKILL) }
